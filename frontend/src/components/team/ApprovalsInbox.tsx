'use client';

import { useState } from 'react';
import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { ApprovalCard } from '@/components/team/ApprovalCard';
import { canDecideApproval } from '@/components/team/approvals';
import {
  useApprovalDecision,
  useApprovalList,
} from '@/components/team/useApprovals';
import { Alert, Eyebrow } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { BotListResponse, WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type Group = components['schemas']['Group'];
type Approval = components['schemas']['Approval'];
type Filter = Approval['status'];

/**
 * Approvals inbox. The list is the same cache as the group transcript, so a
 * decision here updates an open card without a page reload.
 */
export function ApprovalsInbox({
  workspaceId,
  role,
}: {
  workspaceId: string;
  role: WorkspaceRole;
}) {
  const t = useTranslations('team');
  const list = useApprovalList(workspaceId);
  const { decide, busyId, error } = useApprovalDecision(workspaceId);
  const [filter, setFilter] = useState<Filter>('pending');
  const base = `/api/workspaces/${encodeURIComponent(workspaceId)}`;
  const groups = useQuery({
    queryKey: ['groups', workspaceId],
    queryFn: () => requestJson<Group[]>(`${base}/groups`),
  });
  const bots = useQuery({
    queryKey: ['bots', workspaceId],
    queryFn: () => requestJson<BotListResponse>(`${base}/bots`),
  });
  const rows = list.data?.approvals ?? [];
  const counts: Record<Filter, number> = {
    pending: rows.filter((row) => row.status === 'pending').length,
    approved: rows.filter((row) => row.status === 'approved').length,
    denied: rows.filter((row) => row.status === 'denied').length,
  };
  const visible = rows.filter((row) => row.status === filter);
  const emptyKey =
    filter === 'approved'
      ? 'emptyApproved'
      : filter === 'denied'
        ? 'emptyDenied'
        : 'emptyPending';
  const tabs: {
    status: Filter;
    label: 'tabPending' | 'tabApproved' | 'tabDenied';
  }[] = [
    { status: 'pending', label: 'tabPending' },
    { status: 'approved', label: 'tabApproved' },
    { status: 'denied', label: 'tabDenied' },
  ];

  const recentDecisions = rows
    .filter((row) => row.status === 'approved' || row.status === 'denied')
    .slice(0, 4);

  return (
    <div className="flex flex-col gap-5">
      <Link
        href="/team"
        className="inline-flex min-h-11 w-fit items-center gap-1.5 text-[14px] font-bold text-green-600 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
      >
        {t('backToTeam')}
      </Link>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_280px]">
        <section
          role="region"
          aria-labelledby="approvals-heading"
          className="flex min-w-0 flex-col gap-4"
        >
          <header className="flex flex-wrap items-end justify-between gap-3 border-b border-line pb-4">
            <div>
              <Eyebrow>{t('inboxEyebrow')}</Eyebrow>
              <h1
                id="approvals-heading"
                className="mt-1 text-[32px] font-black leading-[1.15] tracking-[-0.01em] text-ink"
              >
                {t('inboxTitle')}
              </h1>
              <p className="mt-1 text-[14px] text-ink-2">{t('inboxLead')}</p>
            </div>
            <div
              role="group"
              aria-label={t('filterStatus')}
              className="inline-flex gap-1 rounded-md border border-line bg-paper p-1"
            >
              {tabs.map((tab) => (
                <button
                  key={tab.status}
                  type="button"
                  aria-pressed={filter === tab.status}
                  className={`min-h-11 rounded-sm px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                    filter === tab.status
                      ? 'bg-dark text-paper'
                      : 'text-ink-2 hover:bg-paper-2 hover:text-ink'
                  }`}
                  onClick={() => setFilter(tab.status)}
                >
                  {t(tab.label, { count: counts[tab.status] })}
                </button>
              ))}
            </div>
          </header>
          {list.isError && !list.data ? (
            <Alert tone="warning">{t('inboxLoadFailed')}</Alert>
          ) : null}
          {list.data && visible.length === 0 ? (
            <p className="rounded-md border border-line bg-paper p-6 text-[15px] text-ink-2">
              {t(emptyKey)}
            </p>
          ) : null}
          <div className="flex flex-col gap-3">
            {visible.map((row) => {
              const bot = bots.data?.bots.find((item) => item.id === row.botId);
              const group = row.conversationId
                ? groups.data?.find((item) => item.id === row.conversationId)
                : undefined;
              return (
                <ApprovalCard
                  key={row.id}
                  approval={row}
                  botName={bot?.name || t('bot')}
                  where={
                    row.conversationId
                      ? group?.name || t('groupFallback')
                      : t('workspaceBudget')
                  }
                  avatarSeed={bot?.avatarSeed ?? row.botId}
                  canDecide={canDecideApproval(role, row)}
                  busy={busyId === row.id}
                  errorKey={error?.id === row.id ? error.key : null}
                  onDecide={(decision) => void decide(row.id, decision)}
                />
              );
            })}
          </div>
        </section>
        <aside aria-label={t('whoDecides')} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
              {t('whoDecides')}
            </h2>
            <div className="flex flex-col gap-2.5 rounded-lg border border-line bg-paper p-4 text-[13px] shadow-card">
              <p>
                <span className="font-black text-ink">{t('whoOwner')}</span>
                <span className="mt-0.5 block text-muted">
                  {t('whoOwnerDetail')}
                </span>
              </p>
              <p className="border-t border-line pt-2.5">
                <span className="font-black text-ink">{t('whoMember')}</span>
                <span className="mt-0.5 block text-muted">
                  {t('whoMemberDetail')}
                </span>
              </p>
              <p className="border-t border-line pt-2.5">
                <span className="font-black text-ink">{t('whoViewer')}</span>
                <span className="mt-0.5 block text-muted">
                  {t('whoViewerDetail')}
                </span>
              </p>
            </div>
          </div>

          {recentDecisions.length > 0 ? (
            <div className="flex flex-col gap-2">
              <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-muted">
                {t('recentDecisions')}
              </h2>
              <ul className="flex flex-col gap-2">
                {recentDecisions.map((item) => {
                  const bot = bots.data?.bots.find(
                    (row) => row.id === item.botId,
                  );
                  return (
                    <li
                      key={item.id}
                      className="rounded-md border border-line bg-paper p-3 text-[13px] shadow-card"
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span className="rounded-xs border border-line bg-paper-2 px-1.5 py-0.5 font-mono text-[11px] font-bold text-ink-2">
                          {item.kind}
                        </span>
                        <span
                          className={`text-[12px] font-bold ${
                            item.status === 'approved'
                              ? 'text-green-600'
                              : 'text-amber'
                          }`}
                        >
                          {item.status === 'approved'
                            ? t('statusApproved')
                            : t('statusDenied')}
                        </span>
                      </div>
                      <p className="mt-1.5 truncate font-bold text-ink">
                        {item.host || bot?.name || t('workspaceBudget')}
                      </p>
                    </li>
                  );
                })}
              </ul>
            </div>
          ) : null}
        </aside>
      </div>
    </div>
  );
}
