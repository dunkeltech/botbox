// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowLeftRight } from 'lucide-react';
import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocale, useTranslations } from 'next-intl';
import { ChatDayDivider } from '@/components/chat/ChatDayDivider';
import { ChatTimestamp } from '@/components/chat/ChatTimestamp';
import {
  formatTimeLabel,
  isDayBoundary,
  localDayKey,
} from '@/components/chat/chatDate';
import {
  ChatTranscriptRow,
  UnattachedToolTurnRow,
} from '@/components/chat/ChatTranscriptRow';
import { ChatTurnToolChip } from '@/components/chat/ChatTurnToolChip';
import { LiveToolStatusBar } from '@/components/chat/LiveToolStatusBar';
import { StopTurnButton } from '@/components/chat/StopTurnButton';
import {
  buildToolTrace,
  type ToolTraceSummary,
  type ToolTraceTurn,
} from '@/components/chat/toolTrace';
import { BrokerTakeoverModal } from '@/components/chat/BrokerTakeoverModal';
import { MarkdownMessage } from '@/components/chat/MarkdownMessage';
import { SecretRequestModal } from '@/components/chat/SecretRequestModal';
import { useSecretRequestControl } from '@/components/chat/useSecretRequestControl';
import { useChatScroll } from '@/components/chat/useChatScroll';
import { useConversationHistory } from '@/components/chat/useConversationHistory';
import { turnFailureKey } from '@/components/chat/turnFailure';
import { activeBotTurns, failedBotTurns } from '@/components/team/activity';
import { ApprovalCard } from '@/components/team/ApprovalCard';
import { GroupMessageComposer } from '@/components/team/GroupMessageComposer';
import {
  approvalForMessage,
  canDecideApproval,
} from '@/components/team/approvals';
import {
  approvalsQueryKey,
  useApprovalDecision,
  useApprovalList,
} from '@/components/team/useApprovals';
import { Alert, BotAvatar, Button } from '@/components/ui';
import { connectConversation } from '@/lib/conversation-events';
import { getAccessToken, requestJson } from '@/lib/api-client';
import { canManageBot } from '@/lib/rbac';
import type { Bot, BotListResponse, WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import { initials, type MentionTarget } from '@/components/team/text';

type Group = components['schemas']['Group'];
type History = components['schemas']['ChatHistory'];
type ChatMessage = components['schemas']['ChatMessage'];

function targets(members: Group['members']): MentionTarget[] {
  const named: MentionTarget[] = members.flatMap((member) => {
    const name = member.name?.trim();
    if (!name) return [];
    const roleDescription = member.roleDescription?.trim() || undefined;
    return [{ id: member.id, name, kind: member.type, roleDescription }];
  });
  return [...named, { id: 'all', name: 'all', kind: 'all' }];
}

function speaker(
  message: ChatMessage,
  group: Group,
  userId: string,
  you: string,
  bot: string,
  human: string,
  system: string,
): { name: string; kind: 'you' | 'bot' | 'human' | 'system' } {
  if (message.senderType === 'system' || message.kind === 'system') {
    return { name: system, kind: 'system' };
  }
  if (message.senderUserId && message.senderUserId === userId) {
    return { name: you, kind: 'you' };
  }
  const member = group.members.find(
    (row) => row.id === message.senderUserId || row.id === message.senderBotId,
  );
  if (message.senderType === 'bot' || message.senderBotId) {
    return { name: member?.name || bot, kind: 'bot' };
  }
  return { name: member?.name || human, kind: 'human' };
}

/** Group transcript, mention listbox, and composer. Viewers get no post control. */
export function GroupConversation({
  workspaceId,
  userId,
  group,
  role,
  canPost,
  query,
  selectedToolTurnKey = null,
  onInspectToolTurn,
  onToolTraceChange,
  scrollRequest = null,
}: {
  workspaceId: string;
  userId: string;
  group: Group;
  role: WorkspaceRole;
  canPost: boolean;
  query: string;
  selectedToolTurnKey?: string | null;
  onInspectToolTurn?: (turnKey: string) => void;
  onToolTraceChange?: (trace: ToolTraceSummary) => void;
  scrollRequest?: { messageId: string; nonce: number } | null;
}) {
  const t = useTranslations('team');
  const tChat = useTranslations('chat');
  const client = useQueryClient();
  const [historyError, setHistoryError] = useState(false);
  const base = `/api/workspaces/${workspaceId}`;
  const history = useQuery({
    queryKey: ['group-messages', workspaceId, group.id],
    queryFn: () => requestJson<History>(`${base}/groups/${group.id}/messages`),
    refetchInterval: 3000,
  });
  const botsQuery = useQuery({
    queryKey: ['bots', workspaceId],
    enabled: role === 'member',
    queryFn: () => requestJson<BotListResponse>(`${base}/bots`),
  });
  function canManageMemoryBot(targetBotId?: string): boolean {
    if (role === 'owner' || role === 'admin') return true;
    if (role !== 'member' || !targetBotId) return false;
    const matchedBot: Bot | undefined = botsQuery.data?.bots.find(
      (b) => b.id === targetBotId,
    );
    return canManageBot(role, userId, matchedBot);
  }
  const approvalList = useApprovalList(workspaceId);
  const {
    decide,
    busyId,
    error: decisionError,
  } = useApprovalDecision(workspaceId);
  async function refreshHistory() {
    await client.invalidateQueries({
      queryKey: ['group-messages', workspaceId, group.id],
    });
  }
  const secretControl = useSecretRequestControl(workspaceId, refreshHistory);
  useEffect(() => {
    const token = getAccessToken();
    if (!token) return;
    const refresh = () => {
      void client.invalidateQueries({
        queryKey: ['group-messages', workspaceId, group.id],
      });
      void client.invalidateQueries({
        queryKey: ['notifications', workspaceId],
      });
      void client.invalidateQueries({
        queryKey: approvalsQueryKey(workspaceId),
      });
      void client.invalidateQueries({ queryKey: ['bots', workspaceId] });
    };
    return connectConversation(
      workspaceId,
      group.id,
      getAccessToken,
      (event) => {
        try {
          const data: unknown = JSON.parse(event.data);
          if (!data || typeof data !== 'object') return;
          // Text deltas are not persisted replies and need no snapshot reload.
          if ('Kind' in data && data.Kind === 'text_delta') return;
          refresh();
        } catch {
          /* Invalid signals cannot change current activity. */
        }
      },
      refresh,
    );
  }, [client, group.id, workspaceId]);

  const needle = query.trim().toLowerCase();
  const preparePrependRef = useRef<() => void>(() => {});
  const fetchOlderPage = useCallback(
    (beforePosition: number) =>
      requestJson<History>(
        `${base}/groups/${group.id}/messages?before=${beforePosition}`,
      ),
    [base, group.id],
  );
  const { rawMessages, hasMore, loadingOlder, loadOlder } =
    useConversationHistory({
      conversationKey: group.id,
      latestPage: history.data,
      fetchOlderPage,
      preparePrepend: () => preparePrependRef.current(),
      onError: () => setHistoryError(true),
    });

  const trace = useMemo(
    () =>
      buildToolTrace({
        messages: rawMessages,
        turns: history.data?.turns,
        imagePathForMessage: (messageId) =>
          `${base}/groups/${group.id}/messages/${messageId}/tool-image`,
      }),
    [rawMessages, history.data?.turns, base, group.id],
  );
  useEffect(() => {
    onToolTraceChange?.(trace);
  }, [trace, onToolTraceChange]);

  function requestBotName(requestId?: string) {
    const message = trace.chatMessages.find(
      (message) =>
        requestId && message.secretRequest?.secretRequestId === requestId,
    );
    return (
      group.members.find(
        (member) => member.type === 'bot' && member.id === message?.senderBotId,
      )?.name ?? t('bot')
    );
  }

  const messages = useMemo(
    () =>
      trace.chatMessages.filter(
        (message) =>
          needle === '' || message.content.toLowerCase().includes(needle),
      ),
    [trace.chatMessages, needle],
  );
  const mentionTargets = useMemo(() => targets(group.members), [group.members]);
  const mentionNames = useMemo(
    () => mentionTargets.map((target) => target.name),
    [mentionTargets],
  );
  const { logRef, onScroll, onMediaLoad, scrollToBottom, preparePrepend } =
    useChatScroll({
      conversationKey: group.id,
      messageCount: messages.length,
      firstMessageId: messages[0]?.id ?? '',
      lastMessageId: messages[messages.length - 1]?.id ?? '',
      onReachTop: () => {
        void loadOlder();
      },
    });
  preparePrependRef.current = preparePrepend;

  useEffect(() => {
    if (!scrollRequest?.messageId || !logRef.current) return;
    const target = logRef.current.querySelector<HTMLElement>(
      `[data-message-id="${CSS.escape(scrollRequest.messageId)}"]`,
    );
    if (target && typeof target.scrollIntoView === 'function') {
      target.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }, [scrollRequest, logRef]);

  const activeTurns = activeBotTurns(history.data?.turns ?? []);
  const failedTurns = failedBotTurns(history.data?.turns ?? []);
  const activeToolTurns = trace.turns.filter(
    (turn) =>
      turn.status === 'running' &&
      !activeTurns.some((active) => active.id === turn.turnKey),
  );

  function renderUnattachedToolTurns(turns: ToolTraceTurn[]) {
    return turns.map((turn) => {
      const member = group.members.find((row) => row.id === turn.botId);
      return (
        <UnattachedToolTurnRow
          key={turn.turnKey}
          turn={turn}
          name={member?.name || t('bot')}
          avatarSeed={member?.id ?? turn.botId ?? group.leadBotId ?? group.id}
          layout="group"
          selected={selectedToolTurnKey === turn.turnKey}
          onInspect={onInspectToolTurn}
        />
      );
    });
  }

  function renderConversationMessage(message: ChatMessage) {
    const who = speaker(
      message,
      group,
      userId,
      t('you'),
      t('bot'),
      t('human'),
      t('system'),
    );
    // Kind approval is the ask-before card. System and card rows stay
    // notices, including the deny line the API appends after a decision.
    if (message.kind === 'approval') {
      const matched = approvalForMessage(
        approvalList.data?.approvals ?? [],
        message.id,
        group.id,
      );
      const bot = group.members.find((member) => member.id === matched?.botId);
      return (
        <ApprovalCard
          className="mx-5"
          approval={matched}
          content={message.content}
          botName={bot?.name}
          where={matched ? group.name : undefined}
          avatarSeed={bot?.id ?? matched?.botId}
          canDecide={matched ? canDecideApproval(role, matched) : false}
          busy={busyId === matched?.id}
          errorKey={
            matched && decisionError?.id === matched.id
              ? decisionError.key
              : null
          }
          onDecide={
            matched
              ? (decision) => void decide(matched.id, decision)
              : undefined
          }
        />
      );
    }
    if (
      message.kind === 'system' ||
      message.kind === 'card' ||
      message.senderType === 'system'
    ) {
      return (
        <div className="mx-5 flex items-center gap-2 rounded-sm border border-mint-line bg-mint-soft px-3.5 py-2 text-[13px] text-dark">
          <ArrowLeftRight
            className="h-4 w-4 shrink-0 text-green-600"
            aria-hidden="true"
          />
          <span>{message.content}</span>
        </div>
      );
    }
    const botMember = group.members.find(
      (member) => member.id === message.senderBotId,
    );
    const attachedTurn = trace.turnsByReplyId.get(message.id);
    return (
      <article data-message-id={message.id} className="flex gap-3 px-5 py-1">
        {who.kind === 'bot' ? (
          <BotAvatar
            seed={
              botMember?.id ??
              message.senderBotId ??
              group.leadBotId ??
              group.id
            }
            size={36}
            decorative
          />
        ) : (
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-dark text-[13px] font-black text-paper">
            {initials(who.name)}
          </span>
        )}
        <div className="min-w-0 flex-1">
          <p className="flex flex-wrap items-baseline gap-2">
            <span className="text-[15px] font-black text-ink">{who.name}</span>
            <span
              className={`rounded-xs px-1.5 py-0.5 text-[11px] font-black uppercase tracking-[0.06em] ${
                who.kind === 'bot'
                  ? 'bg-mint-soft text-green-600'
                  : 'bg-blue-tint text-blue'
              }`}
            >
              {message.kind === 'progress'
                ? t(message.progressBlocker ? 'blockerBadge' : 'progressBadge')
                : message.kind === 'team_status'
                  ? t('statusCheckBadge')
                  : who.kind === 'bot'
                    ? t('botBadge')
                    : t('humanBadge')}
            </span>
            <ChatTimestamp
              iso={message.createdAt}
              className="text-[12px] text-muted"
            />
          </p>
          {message.kind === 'team_status' && message.teamStatus ? (
            <TeamStatusUpdate status={message.teamStatus} />
          ) : (
            <MarkdownMessage
              content={message.content}
              mentionNames={mentionNames}
              className="mt-0.5 text-[15px] leading-[1.55] text-ink"
            />
          )}
          {message.inputRequest ? (
            <InputRequestProgress request={message.inputRequest} />
          ) : null}
          {attachedTurn ? (
            <ChatTurnToolChip
              turn={attachedTurn}
              selected={selectedToolTurnKey === attachedTurn.turnKey}
              onInspect={(turnKey) => onInspectToolTurn?.(turnKey)}
            />
          ) : null}
        </div>
      </article>
    );
  }

  const leadingStartIso = trace.leadingUnattachedTurns[0]?.startedAt;
  const leadingDayKey = leadingStartIso ? localDayKey(leadingStartIso) : '';

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      {historyError || history.isError ? (
        <div className="px-5 pt-3">
          <Alert tone="warning">{t('loadFailed')}</Alert>
        </div>
      ) : null}
      <div
        ref={logRef}
        onScroll={onScroll}
        onLoadCapture={onMediaLoad}
        role="log"
        aria-label={t('messages')}
        className="flex min-h-[280px] flex-1 flex-col gap-3 overflow-y-auto py-4"
      >
        {hasMore ? (
          <div className="mx-5 flex justify-center">
            <Button
              variant="secondary"
              disabled={loadingOlder}
              onClick={() => void loadOlder()}
            >
              {loadingOlder ? tChat('loadingOlder') : tChat('older')}
            </Button>
          </div>
        ) : null}
        {leadingStartIso ? (
          <ChatDayDivider dateIso={leadingStartIso} className="mx-5" />
        ) : null}
        {renderUnattachedToolTurns(trace.leadingUnattachedTurns)}
        {messages.map((message, index) => (
          <Fragment key={message.id}>
            {isDayBoundary(messages, index, leadingDayKey) ? (
              <ChatDayDivider dateIso={message.createdAt} className="mx-5" />
            ) : null}
            <ChatTranscriptRow
              message={message}
              workspaceId={workspaceId}
              role={role}
              cardClassName="mx-5"
              secretControl={secretControl}
              memoryBotId={message.senderBotId}
              canManageMemory={canManageMemoryBot}
              refreshHistory={refreshHistory}
              onInspectTurn={onInspectToolTurn}
              toolTurnsAfter={renderUnattachedToolTurns(
                trace.unattachedTurnsByAnchor.get(message.id) ?? [],
              )}
              renderMessage={() => renderConversationMessage(message)}
            />
          </Fragment>
        ))}
      </div>
      {failedTurns.length > 0 ? (
        <div className="max-h-40 shrink-0 space-y-2 overflow-y-auto px-5 pb-2">
          {failedTurns.map((turn) => (
            <Alert key={turn.id} tone="warning">
              <p className="font-bold">
                {tChat('botTurnFailed', {
                  name:
                    group.members.find((member) => member.id === turn.botId)
                      ?.name || t('bot'),
                })}
              </p>
              <p>{tChat(turnFailureKey(turn.errorCode))}</p>
            </Alert>
          ))}
        </div>
      ) : null}
      <div
        className="max-h-40 shrink-0 space-y-2 overflow-y-auto px-5 pb-2"
        aria-live="polite"
      >
        {activeTurns.map((turn) => {
          const member = group.members.find((row) => row.id === turn.botId);
          const name = member?.name || t('bot');
          const toolTurn = trace.turns.find((row) => row.turnKey === turn.id);
          return (
            <div key={turn.id} className="flex flex-wrap items-center gap-2">
              <div className="min-w-0 flex-1">
                {turn.status === 'running' && toolTurn?.status === 'running' ? (
                  <LiveToolStatusBar
                    name={name}
                    avatarSeed={turn.botId!}
                    statusText={tChat('working', { name })}
                    liveTurn={toolTurn}
                    onInspect={onInspectToolTurn}
                  />
                ) : (
                  <div className="flex items-center gap-2 text-[13px] font-bold text-blue">
                    <BotAvatar
                      seed={turn.botId!}
                      mood={turn.status === 'running' ? 'thinking' : 'idle'}
                      size={26}
                      decorative
                    />
                    <span>
                      {tChat(
                        turn.status === 'queued'
                          ? 'botQueued'
                          : turn.status === 'waiting_secret'
                            ? 'botWaitingSecret'
                            : 'thinking',
                        { name },
                      )}
                    </span>
                  </div>
                )}
              </div>
              {canPost && turn.status === 'running' ? (
                <StopTurnButton
                  botName={name}
                  path={`${base}/groups/${group.id}/turns/${turn.id}/stop`}
                  requested={turn.stopRequested}
                  refresh={history.refetch}
                />
              ) : null}
            </div>
          );
        })}
        {activeToolTurns.map((turn) => {
          const name =
            group.members.find((row) => row.id === turn.botId)?.name ||
            t('bot');
          return (
            <LiveToolStatusBar
              key={turn.turnKey}
              name={name}
              avatarSeed={turn.botId || turn.turnKey}
              statusText={tChat('working', { name })}
              liveTurn={turn}
              onInspect={onInspectToolTurn}
            />
          );
        })}
      </div>
      <GroupMessageComposer
        canPost={canPost}
        targets={mentionTargets}
        postMessage={(content) =>
          requestJson(`${base}/groups/${group.id}/messages`, {
            method: 'POST',
            body: { id: crypto.randomUUID(), content },
          })
        }
        onSent={async () => {
          scrollToBottom();
          await client.invalidateQueries({
            queryKey: ['group-messages', workspaceId, group.id],
          });
          await client.invalidateQueries({ queryKey: ['groups', workspaceId] });
        }}
      />

      <SecretRequestModal
        workspaceId={workspaceId}
        botName={requestBotName(secretControl.activeRequest?.secretRequestId)}
        role={role}
        summary={secretControl.activeRequest}
        onClose={secretControl.closeModal}
        onOpenTakeover={secretControl.openTakeover}
        onCompleted={async () => {
          await client.invalidateQueries({
            queryKey: ['group-messages', workspaceId, group.id],
          });
        }}
      />

      <BrokerTakeoverModal
        workspaceId={workspaceId}
        botName={requestBotName(secretControl.takeoverRequest?.secretRequestId)}
        role={role}
        summary={secretControl.takeoverRequest}
        onClose={secretControl.closeTakeover}
        onCompleted={async () => {
          await client.invalidateQueries({
            queryKey: ['group-messages', workspaceId, group.id],
          });
        }}
      />
    </div>
  );
}

function TeamStatusUpdate({
  status,
}: {
  status: components['schemas']['TeamBotStatus'];
}) {
  const t = useTranslations('team');
  const locale = useLocale();
  const workingSince = status.workingSince
    ? formatTimeLabel(status.workingSince, locale)
    : null;
  const progressAt = status.progressAt
    ? formatTimeLabel(status.progressAt, locale)
    : null;
  return (
    <div className="mt-0.5 text-[15px] leading-[1.55] text-ink">
      <p>
        {t(`teamStatusStates.${status.state}`, { name: status.name })}
        {status.state === 'working' && workingSince ? (
          <span className="ml-2 text-[12px] text-muted">
            {t('workingSince', { time: workingSince })}
          </span>
        ) : null}
      </p>
      {status.progress ? (
        <p className="whitespace-pre-wrap break-words border-l border-line pl-2 text-[13px] text-muted">
          <span className="font-bold">
            {progressAt
              ? t('lastProgressAt', { time: progressAt })
              : t('lastProgress')}{' '}
          </span>
          {status.progress}
        </p>
      ) : null}
    </div>
  );
}

function InputRequestProgress({
  request,
}: {
  request: NonNullable<ChatMessage['inputRequest']>;
}) {
  const t = useTranslations('team');
  const missing = request.recipients.some(
    (recipient) => recipient.state === 'unavailable',
  );
  const title = {
    waiting: request.required
      ? 'inputRequestWaitingRequired'
      : 'inputRequestWaiting',
    collected:
      request.required && missing
        ? 'inputRequestMissingRequired'
        : 'inputRequestCollected',
    stopped: 'inputRequestStopped',
  } as const;
  const recipientState = {
    waiting: 'inputRecipientWaiting',
    replied: 'inputRecipientReplied',
    unavailable: 'inputRecipientUnavailable',
  } as const;
  return (
    <div
      role="status"
      className="mt-2 rounded-sm border border-line px-3 py-2 text-[13px] text-ink"
    >
      <p className="font-bold">{t(title[request.state])}</p>
      <ul className="mt-1 space-y-0.5">
        {request.recipients.map((recipient) => (
          <li key={recipient.botId}>
            {recipient.name} · {t(recipientState[recipient.state])}
          </li>
        ))}
      </ul>
    </div>
  );
}
