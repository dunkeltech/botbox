'use client';

import { Send } from 'lucide-react';
import Link from 'next/link';
import {
  useEffect,
  useId,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { Alert, BotAvatar, Button } from '@/components/ui';
import { getAccessToken, requestBlob, requestJson } from '@/lib/api-client';
import {
  describeToolMessage,
  type ToolCopy,
} from '@/components/chat/toolMessage';
import { BrokerTakeoverModal } from '@/components/chat/BrokerTakeoverModal';
import { SecretRequestCard } from '@/components/chat/SecretRequestCard';
import { SecretRequestModal } from '@/components/chat/SecretRequestModal';
import { useSecretRequestControl } from '@/components/chat/useSecretRequestControl';
import { useChatScroll } from '@/components/chat/useChatScroll';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type Conversation = components['schemas']['ChatConversation'];
type History = components['schemas']['ChatHistory'];
type BudgetSummary = components['schemas']['BudgetSummary'];

const turnFailureKeys: Record<string, string> = {
  'engine.cli_unavailable': 'cliUnavailable',
  'engine.computer_not_running': 'computerNotRunning',
  'engine.computer_restart_required': 'computerRestartRequired',
  'engine.computer_controlled': 'computerControlled',
  'engine.proxy_unavailable': 'proxyUnavailable',
  'engine.credentials_missing': 'credentialsMissing',
  'engine.credentials_unavailable': 'credentialsUnavailable',
  'engine.provider_failed': 'providerFailed',
};

function formatTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function ToolImage({
  path,
  alt,
  className = 'mt-2 max-h-64 rounded-sm border border-line',
}: {
  path: string;
  alt: string;
  className?: string;
}) {
  const [src, setSrc] = useState<string | null>(null);
  useEffect(() => {
    let url: string | null = null;
    let active = true;
    void requestBlob(path)
      .then((blob) => {
        if (!active) return;
        url = URL.createObjectURL(blob);
        setSrc(url);
      })
      .catch(() => {
        if (active) setSrc(null);
      });
    return () => {
      active = false;
      if (url) URL.revokeObjectURL(url);
    };
  }, [path]);
  if (!src) return null;
  // Tool screenshots come from an authenticated fetch into an object URL.
  // eslint-disable-next-line @next/next/no-img-element
  return <img src={src} alt={alt} className={className} />;
}

export function ToolMessageView({
  workspaceId,
  conversationId,
  messageId,
  kind,
  content,
  className = '',
}: {
  workspaceId: string;
  conversationId: string;
  messageId: string;
  kind: string;
  content: string;
  className?: string;
}) {
  const t = useTranslations('chat');
  const copy = describeToolMessage(kind, content);
  if (!copy) return null;
  return (
    <div
      className={`mt-1.5 rounded-sm border border-line bg-paper-2 px-3 py-2 text-[13px] text-ink-2 ${className}`.trim()}
    >
      <ToolLine
        copy={copy}
        imagePath={`/api/workspaces/${workspaceId}/conversations/${conversationId}/messages/${messageId}/tool-image`}
        alt={t('toolImageAlt')}
      />
    </div>
  );
}

function ToolLine({
  copy,
  imagePath,
  alt,
}: {
  copy: ToolCopy;
  imagePath: string;
  alt: string;
}) {
  const t = useTranslations('chat');
  const text = copy.values ? t(copy.key, copy.values) : t(copy.key);
  return (
    <div>
      <p className="font-mono text-[12px] text-ink-2">{text}</p>
      {copy.image ? <ToolImage path={imagePath} alt={alt} /> : null}
    </div>
  );
}

/**
 * Private direct-message chat panel shared by Bot Workspace (`session` bubbles)
 * and Team Chat DMs (`slack` message rows).
 */
export function ChatPanel({
  workspaceId,
  userId,
  botId,
  name,
  avatarSeed,
  canChat = true,
  role,
  transcriptQuery = '',
  variant = 'session',
  hideHeader = false,
}: {
  workspaceId: string;
  userId: string;
  botId: string;
  name: string;
  avatarSeed: string;
  canChat?: boolean;
  role?: WorkspaceRole;
  /** Client-side filter for the team search field. Empty shows the whole history. */
  transcriptQuery?: string;
  /** Visual layout: 'session' uses right-aligned dark user bubbles; 'slack' uses Slack-style rows. */
  variant?: 'session' | 'slack';
  /** Hide the panel heading when wrapped inside a parent header (e.g. Session card or DM header). */
  hideHeader?: boolean;
}) {
  const t = useTranslations('chat');
  const tTeam = useTranslations('team');
  const client = useQueryClient();
  const inputId = useId();
  const [content, setContent] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState(false);
  const [live, setLive] = useState('');
  const [older, setOlder] = useState<History['messages']>([]);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const base = `/api/workspaces/${workspaceId}`;
  const effectiveRole: WorkspaceRole = role ?? (canChat ? 'member' : 'viewer');

  const conversation = useQuery({
    queryKey: ['dm', workspaceId, userId, botId],
    enabled: canChat,
    queryFn: () =>
      requestJson<Conversation>(`${base}/bots/${botId}/dm`, { method: 'POST' }),
  });
  const conversationId = conversation.data?.id;
  const history = useQuery({
    queryKey: ['chat', workspaceId, userId, conversationId],
    enabled: !!conversationId,
    queryFn: () =>
      requestJson<History>(`${base}/conversations/${conversationId}/messages`),
    refetchInterval: 3000,
  });
  const budget = useQuery({
    queryKey: ['budget', workspaceId, botId],
    queryFn: () => requestJson<BudgetSummary>(`${base}/budgets?botId=${botId}`),
    refetchInterval: 10000,
  });

  useEffect(() => {
    if (!conversationId) return;
    const token = getAccessToken();
    if (!token) return;
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const socket = new WebSocket(
      `${protocol}//${location.host}/ws/chat/${workspaceId}/${conversationId}`,
      ['botbox.events', `botbox.auth.${token}`],
    );
    let deltasSinceMessage = false;
    socket.onmessage = (message: MessageEvent<string>) => {
      try {
        const data: unknown = JSON.parse(message.data);
        if (typeof data !== 'object' || data === null) return;
        if (
          'Kind' in data &&
          (data.Kind === 'text_delta' || data.Kind === 'message') &&
          'Text' in data &&
          typeof data.Text === 'string'
        ) {
          const text = data.Text;
          if (data.Kind === 'text_delta' || !deltasSinceMessage) {
            setLive((previous) => (previous + text).slice(-1048576));
          }
          deltasSinceMessage = data.Kind === 'text_delta';
        }
        if ('Kind' in data && data.Kind === 'status') {
          if (
            'Status' in data &&
            (data.Status === 'idle' || data.Status === 'offline')
          ) {
            setLive('');
            deltasSinceMessage = false;
          }
          void client.invalidateQueries({
            queryKey: ['chat', workspaceId, userId, conversationId],
          });
        }
      } catch {
        setError(true);
      }
    };
    return () => socket.close();
  }, [conversationId, workspaceId, userId, client]);

  async function submitMessage() {
    if (!conversationId || !content.trim() || sending) return;
    setSending(true);
    setError(false);
    try {
      await requestJson(`${base}/conversations/${conversationId}/messages`, {
        method: 'POST',
        body: { id: crypto.randomUUID(), content },
      });
      setContent('');
      scrollToBottom();
      await client.invalidateQueries({
        queryKey: ['chat', workspaceId, userId, conversationId],
      });
    } catch {
      setError(true);
    } finally {
      setSending(false);
    }
  }

  async function send(event: FormEvent) {
    event.preventDefault();
    await submitMessage();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void submitMessage();
    }
  }

  async function loadOlder() {
    const first = older[0] ?? history.data?.messages[0];
    if (!first || !conversationId) return;
    setLoadingOlder(true);
    try {
      const page = await requestJson<History>(
        `${base}/conversations/${conversationId}/messages?before=${first.position}`,
      );
      if (page.messages.length > 0) {
        preparePrepend();
      }
      setOlder((previous) => [...page.messages, ...previous]);
    } catch {
      setError(true);
    } finally {
      setLoadingOlder(false);
    }
  }

  const turn = history.data?.turns[0];
  const active = turn?.status === 'queued' || turn?.status === 'running';
  const waitingSecret = turn?.status === 'waiting_secret';
  const secretControl = useSecretRequestControl(workspaceId, async () => {
    await client.invalidateQueries({
      queryKey: ['chat', workspaceId, userId, conversationId],
    });
  });
  const messages = [...older, ...(history.data?.messages ?? [])]
    .filter(
      (message, index, all) =>
        all.findIndex((m) => m.id === message.id) === index,
    )
    .filter((message) => {
      const needle = transcriptQuery.trim().toLowerCase();
      return needle === '' || message.content.toLowerCase().includes(needle);
    });

  const isSlack = variant === 'slack';
  const { logRef, onScroll, onMediaLoad, scrollToBottom, preparePrepend } =
    useChatScroll({
      conversationKey: conversationId ?? '',
      messageCount: messages.length,
      lastMessageId: messages[messages.length - 1]?.id ?? '',
      liveText: live,
    });

  return (
    <section
      className="flex min-h-0 min-w-0 flex-1 flex-col gap-3"
      aria-label={t('title', { name })}
    >
      {!hideHeader ? (
        <div className="flex items-center gap-3">
          <BotAvatar
            seed={avatarSeed}
            mood={active ? 'thinking' : 'idle'}
            size={32}
          />
          <h2 className="text-[20px] font-black text-ink">
            {t('title', { name })}
          </h2>
        </div>
      ) : null}

      <p className="text-[13px] text-muted">{t('privacy')}</p>

      {!canChat ? <Alert tone="warning">{t('viewer')}</Alert> : null}
      {error || conversation.isError || history.isError ? (
        <Alert tone="warning">{t('error')}</Alert>
      ) : null}
      {budget.data?.budgets.some((b) => b.warning) ? (
        <Alert tone="warning">
          {budget.data.budgets.some((b) => b.exceeded)
            ? t('budgetExceeded')
            : t('budgetWarning')}
        </Alert>
      ) : null}

      {history.data?.messages.length === 50 ? (
        <Button
          variant="secondary"
          disabled={loadingOlder}
          onClick={() => void loadOlder()}
        >
          {t('older')}
        </Button>
      ) : null}

      <div
        ref={logRef}
        onScroll={onScroll}
        onLoadCapture={onMediaLoad}
        className={`relative flex min-h-[220px] flex-1 flex-col gap-3 overflow-y-auto pr-1 ${
          isSlack ? '' : 'max-h-[480px]'
        }`}
        role="log"
        aria-label={t('history')}
      >
        {isSlack && messages.length > 0 ? (
          <div className="my-1 flex items-center gap-3">
            <span className="h-px flex-1 bg-line" />
            <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
              {tTeam('todayDivider')}
            </span>
            <span className="h-px flex-1 bg-line" />
          </div>
        ) : null}

        {messages.map((message) => {
          if (message.kind === 'secret_request' && message.secretRequest) {
            const sr = message.secretRequest;
            return (
              <SecretRequestCard
                key={message.id}
                summary={sr}
                role={effectiveRole}
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
            );
          }
          const tool = describeToolMessage(message.kind, message.content);
          const isUser = message.senderType === 'user';
          const timeLabel = formatTime(message.createdAt);

          if (!isSlack && isUser && !tool) {
            return (
              // Keep sr-only's position: absolute contained within the scrollable log
              // so off-viewport messages cannot inflate documentElement.scrollHeight.
              <article
                key={message.id}
                className="relative flex flex-col items-end"
              >
                <span className="sr-only">{t('you')}</span>
                <div className="max-w-[88%] rounded-[14px_14px_4px_14px] bg-dark px-3.5 py-2.5 text-[14px] leading-[1.5] text-paper">
                  <p className="whitespace-pre-wrap break-words">
                    {message.content}
                  </p>
                </div>
                {timeLabel ? (
                  <span className="mt-1 text-[11px] text-muted">
                    {timeLabel}
                  </span>
                ) : null}
              </article>
            );
          }

          return (
            <article key={message.id} className="flex items-start gap-2.5">
              {isUser ? (
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-dark text-[12px] font-black text-paper">
                  {t('you').slice(0, 2).toUpperCase()}
                </span>
              ) : (
                <BotAvatar
                  seed={avatarSeed}
                  mood={active ? 'thinking' : 'idle'}
                  size={isSlack ? 36 : 28}
                  decorative
                />
              )}
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="text-[14px] font-black text-ink">
                    {isUser ? t('you') : name}
                  </span>
                  {isSlack && !isUser ? (
                    <span className="rounded-xs bg-mint-soft px-1.5 py-0.5 text-[11px] font-black uppercase tracking-[0.06em] text-green-600">
                      {tTeam('botBadge')}
                    </span>
                  ) : null}
                  {timeLabel ? (
                    <span className="text-[12px] text-muted">{timeLabel}</span>
                  ) : null}
                </div>
                {tool && conversationId ? (
                  <div className="mt-1 rounded-sm border border-line bg-paper-2 px-3 py-2">
                    <ToolLine
                      copy={tool}
                      imagePath={`${base}/conversations/${conversationId}/messages/${message.id}/tool-image`}
                      alt={t('toolImageAlt')}
                    />
                  </div>
                ) : (
                  <p className="mt-0.5 whitespace-pre-wrap break-words text-[15px] leading-[1.55] text-ink">
                    {message.content || t('toolCall')}
                  </p>
                )}
              </div>
            </article>
          );
        })}

        {live ? (
          <div
            className="flex items-start gap-2.5 rounded-sm border border-blue-line bg-blue-tint/40 p-2.5"
            aria-live="polite"
          >
            <BotAvatar seed={avatarSeed} mood="thinking" size={26} decorative />
            <p className="whitespace-pre-wrap break-words text-[14px] text-ink">
              {live}
            </p>
          </div>
        ) : null}
      </div>

      {active ? (
        <div
          role="status"
          className="flex items-center gap-2 rounded-sm border border-blue-line bg-blue-tint px-3 py-2 text-[13px] font-bold text-blue"
        >
          <BotAvatar seed={avatarSeed} mood="thinking" size={22} decorative />
          <span>
            {turn.status === 'queued'
              ? t('queued', { count: turn.queueAhead })
              : t('working', { name })}
          </span>
        </div>
      ) : waitingSecret ? (
        <div
          role="status"
          className="flex items-center gap-2 rounded-sm border border-amber-line bg-amber-tint px-3 py-2 text-[13px] font-bold text-amber"
        >
          <BotAvatar seed={avatarSeed} mood="idle" size={22} decorative />
          <span>{t('waitingSecret')}</span>
        </div>
      ) : null}

      {turn?.errorCode ? (
        <Alert tone="warning">
          {t(
            Object.hasOwn(turnFailureKeys, turn.errorCode)
              ? (turnFailureKeys[turn.errorCode] ?? 'turnFailed')
              : 'turnFailed',
          )}
          {turn.errorCode === 'engine.credentials_missing' ||
          turn.errorCode === 'engine.credentials_unavailable' ? (
            <Link
              className="mt-2 inline-flex min-h-[44px] items-center font-bold underline"
              href={`/bots/${botId}/secrets`}
            >
              {t('openCredentials')}
            </Link>
          ) : [
              'engine.computer_not_running',
              'engine.computer_restart_required',
              'engine.proxy_unavailable',
            ].includes(turn.errorCode) ? (
            <Link
              className="mt-2 inline-flex min-h-[44px] items-center font-bold underline"
              href={`/bots/${botId}#computer-controls`}
            >
              {t('openComputer')}
            </Link>
          ) : null}
        </Alert>
      ) : null}

      <form
        onSubmit={(event) => void send(event)}
        className="overflow-hidden rounded-md border-[1.5px] border-line-strong bg-paper focus-within:border-green-600"
      >
        <label htmlFor={inputId} className="sr-only">
          {t('message')}
        </label>
        <textarea
          id={inputId}
          rows={2}
          maxLength={8192}
          value={content}
          disabled={!canChat || sending}
          placeholder={t('messagePlaceholder', { name })}
          onChange={(event) => setContent(event.target.value)}
          onKeyDown={handleKeyDown}
          className="block w-full resize-none border-0 bg-transparent px-3.5 pt-3 text-[15px] text-ink outline-none placeholder:text-muted disabled:cursor-not-allowed disabled:bg-paper-2"
        />
        <div className="flex items-center justify-between gap-2 px-3 pb-2 pt-1">
          <span className="text-[12px] text-muted">
            {isSlack ? tTeam('dmPrivacyHint', { name }) : ''}
          </span>
          <Button
            type="submit"
            aria-label={sending ? t('sending') : t('send')}
            disabled={!canChat || sending || !conversationId || !content.trim()}
            className="min-h-[40px] px-3.5"
          >
            <Send className="h-4 w-4" aria-hidden="true" />
            <span className="sr-only">
              {sending ? t('sending') : t('send')}
            </span>
          </Button>
        </div>
      </form>

      <SecretRequestModal
        workspaceId={workspaceId}
        botName={name}
        role={effectiveRole}
        summary={secretControl.activeRequest}
        onClose={secretControl.closeModal}
        onOpenTakeover={secretControl.openTakeover}
        onCompleted={async () => {
          await client.invalidateQueries({
            queryKey: ['chat', workspaceId, userId, conversationId],
          });
        }}
      />

      <BrokerTakeoverModal
        workspaceId={workspaceId}
        botName={name}
        role={effectiveRole}
        summary={secretControl.takeoverRequest}
        onClose={secretControl.closeTakeover}
        onCompleted={async () => {
          await client.invalidateQueries({
            queryKey: ['chat', workspaceId, userId, conversationId],
          });
        }}
      />
    </section>
  );
}
