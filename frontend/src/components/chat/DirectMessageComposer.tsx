// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { Send } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Button } from '@/components/ui';

/** Keeps draft updates local to the composer shared by both direct chat layouts. */
export function DirectMessageComposer({
  name,
  canChat,
  showPrivacyHint,
  postMessage,
  onSent,
  onSendError,
}: {
  name: string;
  canChat: boolean;
  showPrivacyHint: boolean;
  postMessage?: (content: string) => Promise<unknown>;
  onSent: () => Promise<void>;
  onSendError: (hasError: boolean) => void;
}) {
  const t = useTranslations('chat');
  const tTeam = useTranslations('team');
  const inputId = useId();
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);

  async function submitDraft() {
    if (!canChat || !postMessage || !draft.trim() || sending) return;
    setSending(true);
    onSendError(false);
    try {
      await postMessage(draft);
      setDraft('');
      await onSent();
    } catch {
      onSendError(true);
    } finally {
      setSending(false);
    }
  }

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        void submitDraft();
      }}
      className="overflow-hidden rounded-md border-[1.5px] border-line-strong bg-paper focus-within:border-green-600"
    >
      <label htmlFor={inputId} className="sr-only">
        {t('message')}
      </label>
      <textarea
        id={inputId}
        rows={2}
        maxLength={8192}
        value={draft}
        disabled={!canChat || sending}
        placeholder={t('messagePlaceholder', { name })}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter' && !event.shiftKey) {
            event.preventDefault();
            void submitDraft();
          }
        }}
        className="block w-full resize-none border-0 bg-transparent px-3.5 pt-3 text-[15px] text-ink outline-none placeholder:text-muted disabled:cursor-not-allowed disabled:bg-paper-2"
      />
      <div className="flex items-center justify-between gap-2 px-3 pb-2 pt-1">
        <span className="text-[12px] text-muted">
          {showPrivacyHint ? tTeam('dmPrivacyHint', { name }) : ''}
        </span>
        <Button
          type="submit"
          aria-label={sending ? t('sending') : t('send')}
          disabled={!canChat || sending || !postMessage || !draft.trim()}
          className="min-h-[40px] px-3.5"
        >
          <Send className="h-4 w-4" aria-hidden="true" />
          <span className="sr-only">{sending ? t('sending') : t('send')}</span>
        </Button>
      </div>
    </form>
  );
}
