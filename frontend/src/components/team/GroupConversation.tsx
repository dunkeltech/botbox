'use client';

import { ArrowLeftRight } from 'lucide-react';
import { useEffect, useMemo } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
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
import { SecretRequestCard } from '@/components/chat/SecretRequestCard';
import { SecretRequestModal } from '@/components/chat/SecretRequestModal';
import { useSecretRequestControl } from '@/components/chat/useSecretRequestControl';
import { useChatScroll } from '@/components/chat/useChatScroll';
import { turnFailureKey } from '@/components/chat/turnFailure';
import { activeBotTurns, failedBotTurns } from '@/components/team/activity';
import { ApprovalCard } from '@/components/team/ApprovalCard';
import { GroupMessageComposer } from '@/components/team/GroupMessageComposer';
import {
  approvalForMessage,
  canDecideApproval,
} from '@/components/team/approvals';
import {
  useApprovalDecision,
  useApprovalList,
} from '@/components/team/useApprovals';
import { Alert, BotAvatar } from '@/components/ui';
import { connectConversation } from '@/lib/conversation-events';
import { getAccessToken, requestJson } from '@/lib/api-client';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';
import { initials, type MentionTarget } from '@/components/team/text';

type Group = components['schemas']['Group'];
type History = components['schemas']['ChatHistory'];
type ChatMessage = components['schemas']['ChatMessage'];

function formatTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function targets(members: Group['members']): MentionTarget[] {
  const named: MentionTarget[] = members.flatMap((member) => {
    const name = member.name?.trim();
    if (!name) return [];
    return [{ id: member.id, name, kind: member.type }];
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
  const base = `/api/workspaces/${workspaceId}`;
  const history = useQuery({
    queryKey: ['group-messages', workspaceId, group.id],
    queryFn: () => requestJson<History>(`${base}/groups/${group.id}/messages`),
    refetchInterval: 3000,
  });
  const approvalList = useApprovalList(workspaceId);
  const {
    decide,
    busyId,
    error: decisionError,
  } = useApprovalDecision(workspaceId);
  const secretControl = useSecretRequestControl(workspaceId, async () => {
    await client.invalidateQueries({
      queryKey: ['group-messages', workspaceId, group.id],
    });
  });
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
  const trace = useMemo(
    () =>
      buildToolTrace({
        messages: history.data?.messages ?? [],
        turns: history.data?.turns,
        imagePathForMessage: (messageId) =>
          `${base}/groups/${group.id}/messages/${messageId}/tool-image`,
      }),
    [history.data?.messages, history.data?.turns, base, group.id],
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
  const { logRef, onScroll, onMediaLoad, scrollToBottom } = useChatScroll({
    conversationKey: group.id,
    messageCount: messages.length,
    lastMessageId: messages[messages.length - 1]?.id ?? '',
  });

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

  function renderUnattachedToolTurns(turnsToShow: ToolTraceTurn[]) {
    if (turnsToShow.length === 0) return null;
    return turnsToShow.map((toolTurn) => {
      const member = group.members.find((row) => row.id === toolTurn.botId);
      const botName = member?.name || t('bot');
      return (
        <div
          key={`unattached-${toolTurn.turnKey}`}
          className="flex gap-3 px-5 py-1"
        >
          <BotAvatar
            seed={member?.id ?? toolTurn.botId ?? group.leadBotId}
            size={36}
            decorative
          />
          <div className="min-w-0 flex-1">
            <p className="flex flex-wrap items-baseline gap-2">
              <span className="text-[15px] font-black text-ink">{botName}</span>
              <span className="rounded-xs bg-mint-soft px-1.5 py-0.5 text-[11px] font-black uppercase tracking-[0.06em] text-green-600">
                {t('botBadge')}
              </span>
            </p>
            <ChatTurnToolChip
              turn={toolTurn}
              selected={selectedToolTurnKey === toolTurn.turnKey}
              onInspect={(turnKey) => onInspectToolTurn?.(turnKey)}
            />
          </div>
        </div>
      );
    });
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div
        ref={logRef}
        onScroll={onScroll}
        onLoadCapture={onMediaLoad}
        role="log"
        aria-label={t('messages')}
        className="flex min-h-[280px] flex-1 flex-col gap-3 overflow-y-auto py-4"
      >
        {messages.length > 0 ? (
          <div className="mx-5 my-1 flex items-center gap-3">
            <span className="h-px flex-1 bg-line" />
            <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('todayDivider')}
            </span>
            <span className="h-px flex-1 bg-line" />
          </div>
        ) : null}
        {renderUnattachedToolTurns(trace.leadingUnattachedTurns)}
        {messages.map((message) => {
          const unattachedAfter =
            trace.unattachedTurnsByAnchor.get(message.id) ?? [];
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
            const bot = group.members.find(
              (member) => member.id === matched?.botId,
            );
            return (
              <div key={message.id} className="flex flex-col gap-3">
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
                {renderUnattachedToolTurns(unattachedAfter)}
              </div>
            );
          }
          if (message.kind === 'secret_request' && message.secretRequest) {
            const sr = message.secretRequest;
            return (
              <div key={message.id} className="flex flex-col gap-3">
                <SecretRequestCard
                  className="mx-5"
                  summary={sr}
                  role={role}
                  busy={secretControl.busyRequestId === sr.secretRequestId}
                  errorKey={
                    secretControl.requestError?.id === sr.secretRequestId
                      ? secretControl.requestError.key
                      : null
                  }
                  onProvide={secretControl.openModal}
                  onTakeover={secretControl.openTakeover}
                  onDeny={(s) => void secretControl.deny(s)}
                />
                {renderUnattachedToolTurns(unattachedAfter)}
              </div>
            );
          }
          if (
            message.kind === 'system' ||
            message.kind === 'card' ||
            message.senderType === 'system'
          ) {
            return (
              <div key={message.id} className="flex flex-col gap-3">
                <div className="mx-5 flex items-center gap-2 rounded-sm border border-mint-line bg-mint-soft px-3.5 py-2 text-[13px] text-dark">
                  <ArrowLeftRight
                    className="h-4 w-4 shrink-0 text-green-600"
                    aria-hidden="true"
                  />
                  <span>{message.content}</span>
                </div>
                {renderUnattachedToolTurns(unattachedAfter)}
              </div>
            );
          }
          const botMember = group.members.find(
            (member) => member.id === message.senderBotId,
          );
          const timeLabel = formatTime(message.createdAt);
          const attachedTurn = trace.turnsByReplyId.get(message.id);
          return (
            <div key={message.id} className="flex flex-col gap-3">
              <article
                data-message-id={message.id}
                className="flex gap-3 px-5 py-1"
              >
                {who.kind === 'bot' ? (
                  <BotAvatar
                    seed={
                      botMember?.id ?? message.senderBotId ?? group.leadBotId
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
                    <span className="text-[15px] font-black text-ink">
                      {who.name}
                    </span>
                    <span
                      className={`rounded-xs px-1.5 py-0.5 text-[11px] font-black uppercase tracking-[0.06em] ${
                        who.kind === 'bot'
                          ? 'bg-mint-soft text-green-600'
                          : 'bg-blue-tint text-blue'
                      }`}
                    >
                      {who.kind === 'bot' ? t('botBadge') : t('humanBadge')}
                    </span>
                    {timeLabel ? (
                      <span className="text-[12px] text-muted">
                        {timeLabel}
                      </span>
                    ) : null}
                  </p>
                  <MarkdownMessage
                    content={message.content}
                    mentionNames={mentionNames}
                    className="mt-0.5 text-[15px] leading-[1.55] text-ink"
                  />
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
              {renderUnattachedToolTurns(unattachedAfter)}
            </div>
          );
        })}
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
