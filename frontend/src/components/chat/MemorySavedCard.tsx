// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowRight, BookmarkCheck, Pencil, RotateCcw } from 'lucide-react';
import Link from 'next/link';
import { useState } from 'react';
import { useTranslations } from 'next-intl';

import { BotMemoryFormModal } from '@/components/bots/BotMemoryFormModal';
import { Alert, Button } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

export type ChatMessageMemory = components['schemas']['ChatMessageMemory'];

export interface MemorySavedCardProps {
  workspaceId: string;
  botId: string;
  memory: ChatMessageMemory;
  canManage: boolean;
  onUpdated: () => Promise<void> | void;
  className?: string;
}

/**
 * Inline chat card confirming a bot knowledge base save, with mode badge,
 * tags, Edit modal, Undo (delete), and link to Bot Settings -> Knowledge base.
 */
export function MemorySavedCard({
  workspaceId,
  botId,
  memory,
  canManage,
  onUpdated,
  className = '',
}: MemorySavedCardProps) {
  const t = useTranslations('chat');
  const tErrors = useTranslations('errors');

  const [editOpen, setEditOpen] = useState(false);
  const [undoBusy, setUndoBusy] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  const isAlways = memory.mode === 'always';
  const isDeleted = memory.deleted;

  async function handleUndo() {
    if (!canManage || isDeleted || undoBusy) return;
    setUndoBusy(true);
    setErrorKey(null);
    try {
      await requestJson(
        `/api/workspaces/${workspaceId}/bots/${botId}/memories/${memory.memoryId}`,
        { method: 'DELETE' },
      );
      await onUpdated();
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setUndoBusy(false);
    }
  }

  return (
    <div
      data-memory-id={memory.memoryId}
      className={`rounded-lg border-[1.5px] p-4 shadow-card ${
        isDeleted ? 'border-line bg-paper-2' : 'border-mint-line bg-paper'
      } ${className}`.trim()}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className={`grid h-7 w-7 shrink-0 place-items-center rounded-xs ${
              isDeleted ? 'bg-paper text-muted' : 'bg-mint-soft text-green-600'
            }`}
          >
            <BookmarkCheck className="h-4 w-4" aria-hidden="true" />
          </span>
          <span
            className={`inline-flex items-center gap-1.5 rounded-pill border px-2.5 py-0.5 text-[12px] font-bold ${
              isDeleted
                ? 'border-line bg-paper text-muted'
                : 'border-mint-line bg-mint-soft text-green-600'
            }`}
          >
            <span
              className={`h-2 w-2 rounded-full ${
                isDeleted ? 'bg-muted' : 'bg-green'
              }`}
              aria-hidden="true"
            />
            {isDeleted ? t('memoryRemovedBadge') : t('memorySavedBadge')}
          </span>
        </div>

        <span
          className={`inline-flex items-center rounded-pill border px-2.5 py-0.5 text-[12px] font-bold ${
            isAlways
              ? 'border-mint-line bg-mint-soft text-green-600'
              : 'border-blue-line bg-blue-tint text-blue'
          }`}
        >
          {isAlways ? t('memoryModeAlways') : t('memoryModeOnDemand')}
        </span>
      </div>

      <p className="mt-2.5 text-[15px] font-black text-ink">{memory.title}</p>
      <p className="mt-1 whitespace-pre-wrap break-words text-[14px] leading-[1.5] text-ink-2">
        {memory.content}
      </p>

      {memory.tags.length > 0 ? (
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          {memory.tags.map((tag) => (
            <span
              key={tag}
              className="rounded-xs border border-line bg-paper-2 px-2 py-0.5 font-mono text-[12px] text-ink-2"
            >
              #{tag}
            </span>
          ))}
        </div>
      ) : null}

      <p className="mt-2.5 font-mono text-[12px] text-muted">
        {t('memoryMeta', {
          source: memory.sourceLabel,
          tokens: memory.tokenEstimate,
        })}
      </p>

      {errorKey ? (
        <Alert tone="warning" className="mt-3">
          {tErrors(errorKey)}
        </Alert>
      ) : null}

      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
        {canManage && !isDeleted ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={undoBusy}
              onClick={() => setEditOpen(true)}
              className="px-3 text-[13px]"
            >
              <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
              <span>{t('memoryEdit')}</span>
            </Button>
            <button
              type="button"
              disabled={undoBusy}
              onClick={() => void handleUndo()}
              className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-amber-line bg-paper px-3 text-[13px] font-bold text-amber transition duration-150 hover:bg-amber-tint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60"
            >
              <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
              <span>{undoBusy ? t('memoryUndoing') : t('memoryUndo')}</span>
            </button>
          </div>
        ) : (
          <span />
        )}

        <Link
          href={`/bots/${botId}/settings/knowledge`}
          className="inline-flex min-h-[44px] items-center gap-1 rounded-sm px-2 text-[13px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <span>{t('memoryOpenKnowledgeBase')}</span>
          <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
        </Link>
      </div>

      <BotMemoryFormModal
        open={editOpen}
        workspaceId={workspaceId}
        botId={botId}
        initial={{
          memoryId: memory.memoryId,
          title: memory.title,
          content: memory.content,
          mode: memory.mode,
          tags: memory.tags,
        }}
        onClose={() => setEditOpen(false)}
        onSaved={() => {
          void onUpdated();
        }}
      />
    </div>
  );
}
