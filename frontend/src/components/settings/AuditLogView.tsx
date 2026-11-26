// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ShieldCheck } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { type FormEvent, useCallback, useEffect, useId, useState } from 'react';

import { Button, Card, Eyebrow, Input } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type WorkspaceRole = components['schemas']['WorkspaceRole'];
type AuditLogEntry = components['schemas']['AuditLogEntry'];
type AuditLogListResponse = components['schemas']['AuditLogListResponse'];

const PAGE_LIMIT = 25;

export interface AuditLogViewProps {
  workspaceId: string;
  role: WorkspaceRole;
}

function formatActor(entry: AuditLogEntry, systemLabel: string): string {
  if (entry.actorDisplayName && entry.actorEmail) {
    return `${entry.actorDisplayName} (${entry.actorEmail})`;
  }
  if (entry.actorEmail) {
    return entry.actorEmail;
  }
  if (entry.actorDisplayName) {
    return entry.actorDisplayName;
  }
  if (entry.actorUserId) {
    return entry.actorUserId;
  }
  return systemLabel;
}

function formatTarget(entry: AuditLogEntry): string {
  if (entry.targetType && entry.targetId) {
    return `${entry.targetType}:${entry.targetId.slice(0, 8)}`;
  }
  if (entry.targetType) {
    return entry.targetType;
  }
  if (entry.targetId) {
    return entry.targetId;
  }
  return '';
}

function formatMetadata(meta: AuditLogEntry['metadata']): string {
  if (!meta || Object.keys(meta).length === 0) {
    return '{}';
  }
  return JSON.stringify(meta);
}

/**
 * Filterable, paginated workspace audit log inspector restricted to owners and admins.
 */
