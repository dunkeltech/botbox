// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { BookOpen, Pencil, Plus, ShieldCheck, Trash2 } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  type BotMemory,
  type BotMemoryFormInitial,
  BotMemoryFormModal,
  type BotMemoryMode,
} from '@/components/bots/BotMemoryFormModal';
import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  ModalDialog,
  SegmentedControl,
  Select,
} from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

type BotMemoryListResponse = components['schemas']['BotMemoryListResponse'];
type BotMemorySummary = components['schemas']['BotMemorySummary'];
type BotMemorySourceKind = components['schemas']['BotMemorySourceKind'];

type ModeFilter = 'all' | BotMemoryMode;
type SourceFilter = 'all' | BotMemorySourceKind;

const PAGE_SIZE = 10;

export interface BotKnowledgeBasePanelProps {
  workspaceId: string;
  botId: string;
  botName: string;
  canManage: boolean;
}

function formatUpdatedAt(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/**
 * Per-bot Knowledge Base management panel in Bot Settings:
 * auto-save switch, always-in-context token budget meter, search/filters,
 * entry CRUD, inline mode switching, and amber-toned Danger Zone clear action.
 */
export function BotKnowledgeBasePanel({
  workspaceId,
  botId,
  botName,
  canManage,
}: BotKnowledgeBasePanelProps) {
  const t = useTranslations('botSettings.knowledge');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const [data, setData] = useState<BotMemoryListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  const [searchQuery, setSearchQuery] = useState('');
  const [modeFilter, setModeFilter] = useState<ModeFilter>('all');
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>('all');
  const [tagFilter, setTagFilter] = useState('');
  const [offset, setOffset] = useState(0);

  const [policyBusy, setPolicyBusy] = useState(false);
  const [busyEntryId, setBusyEntryId] = useState<string | null>(null);

  const [formOpen, setFormOpen] = useState(false);
  const [formInitial, setFormInitial] = useState<BotMemoryFormInitial | null>(
    null,
  );
  const [deleteTarget, setDeleteTarget] = useState<BotMemory | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [clearOpen, setClearOpen] = useState(false);
  const [clearing, setClearing] = useState(false);

  const loadMemories = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      params.set('limit', String(PAGE_SIZE));
      params.set('offset', String(offset));
      if (searchQuery.trim()) {
        params.set('q', searchQuery.trim());
      }
      if (modeFilter !== 'all') {
        params.set('mode', modeFilter);
      }
      if (sourceFilter !== 'all') {
        params.set('source', sourceFilter);
      }
      if (tagFilter) {
        params.set('tag', tagFilter);
      }
      const res = await requestJson<BotMemoryListResponse>(
        `/api/workspaces/${workspaceId}/bots/${botId}/memories?${params.toString()}`,
      );
      setData(res);
      setErrorKey(null);
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setLoading(false);
    }
  }, [
    workspaceId,
    botId,
    offset,
    searchQuery,
    modeFilter,
    sourceFilter,
    tagFilter,
  ]);

  useEffect(() => {
    void loadMemories();
  }, [loadMemories]);

  const summary = data?.summary;
  const entries = data?.entries ?? [];
  const filteredCount = data?.filteredCount ?? 0;
  const availableTags = data?.tags ?? [];

  const alwaysUsed = summary?.alwaysTokens ?? 0;
  const alwaysLimit = summary?.alwaysTokenLimit ?? 4000;
  const budgetPct =
    alwaysLimit > 0
      ? Math.min(100, Math.round((alwaysUsed / alwaysLimit) * 100))
      : 0;
  const budgetHigh = budgetPct >= 80;

  async function handleToggleAutoSave() {
    if (!canManage || !summary || policyBusy) return;
    setPolicyBusy(true);
    setErrorKey(null);
    try {
      const updated = await requestJson<BotMemorySummary>(
        `/api/workspaces/${workspaceId}/bots/${botId}/memories/policy`,
        {
          method: 'PUT',
          body: { autoSave: !summary.autoSave },
        },
      );
      setData((prev) => (prev ? { ...prev, summary: updated } : prev));
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setPolicyBusy(false);
    }
  }

  async function handleSwitchMode(entry: BotMemory, nextMode: BotMemoryMode) {
    if (!canManage || entry.mode === nextMode || busyEntryId) return;
    setBusyEntryId(entry.id);
    setErrorKey(null);
    try {
      await requestJson<BotMemory>(
        `/api/workspaces/${workspaceId}/bots/${botId}/memories/${entry.id}`,
        {
          method: 'PATCH',
          body: { mode: nextMode },
        },
      );
      await loadMemories();
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusyEntryId(null);
    }
  }

  async function handleConfirmDelete() {
    if (!canManage || !deleteTarget || deleting) return;
    setDeleting(true);
    setErrorKey(null);
    try {
      await requestJson(
        `/api/workspaces/${workspaceId}/bots/${botId}/memories/${deleteTarget.id}`,
        { method: 'DELETE' },
      );
      setDeleteTarget(null);
      await loadMemories();
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setDeleting(false);
    }
  }

  async function handleConfirmClear() {
    if (!canManage || clearing) return;
    setClearing(true);
    setErrorKey(null);
    try {
      await requestJson(
        `/api/workspaces/${workspaceId}/bots/${botId}/memories`,
        { method: 'DELETE' },
      );
      setClearOpen(false);
      setOffset(0);
      await loadMemories();
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setClearing(false);
    }
  }

  function openAddModal() {
    setFormInitial({
      title: '',
      content: '',
      mode: 'on_demand',
      tags: [],
    });
    setFormOpen(true);
  }

  function openEditModal(entry: BotMemory) {
    setFormInitial({
      memoryId: entry.id,
      title: entry.title,
      content: entry.content,
      mode: entry.mode,
      tags: entry.tags,
    });
    setFormOpen(true);
  }

  const hasFilters =
    searchQuery.trim() !== '' ||
    modeFilter !== 'all' ||
    sourceFilter !== 'all' ||
    tagFilter !== '';

  return (
    <div className="flex flex-col gap-6">
      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      {!canManage ? <Alert tone="info">{t('readOnlyNotice')}</Alert> : null}

      <Card className="p-6">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('policyEyebrow')}</Eyebrow>
            <h2 className="text-[22px] font-black leading-[1.3] text-ink">
              {t('policyTitle')}
            </h2>
            <p className="max-w-2xl text-[14px] leading-[1.5] text-ink-2">
              {t('policyLead', { name: botName })}
            </p>
          </div>
          {canManage ? (
            <Button type="button" onClick={openAddModal}>
              <Plus className="h-4 w-4" aria-hidden="true" />
              <span>{t('addMemory')}</span>
            </Button>
          ) : null}
        </div>

        <div className="mt-5 flex flex-wrap items-center justify-between gap-4 border-t border-line pt-5">
          <div className="flex max-w-2xl flex-col gap-1">
            <span className="text-[15px] font-bold text-ink">
              {t('autoSaveLabel')}
            </span>
            <span className="text-[14px] leading-[1.5] text-muted">
              {t('autoSaveHelper', { name: botName })}
            </span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={summary?.autoSave ?? true}
            aria-label={t('autoSaveLabel')}
            disabled={!canManage || policyBusy || !summary}
            onClick={() => void handleToggleAutoSave()}
            className={`relative inline-flex h-[30px] w-[52px] shrink-0 items-center rounded-pill transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
              (summary?.autoSave ?? true) ? 'bg-green' : 'bg-line-strong'
            } ${!canManage || policyBusy ? 'cursor-not-allowed opacity-70' : 'cursor-pointer'}`}
          >
            <span
              className={`inline-block h-6 w-6 transform rounded-full bg-paper shadow transition duration-150 ${
                (summary?.autoSave ?? true)
                  ? 'translate-x-[25px]'
                  : 'translate-x-[3px]'
              }`}
            />
          </button>
        </div>

        <div className="mt-5 rounded-md border border-line bg-paper-2 p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-[14px] font-bold text-ink">
              {t('budgetLabel')}
            </span>
            <span className="font-mono text-[13px] font-bold text-ink-2">
              {t('budgetReadout', {
                pinned: summary?.alwaysCount ?? 0,
                used: alwaysUsed,
                limit: alwaysLimit,
                percent: budgetPct,
              })}
            </span>
          </div>

          <div
            role="progressbar"
            aria-label={t('budgetLabel')}
            aria-valuemin={0}
            aria-valuemax={alwaysLimit}
            aria-valuenow={alwaysUsed}
            className="relative mt-2.5 h-2 w-full overflow-hidden rounded-pill bg-line"
          >
            <div
              className={`h-full rounded-pill transition-all duration-150 ${
                budgetHigh ? 'bg-amber' : 'bg-green'
              }`}
              style={{ width: `${budgetPct}%` }}
            />
            <span
              aria-hidden="true"
              className="absolute bottom-0 top-0 left-[80%] w-[2px] bg-ink-2/30"
            />
          </div>

          <p className="mt-2.5 text-[13px] leading-[1.5] text-muted">
            {budgetHigh ? t('budgetWarning') : t('budgetHelper')}
          </p>
        </div>

        <div className="mt-5 flex flex-col gap-2 rounded-md bg-terminal p-4 text-on-dark">
          <div className="flex items-center gap-2 text-[13px] font-bold text-green-light">
            <ShieldCheck className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>{t('isolationTitle')}</span>
          </div>
          <p className="font-mono text-[12px] leading-[1.6] text-on-dark-muted">
            {t('isolationBody', { name: botName })}
          </p>
          <div className="flex items-center gap-2 font-mono text-[12px] text-green-light">
            <span
              className="h-2 w-2 rounded-full bg-green-light"
              aria-hidden="true"
            />
            <span>{t('isolationFooter')}</span>
          </div>
        </div>
      </Card>

      <Card className="p-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('entriesEyebrow')}</Eyebrow>
            <h2 className="text-[22px] font-black leading-[1.3] text-ink">
              {t('entriesTitle')}
            </h2>
          </div>
          <span className="rounded-pill border border-line bg-paper-2 px-3 py-1 font-mono text-[12px] font-bold text-ink-2">
            {t('summaryBadge', {
              total: summary?.totalCount ?? 0,
              always: summary?.alwaysCount ?? 0,
              onDemand: summary?.onDemandCount ?? 0,
            })}
          </span>
        </div>

        <div className="mt-5 flex flex-col gap-3">
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-[220px] flex-1">
              <Input
                label={t('searchLabel')}
                placeholder={t('searchPlaceholder')}
                value={searchQuery}
                onChange={(e) => {
                  setSearchQuery(e.target.value);
                  setOffset(0);
                }}
              />
            </div>

            <div className="w-full sm:w-auto">
              <SegmentedControl<ModeFilter>
                ariaLabel={t('fieldMode')}
                value={modeFilter}
                onChange={(next) => {
                  setModeFilter(next);
                  setOffset(0);
                }}
                options={[
                  {
                    value: 'all',
                    label: t('filterAll', { count: summary?.totalCount ?? 0 }),
                  },
                  {
                    value: 'always',
                    label: t('filterAlways', {
                      count: summary?.alwaysCount ?? 0,
                    }),
                  },
                  {
                    value: 'on_demand',
                    label: t('filterOnDemand', {
                      count: summary?.onDemandCount ?? 0,
                    }),
                  },
                ]}
              />
            </div>

            <div className="w-full sm:w-44">
              <Select
                label={t('sourceLabel')}
                hideLabel
                value={sourceFilter}
                onChange={(e) => {
                  setSourceFilter(e.target.value as SourceFilter);
                  setOffset(0);
                }}
              >
                <option value="all">{t('sourceAll')}</option>
                <option value="chat">{t('sourceChat')}</option>
                <option value="manual">{t('sourceManual')}</option>
              </Select>
            </div>
          </div>

          {availableTags.length > 0 ? (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="mr-1 text-[12px] font-bold text-muted">
                {t('tagFilterLabel')}
              </span>
              <button
                type="button"
                aria-pressed={tagFilter === ''}
                onClick={() => {
                  setTagFilter('');
                  setOffset(0);
                }}
                className={`min-h-[36px] rounded-xs border px-2.5 font-mono text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                  tagFilter === ''
                    ? 'border-dark bg-dark text-paper'
                    : 'border-line bg-paper-2 text-ink-2 hover:bg-mint-soft'
                }`}
              >
                {t('tagAll')}
              </button>
              {availableTags.map((tag) => {
                const active = tagFilter === tag;
                return (
                  <button
                    key={tag}
                    type="button"
                    aria-pressed={active}
                    onClick={() => {
                      setTagFilter(active ? '' : tag);
                      setOffset(0);
                    }}
                    className={`min-h-[36px] rounded-xs border px-2.5 font-mono text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      active
                        ? 'border-dark bg-dark text-paper'
                        : 'border-line bg-paper-2 text-ink-2 hover:bg-mint-soft'
                    }`}
                  >
                    #{tag}
                  </button>
                );
              })}
            </div>
          ) : null}
        </div>

        <div className="mt-5 flex flex-col gap-3">
          {loading && !data ? (
            <p
              role="status"
              className="py-6 text-center text-[14px] text-muted"
            >
              {tActions('loading')}
            </p>
          ) : entries.length === 0 ? (
            <div className="flex flex-col items-center gap-2 rounded-md border border-dashed border-line-strong bg-paper-2 px-4 py-8 text-center">
              <BookOpen className="h-5 w-5 text-muted" aria-hidden="true" />
              <p className="max-w-md text-[14px] text-ink-2">
                {hasFilters
                  ? t('emptyFiltered')
                  : t('empty', { name: botName })}
              </p>
            </div>
          ) : (
            entries.map((entry) => {
              const isAlways = entry.mode === 'always';
              const entryBusy = busyEntryId === entry.id;
              return (
                <article
                  key={entry.id}
                  className="flex flex-col gap-3 rounded-md border border-line bg-paper p-4 transition duration-150 hover:border-line-strong"
                >
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="text-[16px] font-black text-ink">
                          {entry.title}
                        </h3>
                        <span
                          className={`inline-flex items-center rounded-pill border px-2.5 py-0.5 text-[12px] font-bold ${
                            isAlways
                              ? 'border-mint-line bg-mint-soft text-green-600'
                              : 'border-blue-line bg-blue-tint text-blue'
                          }`}
                        >
                          {isAlways ? t('modeAlways') : t('modeOnDemand')}
                        </span>
                      </div>
                      <p className="mt-1 font-mono text-[12px] text-muted">
                        {t('entryMeta', {
                          source: entry.sourceLabel || entry.sourceKind,
                          updated: formatUpdatedAt(entry.updatedAt),
                          tokens: entry.tokenEstimate,
                        })}
                      </p>
                    </div>

                    {canManage ? (
                      <div className="flex flex-wrap items-center gap-2">
                        <div
                          role="group"
                          aria-label={t('modeToggleGroup', {
                            title: entry.title,
                          })}
                          className="inline-flex rounded-sm border border-line bg-paper-2 p-0.5"
                        >
                          <button
                            type="button"
                            aria-pressed={isAlways}
                            disabled={entryBusy}
                            onClick={() =>
                              void handleSwitchMode(entry, 'always')
                            }
                            className={`min-h-[40px] rounded-xs px-2.5 text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                              isAlways
                                ? 'bg-paper text-green-600 shadow-sm'
                                : 'text-ink-2 hover:text-ink'
                            }`}
                          >
                            {t('switchAlwaysShort')}
                          </button>
                          <button
                            type="button"
                            aria-pressed={!isAlways}
                            disabled={entryBusy}
                            onClick={() =>
                              void handleSwitchMode(entry, 'on_demand')
                            }
                            className={`min-h-[40px] rounded-xs px-2.5 text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                              !isAlways
                                ? 'bg-paper text-blue shadow-sm'
                                : 'text-ink-2 hover:text-ink'
                            }`}
                          >
                            {t('switchOnDemandShort')}
                          </button>
                        </div>

                        <Button
                          type="button"
                          variant="secondary"
                          disabled={entryBusy}
                          onClick={() => openEditModal(entry)}
                          className="px-3 text-[13px]"
                        >
                          <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
                          <span>{t('editAction')}</span>
                        </Button>

                        <button
                          type="button"
                          aria-label={t('deleteAction', { title: entry.title })}
                          disabled={entryBusy}
                          onClick={() => setDeleteTarget(entry)}
                          className="inline-flex min-h-[44px] min-w-[44px] items-center justify-center rounded-sm border border-amber-line bg-paper text-amber transition duration-150 hover:bg-amber-tint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                        >
                          <Trash2 className="h-4 w-4" aria-hidden="true" />
                        </button>
                      </div>
                    ) : null}
                  </div>

                  <p className="whitespace-pre-wrap break-words rounded-sm border border-line bg-paper-2 px-3 py-2.5 text-[14px] leading-[1.55] text-ink-2">
                    {entry.content}
                  </p>

                  {entry.tags.length > 0 ? (
                    <div className="flex flex-wrap gap-1.5">
                      {entry.tags.map((tag) => (
                        <span
                          key={tag}
                          className="rounded-xs border border-line bg-paper-2 px-2 py-0.5 font-mono text-[12px] text-ink-2"
                        >
                          #{tag}
                        </span>
                      ))}
                    </div>
                  ) : null}
                </article>
              );
            })
          )}
        </div>

        {filteredCount > 0 ? (
          <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-line pt-4">
            <span className="text-[13px] text-muted">
              {t('showingCount', {
                shown: entries.length,
                total: filteredCount,
              })}
            </span>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="secondary"
                disabled={offset === 0 || loading}
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              >
                {t('previousPage')}
              </Button>
              <Button
                type="button"
                variant="secondary"
                disabled={offset + entries.length >= filteredCount || loading}
                onClick={() => setOffset(offset + PAGE_SIZE)}
              >
                {t('nextPage')}
              </Button>
            </div>
          </div>
        ) : null}
      </Card>

      {canManage ? (
        <section
          aria-labelledby="kb-danger-heading"
          className="rounded-lg border-[1.5px] border-dashed border-amber-line bg-amber-tint p-6"
        >
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('dangerEyebrow')}</Eyebrow>
            <h2
              id="kb-danger-heading"
              className="text-[22px] font-black leading-[1.3] text-ink"
            >
              {t('dangerTitle')}
            </h2>
          </div>

          <div className="mt-4 flex flex-wrap items-center justify-between gap-4 rounded-md border border-amber-line bg-paper p-4">
            <div className="flex max-w-2xl flex-col gap-1">
              <p className="text-[16px] font-black text-ink">
                {t('clearTitle', { name: botName })}
              </p>
              <p className="text-[14px] leading-[1.5] text-ink-2">
                {t('clearBody', {
                  name: botName,
                  count: summary?.totalCount ?? 0,
                })}
              </p>
            </div>

            <button
              type="button"
              disabled={(summary?.totalCount ?? 0) === 0 || clearing}
              onClick={() => setClearOpen(true)}
              className="inline-flex min-h-[44px] items-center gap-2 rounded-sm border border-dashed border-amber-line bg-paper px-4 text-[14px] font-bold text-amber transition duration-150 hover:bg-amber-tint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60"
            >
              <Trash2 className="h-4 w-4" aria-hidden="true" />
              <span>{t('clearButton')}</span>
            </button>
          </div>
        </section>
      ) : null}

      <BotMemoryFormModal
        open={formOpen}
        workspaceId={workspaceId}
        botId={botId}
        initial={formInitial}
        onClose={() => setFormOpen(false)}
        onSaved={() => {
          void loadMemories();
        }}
      />

      <ModalDialog
        open={Boolean(deleteTarget)}
        onClose={() => {
          if (!deleting) setDeleteTarget(null);
        }}
        title={t('deleteTitle')}
        closeLabel={tActions('close')}
      >
        <p className="text-[14px] leading-[1.55] text-ink-2">
          {t('deleteBody', {
            title: deleteTarget?.title ?? '',
            name: botName,
          })}
        </p>
        <div className="flex flex-wrap justify-end gap-2.5 pt-2">
          <Button
            type="button"
            variant="secondary"
            disabled={deleting}
            onClick={() => setDeleteTarget(null)}
          >
            {tActions('cancel')}
          </Button>
          <button
            type="button"
            disabled={deleting}
            onClick={() => void handleConfirmDelete()}
            className="inline-flex min-h-[44px] items-center justify-center rounded-sm border border-amber-line bg-amber-tint px-4 text-[14px] font-bold text-amber transition duration-150 hover:border-amber focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60"
          >
            {deleting ? t('deleting') : t('deleteConfirm')}
          </button>
        </div>
      </ModalDialog>

      <ModalDialog
        open={clearOpen}
        onClose={() => {
          if (!clearing) setClearOpen(false);
        }}
        title={t('clearDialogTitle', { name: botName })}
        closeLabel={tActions('close')}
      >
        <p className="text-[14px] leading-[1.55] text-ink-2">
          {t('clearDialogBody', {
            name: botName,
            count: summary?.totalCount ?? 0,
          })}
        </p>
        <div className="flex flex-wrap justify-end gap-2.5 pt-2">
          <Button
            type="button"
            variant="secondary"
            disabled={clearing}
            onClick={() => setClearOpen(false)}
          >
            {tActions('cancel')}
          </Button>
          <button
            type="button"
            disabled={clearing}
            onClick={() => void handleConfirmClear()}
            className="inline-flex min-h-[44px] items-center justify-center rounded-sm border border-dashed border-amber-line bg-amber-tint px-4 text-[14px] font-bold text-amber transition duration-150 hover:border-amber focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60"
          >
            {clearing ? t('clearing') : t('clearConfirm')}
          </button>
        </div>
      </ModalDialog>
    </div>
  );
}
