// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { Paperclip, Send } from 'lucide-react';
import { useRef, useState, type KeyboardEvent } from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Button } from '@/components/ui';
import {
  activeMention,
  filterByQuery,
  insertMention,
  type MentionTarget,
} from '@/components/team/text';

/** Keeps the group draft and mention picker independent of the transcript. */
export function GroupMessageComposer({
  canPost,
  targets,
  postMessage,
  onSent,
}: {
  canPost: boolean;
  targets: MentionTarget[];
  postMessage: (content: string) => Promise<unknown>;
  onSent: () => Promise<void>;
}) {
  const t = useTranslations('team');
  const field = useRef<HTMLTextAreaElement>(null);
  const [draft, setDraft] = useState('');
  const [caret, setCaret] = useState(0);
  const [picked, setPicked] = useState(0);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState(false);
  const mention = activeMention(draft, caret);
  const choices = mention ? filterByQuery(targets, mention.query) : [];
  const selected = choices[Math.min(picked, Math.max(choices.length - 1, 0))];

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
    if (mention && choices.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setPicked((index) => (index + 1) % choices.length);
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setPicked((index) => (index - 1 + choices.length) % choices.length);
        return;
      }
      if (event.key === 'Escape') {
        // Move the caret onto the @ so the query stops without deleting the draft.
        event.preventDefault();
        const pos = mention.start;
        setCaret(pos);
        requestAnimationFrame(() => {
          field.current?.setSelectionRange(pos, pos);
        });
        return;
      }
      if ((event.key === 'Enter' || event.key === 'Tab') && selected) {
        event.preventDefault();
        apply(selected.name);
        return;
      }
    }
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void submitDraft();
    }
  }

  async function submitDraft() {
    if (!canPost || !draft.trim() || sending) return;
    setSending(true);
    setError(false);
    try {
      await postMessage(draft);
      setDraft('');
      setCaret(0);
      await onSent();
    } catch {
      setError(true);
    } finally {
      setSending(false);
    }
  }

  return canPost ? (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        void submitDraft();
      }}
      className="px-5 pb-4 pt-1"
    >
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
              <span className="shrink-0 font-black">@{choice.name}</span>
              <span className="shrink-0 rounded-xs bg-blue-tint px-1.5 text-[11px] font-black uppercase text-blue">
                {choice.kind === 'all'
                  ? t('allMembers')
                  : choice.kind === 'bot'
                    ? t('botBadge')
                    : t('humanBadge')}
              </span>
              {choice.roleDescription ? (
                <span className="min-w-0 truncate text-[12px] text-muted">
                  {choice.roleDescription}
                </span>
              ) : null}
            </button>
          ))}
        </div>
      ) : null}
      <div className="overflow-hidden rounded-md border-[1.5px] border-line-strong bg-paper focus-within:border-green-600">
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
          className="block w-full resize-none border-0 bg-transparent px-3.5 py-3 text-[15px] text-ink outline-none placeholder:text-muted"
          onChange={(event) =>
            rememberCaret(event.target.value, event.target.selectionStart)
          }
          onKeyDown={onKeyDown}
          onSelect={(event) => setCaret(event.currentTarget.selectionStart)}
        />
        <div className="flex items-center gap-2 px-2 pb-2">
          <button
            type="button"
            aria-label={t('mention')}
            className="grid h-11 w-11 place-items-center rounded-sm text-[18px] font-black text-ink-2 hover:bg-paper-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
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
          <span
            aria-hidden="true"
            className="grid h-9 w-9 place-items-center text-muted"
          >
            <Paperclip className="h-4 w-4" />
          </span>
          <span className="hidden flex-1 truncate text-[12px] text-muted sm:inline">
            {t('groupPrivacyHint')}
          </span>
          <span className="flex-1 sm:hidden" />
          <Button
            type="submit"
            aria-label={sending ? t('sending') : t('send')}
            disabled={sending || !draft.trim()}
            className="min-h-[40px] px-3.5"
          >
            <Send className="h-4 w-4" aria-hidden="true" />
            <span className="sr-only">
              {sending ? t('sending') : t('send')}
            </span>
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
    <p className="border-t border-line bg-paper-2 px-5 py-3 text-[14px] text-ink-2">
      {t('viewerRead')}
    </p>
  );
}
