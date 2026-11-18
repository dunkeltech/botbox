// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { type FormEvent, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  Button,
  Input,
  ModalDialog,
  RadioCardGroup,
  Textarea,
} from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

export type BotMemory = components['schemas']['BotMemory'];
export type BotMemoryMode = components['schemas']['BotMemoryMode'];

export interface BotMemoryFormInitial {
  memoryId?: string;
  title: string;
  content: string;
  mode: BotMemoryMode;
  tags: string[];
}

export interface BotMemoryFormModalProps {
  open: boolean;
  workspaceId: string;
  botId: string;
  initial: BotMemoryFormInitial | null;
  onClose: () => void;
  onSaved: (memory: BotMemory) => void;
}

function parseTagsInput(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const part of raw.split(',')) {
    const clean = part.trim().replace(/^#+/, '').toLowerCase();
    if (!clean || seen.has(clean)) continue;
    seen.add(clean);
    out.push(clean);
    if (out.length >= 12) break;
  }
  return out;
}

function estimateTokens(
  title: string,
  content: string,
  tags: string[],
): number {
  let chars = title.trim().length + content.trim().length;
  for (const tag of tags) {
    chars += tag.length + 2;
  }
  return Math.max(1, Math.ceil(chars / 4));
}

/**
 * Shared Add/Edit modal dialog for bot knowledge base entries used by both
 * Bot Settings and the in-chat MemorySavedCard.
 */
export function BotMemoryFormModal({
  open,
  workspaceId,
  botId,
  initial,
  onClose,
  onSaved,
}: BotMemoryFormModalProps) {
  const t = useTranslations('botSettings.knowledge');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const isEdit = Boolean(initial?.memoryId);
  const [title, setTitle] = useState(initial?.title ?? '');
  const [content, setContent] = useState(initial?.content ?? '');
  const [mode, setMode] = useState<BotMemoryMode>(initial?.mode ?? 'on_demand');
  const [tagsRaw, setTagsRaw] = useState((initial?.tags ?? []).join(', '));
  const [saving, setSaving] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  useEffect(() => {
    if (!open) return;
    setTitle(initial?.title ?? '');
    setContent(initial?.content ?? '');
    setMode(initial?.mode ?? 'on_demand');
    setTagsRaw((initial?.tags ?? []).join(', '));
    setErrorKey(null);
    setSaving(false);
  }, [open, initial]);

  const parsedTags = parseTagsInput(tagsRaw);
  const tokenEstimate = estimateTokens(title, content, parsedTags);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (saving) return;
    setSaving(true);
    setErrorKey(null);
    try {
      const base = `/api/workspaces/${workspaceId}/bots/${botId}/memories`;
      const payload = {
        title: title.trim(),
        content: content.trim(),
        mode,
        tags: parsedTags,
      };
      const saved =
        isEdit && initial?.memoryId
          ? await requestJson<BotMemory>(`${base}/${initial.memoryId}`, {
              method: 'PATCH',
              body: payload,
            })
          : await requestJson<BotMemory>(base, {
              method: 'POST',
              body: payload,
            });
      onSaved(saved);
      onClose();
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setSaving(false);
    }
  }

  return (
    <ModalDialog
      open={open}
      onClose={onClose}
      title={isEdit ? t('modalEditTitle') : t('modalAddTitle')}
      subtitle={isEdit ? t('modalEditSubtitle') : t('modalAddSubtitle')}
      closeLabel={tActions('close')}
    >
      <form
        onSubmit={(e) => void handleSubmit(e)}
        className="flex flex-col gap-4"
      >
        {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

        <Input
          label={t('fieldTitle')}
          placeholder={t('fieldTitlePlaceholder')}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          maxLength={160}
          required
          disabled={saving}
        />

        <Textarea
          label={t('fieldContent')}
          placeholder={t('fieldContentPlaceholder')}
          value={content}
          onChange={(e) => setContent(e.target.value)}
          maxLength={4000}
          rows={4}
          required
          disabled={saving}
        />

        <RadioCardGroup<BotMemoryMode>
          legend={t('fieldMode')}
          name="memory-mode"
          value={mode}
          onChange={setMode}
          disabled={saving}
          options={[
            {
              value: 'always',
              title: t('modeAlwaysTitle'),
              description: t('modeAlwaysDesc'),
            },
            {
              value: 'on_demand',
              title: t('modeOnDemandTitle'),
              description: t('modeOnDemandDesc'),
            },
          ]}
        />

        <Input
          label={t('fieldTags')}
          placeholder={t('fieldTagsPlaceholder')}
          helperText={t('fieldTagsHelper')}
          value={tagsRaw}
          onChange={(e) => setTagsRaw(e.target.value)}
          mono
          disabled={saving}
        />

        <div className="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-line bg-paper-2 px-3 py-2 text-[13px] text-ink-2">
          <span className="font-mono">
            {t('tokenEstimatePreview', { tokens: tokenEstimate })}
          </span>
          {parsedTags.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {parsedTags.map((tag) => (
                <span
                  key={tag}
                  className="rounded-xs border border-line bg-paper px-2 py-0.5 font-mono text-[11px] text-ink-2"
                >
                  #{tag}
                </span>
              ))}
            </div>
          ) : null}
        </div>

        <div className="flex flex-wrap justify-end gap-2.5 pt-2">
          <Button
            type="button"
            variant="secondary"
            onClick={onClose}
            disabled={saving}
          >
            {tActions('cancel')}
          </Button>
          <Button
            type="submit"
            disabled={saving || !title.trim() || !content.trim()}
          >
            {saving ? t('saving') : isEdit ? t('saveEdit') : t('saveCreate')}
          </Button>
        </div>
      </form>
    </ModalDialog>
  );
}