export function AuditLogView({ workspaceId, role }: AuditLogViewProps) {
  const t = useTranslations('workspaceSettings.audit');
  const actionInputId = useId();
  const targetSelectId = useId();

  const canViewAudit = role === 'owner' || role === 'admin';

  const [actionDraft, setActionDraft] = useState('');
  const [targetTypeDraft, setTargetTypeDraft] = useState('');
  const [appliedAction, setAppliedAction] = useState('');
  const [appliedTargetType, setAppliedTargetType] = useState('');
  const [offset, setOffset] = useState(0);

  const [entries, setEntries] = useState<AuditLogEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [isLoading, setIsLoading] = useState(canViewAudit);
  const [loadFailed, setLoadFailed] = useState(false);

  const loadAuditLogs = useCallback(async () => {
    if (!canViewAudit) {
      setIsLoading(false);
      return;
    }
    setIsLoading(true);
    setLoadFailed(false);
    const params = new URLSearchParams({
      limit: String(PAGE_LIMIT),
      offset: String(offset),
    });
    if (appliedAction.trim() !== '') {
      params.set('action', appliedAction.trim());
    }
    if (appliedTargetType.trim() !== '') {
      params.set('targetType', appliedTargetType.trim());
    }
    try {
      const resp = await requestJson<AuditLogListResponse>(
        `/api/workspaces/${workspaceId}/audit-logs?${params.toString()}`,
      );
      setEntries(resp.entries);
      setTotal(resp.total);
    } catch {
      setLoadFailed(true);
      setEntries([]);
      setTotal(0);
    } finally {
      setIsLoading(false);
    }
  }, [appliedAction, appliedTargetType, canViewAudit, offset, workspaceId]);

  useEffect(() => {
    void loadAuditLogs();
  }, [loadAuditLogs]);

  const handleApplyFilters = (e: FormEvent) => {
    e.preventDefault();
    setOffset(0);
    setAppliedAction(actionDraft);
    setAppliedTargetType(targetTypeDraft);
  };

  const handleResetFilters = () => {
    setActionDraft('');
    setTargetTypeDraft('');
    setOffset(0);
    setAppliedAction('');
    setAppliedTargetType('');
  };

  if (!canViewAudit) {
    return (
      <Card>
        <div className="flex items-start gap-3">
          <div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-md border border-amber-line bg-amber-tint text-amber">
            <ShieldCheck className="size-4" aria-hidden="true" />
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <Eyebrow>{t('eyebrow')}</Eyebrow>
              <span className="inline-flex items-center rounded-sm border border-amber-line bg-amber-tint px-2 py-0.5 text-[11px] font-bold uppercase tracking-[0.06em] text-amber">
                {t('adminOnlyBadge')}
              </span>
            </div>
            <h2 className="mt-1 text-[17px] font-bold tracking-[-0.015em] text-ink">
              {t('adminOnlyTitle')}
            </h2>
            <p className="mt-1 text-[14px] leading-relaxed text-ink-2">
              {t('adminOnlyBody')}
            </p>
          </div>
        </div>
      </Card>
    );
  }

  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(offset + entries.length, total);
  const hasPrev = offset > 0;
  const hasNext = offset + PAGE_LIMIT < total;

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <Eyebrow>{t('eyebrow')}</Eyebrow>
            <h2 className="mt-1 text-[17px] font-bold tracking-[-0.015em] text-ink">
              {t('title')}
            </h2>
            <p className="mt-1 text-[14px] leading-relaxed text-ink-2">
              {t('lead')}
            </p>
          </div>
          <span className="inline-flex items-center rounded-sm border border-line bg-paper-2 px-2.5 py-1 font-mono text-[12px] font-medium text-ink-2">
            {t('totalCount', { count: total })}
          </span>
        </div>

        <form
          onSubmit={handleApplyFilters}
          className="mt-4 grid grid-cols-1 gap-3 border-t border-line pt-4 sm:grid-cols-[1fr_220px_auto]"
        >
          <div>
            <Input
              id={actionInputId}
              label={t('actionFilterLabel')}
              value={actionDraft}
              onChange={(e) => setActionDraft(e.target.value)}
              placeholder={t('actionFilterPlaceholder')}
            />
          </div>

          <div>
            <label
              htmlFor={targetSelectId}
              className="mb-1 block text-[12px] font-bold uppercase tracking-[0.06em] text-ink-2"
            >
              {t('targetTypeFilterLabel')}
            </label>
            <select
              id={targetSelectId}
              value={targetTypeDraft}
              onChange={(e) => setTargetTypeDraft(e.target.value)}
              className="min-h-[44px] w-full rounded-sm border border-line-2 bg-paper px-3 text-[14px] text-ink transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <option value="">{t('targetTypeAll')}</option>
              <option value="bot">{t('targetBot')}</option>
              <option value="environment">{t('targetEnvironment')}</option>
              <option value="workspace_secret">
                {t('targetWorkspaceSecret')}
              </option>
              <option value="bot_secret">{t('targetBotSecret')}</option>
              <option value="user">{t('targetUser')}</option>
              <option value="workspace">{t('targetWorkspace')}</option>
            </select>
          </div>

          <div className="flex items-end gap-2">
            <Button type="submit" variant="primary">
              {t('applyFilters')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              onClick={handleResetFilters}
            >
              {t('resetFilters')}
            </Button>
          </div>
        </form>
      </Card>

      {isLoading ? (
        <Card>
          <p className="text-[14px] text-ink-2">{t('loading')}</p>
        </Card>
      ) : loadFailed ? (
        <Card>
          <p
            role="alert"
            className="rounded-sm border border-amber-line bg-amber-tint px-3 py-2 text-[14px] text-amber"
          >
            {t('loadError')}
          </p>
        </Card>
      ) : entries.length === 0 ? (
        <Card>
          <p className="text-[14px] text-ink-2">{t('empty')}</p>
        </Card>
      ) : (
        <div className="overflow-hidden rounded-lg border border-line bg-terminal text-on-dark shadow-xs">
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left font-mono text-[12px]">
              <thead>
                <tr className="border-b border-on-dark/15 text-[11px] uppercase tracking-[0.06em] text-on-dark/70">
                  <th scope="col" className="px-4 py-3 font-bold">
                    {t('colTimestamp')}
                  </th>
                  <th scope="col" className="px-4 py-3 font-bold">
                    {t('colActor')}
                  </th>
                  <th scope="col" className="px-4 py-3 font-bold">
                    {t('colAction')}
                  </th>
                  <th scope="col" className="px-4 py-3 font-bold">
                    {t('colTarget')}
                  </th>
                  <th scope="col" className="px-4 py-3 font-bold">
                    {t('colMetadata')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {entries.map((entry) => (
                  <tr
                    key={entry.id}
                    className="border-b border-on-dark/10 align-top last:border-b-0"
                  >
                    <td className="whitespace-nowrap px-4 py-2.5 text-on-dark/80">
                      {new Date(entry.createdAt).toISOString()}
                    </td>
                    <td className="px-4 py-2.5 text-on-dark">
                      {formatActor(entry, t('systemActor'))}
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5">
                      <span className="inline-flex rounded-sm border border-on-dark/20 bg-on-dark/10 px-2 py-0.5 text-mint-soft">
                        {entry.action}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-on-dark/80">
                      {formatTarget(entry)}
                    </td>
                    <td className="max-w-[340px] break-all px-4 py-2.5 text-on-dark/75">
                      {formatMetadata(entry.metadata)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-on-dark/15 bg-terminal px-4 py-3">
            <span className="font-mono text-[12px] text-on-dark/75">
              {t('pageStatus', { from, to, total })}
            </span>
            <div className="flex items-center gap-2">
              <button
                type="button"
                disabled={!hasPrev}
                onClick={() =>
                  setOffset((prev) => Math.max(0, prev - PAGE_LIMIT))
                }
                className="min-h-[44px] rounded-sm border border-on-dark/25 px-3 text-[13px] font-bold text-on-dark transition hover:bg-on-dark/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-mint-soft disabled:cursor-not-allowed disabled:opacity-40"
              >
                {t('previousPage')}
              </button>
              <button
                type="button"
                disabled={!hasNext}
                onClick={() => setOffset((prev) => prev + PAGE_LIMIT)}
                className="min-h-[44px] rounded-sm border border-on-dark/25 px-3 text-[13px] font-bold text-on-dark transition hover:bg-on-dark/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-mint-soft disabled:cursor-not-allowed disabled:opacity-40"
              >
                {t('nextPage')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
