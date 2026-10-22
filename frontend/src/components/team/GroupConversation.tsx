'use client';

import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { ToolMessageView } from '@/components/chat/ChatPanel';
import { Alert, BotAvatar, Button } from '@/components/ui';
import { getAccessToken, requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';
import {
  activeMention,
  filterByQuery,
  initials,
  insertMention,
  mentionParts,
  type MentionTarget,
} from '@/components/team/text';

type Group = components['schemas']['Group'];
type History = components['schemas']['ChatHistory'];
type ChatMessage = components['schemas']['ChatMessage'];

function targets(group: Group): MentionTarget[] {
  return group.members.flatMap((member) => {
    const name = member.name?.trim();
    if (!name) return [];
    return [{ id: member.id, name, kind: member.type }];
  });
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
  canPost,
  query,
}: {
  workspaceId: string;
  userId: string;
  group: Group;
  canPost: boolean;
  query: string;
}) {
  const t = useTranslations('team');
  const client = useQueryClient();
  const field = useRef<HTMLTextAreaElement>(null);
  const [draft, setDraft] = useState('');
  const [caret, setCaret] = useState(0);
  const [picked, setPicked] = useState(0);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState(false);
  const base = `/api/workspaces/${workspaceId}`;
  const history = useQuery({
    queryKey: ['group-messages', workspaceId, group.id],
    queryFn: () => requestJson<History>(`${base}/groups/${group.id}/messages`),
    refetchInterval: 3000,
  });
  useEffect(() => {
    const token = getAccessToken();
    if (!token) return;
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const socket = new WebSocket(
      `${protocol}//${location.host}/ws/chat/${workspaceId}/${group.id}`,
      ['botbox.events', `botbox.auth.${token}`],
    );
    socket.onmessage = () => {
      void client.invalidateQueries({
        queryKey: ['group-messages', workspaceId, group.id],
      });
      void client.invalidateQueries({
        queryKey: ['notifications', workspaceId],
      });
    };
    return () => socket.close();
  }, [client, group.id, workspaceId]);

  const mention = activeMention(draft, caret);
  const choices = mention ? filterByQuery(targets(group), mention.query) : [];
  const selected = choices[Math.min(picked, Math.max(choices.length - 1, 0))];
  const needle = query.trim().toLowerCase();
  const messages = (history.data?.messages ?? []).filter(
    (message) =>
      needle === '' || message.content.toLowerCase().includes(needle),
  );
  const names = targets(group).map((target) => target.name);

  function rememberCaret(value: string, nextCaret: number) {
    setDraft(value);
    setCaret(nextCaret);
    setPicked(0);
  }

  function apply(name: string) {
    if (!mention) return;
    const next = insertMention(draft, mention.start, caret, name);
    rememberCaret(next.value, next.caret);
    requestAnimationFrame(() => {
      field.current?.focus();
      field.current?.setSelectionRange(next.caret, next.caret);
    });
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (!mention || choices.length === 0) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setPicked((index) => (index + 1) % choices.length);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setPicked((index) => (index - 1 + choices.length) % choices.length);
    } else if (event.key === 'Escape') {
      // Move the caret onto the @ so the query stops without deleting the draft.
      event.preventDefault();
      const pos = mention.start;
      setCaret(pos);
      requestAnimationFrame(() => {
        field.current?.setSelectionRange(pos, pos);
      });
    } else if ((event.key === 'Enter' || event.key === 'Tab') && selected) {
      event.preventDefault();
      apply(selected.name);
    }
  }

  async function send(event: FormEvent) {
    event.preventDefault();
    if (!canPost || !draft.trim() || sending) return;
    setSending(true);
    setError(false);
    try {
      await requestJson(`${base}/groups/${group.id}/messages`, {
        method: 'POST',
        body: { id: crypto.randomUUID(), content: draft },
      });
      setDraft('');
      setCaret(0);
      await client.invalidateQueries({
        queryKey: ['group-messages', workspaceId, group.id],
      });
      await client.invalidateQueries({ queryKey: ['groups', workspaceId] });
    } catch {
      setError(true);
    } finally {
      setSending(false);
    }
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div
        role="log"
        aria-label={t('messages')}
        className="flex min-h-[280px] flex-1 flex-col gap-2 overflow-y-auto py-3"
      >
        {messages.map((message) => {
          const who = speaker(
            message,
            group,
            userId,
            t('you'),
            t('bot'),
            t('human'),
            t('system'),
          );
          if (
            message.kind === 'system' ||
            message.kind === 'approval' ||
            message.kind === 'card' ||
            message.senderType === 'system'
          ) {
            return (
              <p
                key={message.id}
                className="mx-5 rounded-sm border border-mint-line bg-mint-soft px-3 py-2 text-[13px] text-dark"
              >
                {message.content}
              </p>
            );
          }
          const botMember = group.members.find(
            (member) => member.id === message.senderBotId,
          );
          return (
            <article key={message.id} className="flex gap-3 px-5 py-1">
              {who.kind === 'bot' ? (
                <BotAvatar
                  seed={botMember?.id ?? message.senderBotId ?? group.leadBotId}
                  size={36}
                  decorative
                />
              ) : (
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-hex-3 text-[13px] font-black text-paper">
                  {initials(who.name)}
                </span>
              )}
              <div className="min-w-0 flex-1">
                <p className="flex flex-wrap items-baseline gap-2">
                  <span className="font-black">{who.name}</span>
                  <span className="rounded-xs bg-mint-soft px-1.5 text-[11px] font-black uppercase tracking-wide text-green-600">
                    {who.kind === 'bot' ? t('botBadge') : t('humanBadge')}
                  </span>
                </p>
                {message.kind === 'tool' ? (
                  <ToolMessageView
                    workspaceId={workspaceId}
                    conversationId={group.id}
                    messageId={message.id}
                    kind={message.kind}
                    content={message.content}
                  />
                ) : (
                  <p className="whitespace-pre-wrap break-words text-[15px] leading-[1.55]">
                    {mentionParts(message.content, names).map((part, index) =>
                      part.mention ? (
                        <span
                          key={`${message.id}-${index}`}
                          className="rounded-xs bg-blue-tint px-0.5 font-bold text-blue"
                        >
                          {part.text}
                        </span>
                      ) : (
                        <span key={`${message.id}-${index}`}>{part.text}</span>
                      ),
                    )}
                  </p>
                )}
              </div>
            </article>
          );
        })}
      </div>
      {canPost ? (
        <form onSubmit={(event) => void send(event)} className="px-5 pb-4">
          {mention && choices.length > 0 ? (
            <div
              id="group-mentions"
              role="listbox"
              aria-label={t('mentionList')}
              className="mb-2 overflow-hidden rounded-md border-[1.5px] border-mint-line bg-paper shadow-card"
            >
              <p className="bg-paper-2 px-3 py-1.5 text-[11px] font-bold uppercase tracking-wide text-muted">
                {t('mentionHint')}
              </p>
              {choices.map((choice, index) => (
                <button
                  key={choice.id}
                  type="button"
                  role="option"
                  aria-selected={choice.id === selected?.id}
                  className={`flex min-h-[44px] w-full items-center gap-2 border-t border-line px-3 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${index === picked ? 'bg-mint-soft' : 'bg-paper'}`}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => apply(choice.name)}
                >
                  <span className="font-black">@{choice.name}</span>
                  <span className="rounded-xs bg-blue-tint px-1.5 text-[11px] font-black uppercase text-blue">
                    {choice.kind === 'bot' ? t('botBadge') : t('humanBadge')}
                  </span>
                </button>
              ))}
            </div>
          ) : null}
          <div className="overflow-hidden rounded-md border-[1.5px] border-line-strong bg-paper">
            <label htmlFor="group-composer" className="sr-only">
              {t('composer')}
            </label>
            <textarea
              id="group-composer"
              ref={field}
              rows={2}
              maxLength={8192}
              value={draft}
              placeholder={t('composer')}
              aria-expanded={Boolean(mention && choices.length > 0)}
              aria-controls={
                mention && choices.length > 0 ? 'group-mentions' : undefined
              }
              className="block w-full resize-none border-0 bg-transparent px-3.5 py-3 text-[15px] text-ink outline-none"
              onChange={(event) =>
                rememberCaret(event.target.value, event.target.selectionStart)
              }
              onKeyDown={onKeyDown}
              onSelect={(event) => setCaret(event.currentTarget.selectionStart)}
            />
            <div className="flex items-center gap-1 px-1.5 pb-1.5">
              <button
                type="button"
                aria-label={t('mention')}
                className="grid h-11 w-11 place-items-center rounded-sm text-[18px] font-black text-ink-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                onClick={() => {
                  const pos = caret;
                  const next = `${draft.slice(0, pos)}@${draft.slice(pos)}`;
                  rememberCaret(next, pos + 1);
                  requestAnimationFrame(() => {
                    field.current?.focus();
                    field.current?.setSelectionRange(pos + 1, pos + 1);
                  });
                }}
              >
                @
              </button>
              <span className="flex-1" />
              <Button type="submit" disabled={sending || !draft.trim()}>
                {sending ? t('sending') : t('send')}
              </Button>
            </div>
          </div>
          {error ? (
            <div className="mt-2">
              <Alert tone="warning">{t('sendFailed')}</Alert>
            </div>
          ) : null}
        </form>
      ) : (
        <p className="px-5 pb-4 text-[14px] text-ink-2">{t('viewerRead')}</p>
      )}
    </div>
  );
}
