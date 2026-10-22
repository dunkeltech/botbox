'use client';

import Link from 'next/link';
import { useEffect, useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { Alert, BotAvatar, Button, Input } from '@/components/ui';
import { getAccessToken, requestBlob, requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';
import {
  describeToolMessage,
  type ToolCopy,
} from '@/components/chat/toolMessage';

type Conversation = components['schemas']['ChatConversation'];
type History = components['schemas']['ChatHistory'];
type BudgetSummary = components['schemas']['BudgetSummary'];

// Only known codes select copy; dependency error text never becomes UI content.
const turnFailureKeys: Record<string, string> = {
  'engine.cli_unavailable': 'cliUnavailable',
  'engine.computer_not_running': 'computerNotRunning',
  'engine.computer_restart_required': 'computerRestartRequired',
  'engine.control_held': 'computerControlled',
  'engine.proxy_unavailable': 'proxyUnavailable',
  'engine.credentials_missing': 'credentialsMissing',
  'engine.credentials_unavailable': 'credentialsUnavailable',
  'engine.provider_failed': 'providerFailed',
  budget_exceeded: 'budgetExceeded',
};

function ToolImage({ path, alt }: { path: string; alt: string }) {
  const [src, setSrc] = useState<string | null>(null);
  useEffect(() => {
    let url: string | null = null;
    let cancelled = false;
    requestBlob(path)
      .then((blob) => {
        if (cancelled || !blob.type.startsWith('image/')) return;
        const next = URL.createObjectURL(blob);
        // The effect may have cleaned up while the blob was in flight.
        if (cancelled) {
          URL.revokeObjectURL(next);
          return;
        }
        url = next;
        setSrc(next);
      })
      .catch(() => {
        // The sentence stays. A missing file does not replace it with an error.
      });
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [path]);
  if (!src) return null;
  return (
    // The blob URL is minted in this session from an authenticated fetch.
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={src}
      alt={alt}
      className="max-h-80 w-full rounded-sm border border-line object-contain"
    />
  );
}

export function ToolMessageView({
  workspaceId,
  conversationId,
  messageId,
  kind,
  content,
}: {
  workspaceId: string;
  conversationId: string;
  messageId: string;
  kind: string;
  content: string;
}) {
  const t = useTranslations('chat');
  const copy = describeToolMessage(kind, content);
  if (!copy) {
    return (
      <p className="whitespace-pre-wrap break-words text-[15px]">{content}</p>
    );
  }
  return (
    <ToolLine
      copy={copy}
      imagePath={`/api/workspaces/${workspaceId}/conversations/${conversationId}/messages/${messageId}/tool-image`}
      alt={t('toolImageAlt')}
    />
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
  return (
    <div className="flex flex-col gap-2">
      <p className="break-words text-[14px]">
        {copy.values ? t(copy.key, copy.values) : t(copy.key)}
      </p>
      {copy.image ? <ToolImage path={imagePath} alt={alt} /> : null}
    </div>
  );
}

/** Displays the caller's private DM; the bot computer remains a shared workspace resource. */
export function ChatPanel({
  workspaceId,
  userId,
  botId,
  name,
  avatarSeed,
  canChat = true,
  transcriptQuery = '',
}: {
  workspaceId: string;
  userId: string;
  botId: string;
  name: string;
  avatarSeed: string;
  canChat?: boolean;
  /** Client-side filter for the team search field. Empty shows the whole history. */
  transcriptQuery?: string;
}) {
  const t = useTranslations('chat');
  const client = useQueryClient();
  const [content, setContent] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState(false);
  const [live, setLive] = useState('');
  const [older, setOlder] = useState<History['messages']>([]);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const base = `/api/workspaces/${workspaceId}`;
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
  async function send(event: FormEvent) {
    event.preventDefault();
    if (!conversationId || !content.trim() || sending) return;
    setSending(true);
    setError(false);
    try {
      await requestJson(`${base}/conversations/${conversationId}/messages`, {
        method: 'POST',
        body: { id: crypto.randomUUID(), content },
      });
      setContent('');
      await client.invalidateQueries({
        queryKey: ['chat', workspaceId, userId, conversationId],
      });
    } catch {
      setError(true);
    } finally {
      setSending(false);
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
      setOlder((previous) => [...page.messages, ...previous]);
    } catch {
      setError(true);
    } finally {
      setLoadingOlder(false);
    }
  }
  const turn = history.data?.turns[0];
  const active = turn?.status === 'queued' || turn?.status === 'running';
  return (
    <section
      className="flex min-w-0 flex-col gap-4"
      aria-label={t('title', { name })}
    >
      <div className="flex items-center gap-3">
        <BotAvatar
          seed={avatarSeed}
          mood={active ? 'thinking' : 'idle'}
          size={32}
        />
        <h2 className="text-[20px] font-black">{t('title', { name })}</h2>
      </div>
      <p className="text-[13px] text-ink-2">{t('privacy')}</p>
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
        className="max-h-[520px] space-y-3 overflow-y-auto"
        role="log"
        aria-label={t('history')}
      >
        {[...older, ...(history.data?.messages ?? [])]
          .filter(
            (message, index, all) =>
              all.findIndex((m) => m.id === message.id) === index,
          )
          .filter((message) => {
            const needle = transcriptQuery.trim().toLowerCase();
            return (
              needle === '' || message.content.toLowerCase().includes(needle)
            );
          })
          .map((message) => {
            const tool = describeToolMessage(message.kind, message.content);
            return (
              <article
                key={message.id}
                className={`border border-line p-3 ${tool ? 'rounded-sm bg-paper-2' : `rounded-md ${message.senderType === 'user' ? 'bg-paper-2' : 'bg-paper'}`}`}
              >
                <p className="text-[12px] font-bold text-muted">
                  {message.senderType === 'user' ? t('you') : name}
                </p>
                {tool && conversationId ? (
                  <ToolLine
                    copy={tool}
                    imagePath={`${base}/conversations/${conversationId}/messages/${message.id}/tool-image`}
                    alt={t('toolImageAlt')}
                  />
                ) : (
                  <p className="whitespace-pre-wrap break-words text-[14px]">
                    {message.content || t('toolCall')}
                  </p>
                )}
              </article>
            );
          })}
        {live ? (
          <p className="whitespace-pre-wrap text-[14px]" aria-live="polite">
            {live}
          </p>
        ) : null}
      </div>
      {active ? (
        <p role="status" className="text-[13px] text-blue">
          {turn.status === 'queued'
            ? t('queued', { count: turn.queueAhead })
            : t('working', { name })}
        </p>
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
        className="flex flex-col gap-3"
      >
        <Input
          label={t('message')}
          value={content}
          maxLength={8192}
          disabled={!canChat || sending}
          onChange={(event) => setContent(event.target.value)}
        />
        <Button
          type="submit"
          disabled={!canChat || sending || !conversationId || !content.trim()}
        >
          {sending ? t('sending') : t('send')}
        </Button>
      </form>
    </section>
  );
}
