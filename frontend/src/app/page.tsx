'use client';

import {
  ArrowRight,
  Bell,
  Box,
  Play,
  Plus,
  Search,
  SlidersHorizontal,
  Square,
} from 'lucide-react';
import Link from 'next/link';
import { useEffect, useMemo, useState } from 'react';
import { useFormatter, useTranslations } from 'next-intl';

import { AppShell } from '@/components/shell/AppShell';
import {
  Alert,
  BotAvatar,
  BudgetMeter,
  Button,
  Card,
  type ChipStatus,
  Eyebrow,
  SegmentedControl,
  StatusChip,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import {
  listBots,
  requestJson,
  startBotEnvironment,
  stopBotEnvironment,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canCreateBot, canManageBot } from '@/lib/rbac';
import type { Bot, BotStatus } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type BudgetSummary = components['schemas']['BudgetSummary'];
type ApprovalList = components['schemas']['ApprovalList'];
type NotificationList = components['schemas']['NotificationList'];
type StatusFilter = 'all' | 'running' | 'idle' | 'stopped';

function mapBotStatusToChip(status: BotStatus): ChipStatus {
  if (status === 'action') {
    return 'working';
  }
  return status;
}

function FleetContent() {
  const t = useTranslations('fleet');
  const tTeam = useTranslations('team');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');
  const format = useFormatter();
  const { user, activeWorkspace } = useAuth();

  const [bots, setBots] = useState<Bot[]>([]);
  const [budget, setBudget] = useState<BudgetSummary | null>(null);
  const [pendingApprovals, setPendingApprovals] = useState<number | null>(null);
  const [notifications, setNotifications] = useState<
    NotificationList['notifications']
  >([]);
  const [loading, setLoading] = useState(true);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [sandboxBusyId, setSandboxBusyId] = useState<string | null>(null);

  useEffect(() => {
    if (!activeWorkspace) {
      setBots([]);
      setBudget(null);
      setPendingApprovals(null);
      setNotifications([]);
      setLoading(false);
      return;
    }

    let cancelled = false;
    setLoading(true);
    setErrorKey(null);
    const base = `/api/workspaces/${encodeURIComponent(activeWorkspace.id)}`;

    void (async () => {
      try {
        const [res, budgetRes, approvalsRes, notesRes] = await Promise.all([
          listBots(activeWorkspace.id),
          requestJson<BudgetSummary>(`${base}/budgets`).catch(() => null),
          requestJson<ApprovalList>(`${base}/approvals`).catch(() => null),
          requestJson<NotificationList>(`${base}/notifications`).catch(
            () => null,
          ),
        ]);
        if (!cancelled) {
          setBots(res.bots);
          setBudget(budgetRes);
          setPendingApprovals(
            approvalsRes
              ? approvalsRes.approvals.filter((a) => a.status === 'pending')
                  .length
              : null,
          );
          setNotifications(notesRes?.notifications ?? []);
        }
      } catch (err) {
        if (!cancelled) {
          setErrorKey(resolveErrorKey(err));
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [activeWorkspace]);

  const statusCounts = useMemo(() => {
    let running = 0;
    let idle = 0;
    let stopped = 0;
    let docker = 0;
    let kubernetes = 0;
    for (const bot of bots) {
      if (bot.status === 'action' || bot.status === 'thinking') {
        running += 1;
      } else if (bot.status === 'idle') {
        idle += 1;
      } else {
        stopped += 1;
      }
      if (bot.driverType === 'kubernetes') {
        kubernetes += 1;
      } else {
        docker += 1;
      }
    }
    return {
      all: bots.length,
      running,
      idle,
      stopped,
      active: running + idle,
      docker,
      kubernetes,
    };
  }, [bots]);

  const filteredBots = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    return bots.filter((bot) => {
      if (
        statusFilter === 'running' &&
        bot.status !== 'action' &&
        bot.status !== 'thinking'
      ) {
        return false;
      }
      if (statusFilter === 'idle' && bot.status !== 'idle') {
        return false;
      }
      if (statusFilter === 'stopped' && bot.status !== 'stopped') {
        return false;
      }
      if (!q) {
        return true;
      }
      return (
        bot.name.toLowerCase().includes(q) ||
        bot.description.toLowerCase().includes(q) ||
        bot.llmModel.toLowerCase().includes(q) ||
        bot.llmProvider.toLowerCase().includes(q) ||
        bot.driverType.toLowerCase().includes(q) ||
        bot.avatarSeed.toLowerCase().includes(q)
      );
    });
  }, [bots, searchQuery, statusFilter]);

  const canCreate =
    activeWorkspace !== null && canCreateBot(activeWorkspace.role);

  const dailyBudget = budget?.budgets.find((b) => b.period === 'day') ?? null;
  const spendTodayText = format.number(
    (budget?.spendTodayMicros ?? 0) / 1000000,
    { style: 'currency', currency: 'USD' },
  );
  const dailyLimitText = dailyBudget
    ? format.number(dailyBudget.limitMicros / 1000000, {
        style: 'currency',
        currency: 'USD',
      })
    : null;

  async function handleSandboxToggle(bot: Bot) {
    if (
      !activeWorkspace ||
      !canManageBot(activeWorkspace.role, user?.id, bot)
    ) {
      return;
    }
    setErrorKey(null);
    setSandboxBusyId(bot.id);
    const isRunning = bot.status !== 'stopped';
    try {
      const updated = isRunning
        ? await stopBotEnvironment(activeWorkspace.id, bot.id)
        : await startBotEnvironment(activeWorkspace.id, bot.id);
      const nextRunning = updated.status === 'running';
      setBots((prev) =>
        prev.map((item) =>
          item.id === bot.id
            ? {
                ...item,
                status: nextRunning ? 'idle' : 'stopped',
                mood: nextRunning ? 'idle' : 'offline',
              }
            : item,
        ),
      );
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setSandboxBusyId(null);
    }
  }

  return (
    <div className="flex flex-col gap-7">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex max-w-2xl flex-col gap-1.5">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h1 className="text-[36px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[44px]">
            {t('title')}
          </h1>
          <p className="text-[16px] leading-[1.6] text-ink-2">{t('lead')}</p>
        </div>

        <div className="flex flex-wrap items-center gap-2.5">
          <Link
            href="/team/approvals"
            className="inline-flex min-h-[44px] items-center gap-2 rounded-sm border-[1.5px] border-line bg-paper px-4 text-[14px] font-bold text-green-600 transition duration-150 hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <Bell className="h-4 w-4" aria-hidden="true" />
            <span>{t('approvalsCta')}</span>
          </Link>
          {canCreate ? (
            <Link
              href="/bots/new"
              className="inline-flex min-h-[44px] shrink-0 items-center justify-center gap-2 rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper shadow-card transition duration-150 hover:-translate-y-px hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              <span>{t('createCta')}</span>
            </Link>
          ) : null}
        </div>
      </div>

      <section
        aria-label={t('eyebrow')}
        className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4"
      >
        <Card className="flex flex-col justify-between p-4">
          <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
            {t('statSandboxes')}
          </p>
          <p className="mt-1 text-[28px] font-black leading-[1.2] text-ink">
            {t('statSandboxesValue', {
              active: statusCounts.active,
              total: statusCounts.all,
            })}
          </p>
          <p className="mt-2 text-[13px] text-muted">
            {t('statSandboxesDetail', {
              busy: statusCounts.running,
              idle: statusCounts.idle,
              stopped: statusCounts.stopped,
            })}
          </p>
        </Card>

        <Card
          className={`flex flex-col justify-between p-4 ${
            dailyBudget?.exceeded
              ? 'border-dashed border-amber-line bg-amber-tint/50'
              : dailyBudget?.warning
                ? 'border-amber-line bg-amber-tint/30'
                : ''
          }`}
        >
          <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
            {t('statSpendToday')}
          </p>
          <p className="mt-1 text-[28px] font-black leading-[1.2] text-ink">
            {spendTodayText}{' '}
            {dailyLimitText ? (
              <span className="text-[15px] font-bold text-muted">
                {t('statSpendOfLimit', { limit: dailyLimitText })}
              </span>
            ) : null}
          </p>
          {dailyBudget ? (
            <div className="mt-2.5">
              <BudgetMeter
                ariaLabel={t('statSpendToday')}
                spentMicros={dailyBudget.spentMicros}
                limitMicros={dailyBudget.limitMicros}
                warning={dailyBudget.warning}
                exceeded={dailyBudget.exceeded}
              />
            </div>
          ) : (
            <p className="mt-2 text-[13px] text-muted">{t('statSpendNoCap')}</p>
          )}
          <div className="mt-2">
            <Link
              href="/settings/members#budgets"
              className="text-[13px] font-bold text-green-600 hover:underline"
            >
              {t('budgetsCta')}
            </Link>
          </div>
        </Card>

        <Card className="flex flex-col justify-between p-4">
          <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
            {t('statApprovals')}
          </p>
          <p
            className={`mt-1 text-[28px] font-black leading-[1.2] ${
              pendingApprovals && pendingApprovals > 0
                ? 'text-amber'
                : 'text-ink'
            }`}
          >
            {pendingApprovals ?? '—'}
          </p>
          <p className="mt-2 text-[13px] text-muted">
            {pendingApprovals && pendingApprovals > 0
              ? t('statApprovalsDetail')
              : t('statApprovalsClear')}
          </p>
        </Card>

        <Card className="flex flex-col justify-between p-4">
          <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
            {t('statDrivers')}
          </p>
          <p className="mt-1 text-[28px] font-black leading-[1.2] text-ink">
            {statusCounts.all}
          </p>
          <p className="mt-2 font-mono text-[13px] text-muted">
            {t('statDriversDetail', {
              docker: statusCounts.docker,
              kubernetes: statusCounts.kubernetes,
            })}
          </p>
        </Card>
      </section>

      {activeWorkspace?.role === 'viewer' ? (
        <Alert tone="info">{t('viewerReadonly')}</Alert>
      ) : null}

      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      <div className="flex flex-wrap items-start gap-7">
        <section className="flex min-w-0 flex-[999_1_620px] flex-col gap-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <SegmentedControl<StatusFilter>
              ariaLabel={t('filterAria')}
              value={statusFilter}
              onChange={setStatusFilter}
              size="sm"
              options={[
                {
                  value: 'all',
                  label: t('filterAll'),
                  badge: statusCounts.all,
                },
                {
                  value: 'running',
                  label: t('filterRunning'),
                  badge: statusCounts.running,
                },
                {
                  value: 'idle',
                  label: t('filterIdle'),
                  badge: statusCounts.idle,
                },
                {
                  value: 'stopped',
                  label: t('filterStopped'),
                  badge: statusCounts.stopped,
                },
              ]}
            />

            <div
              role="search"
              className="flex min-h-[44px] w-full items-center gap-2 rounded-sm border-[1.5px] border-line bg-paper px-3 sm:w-72"
            >
              <Search
                className="h-4 w-4 shrink-0 text-muted"
                aria-hidden="true"
              />
              <label htmlFor="fleet-search" className="sr-only">
                {t('searchLabel')}
              </label>
              <input
                id="fleet-search"
                type="search"
                value={searchQuery}
                placeholder={t('searchPlaceholder')}
                onChange={(event) => setSearchQuery(event.target.value)}
                className="min-w-0 flex-1 border-0 bg-transparent text-[14px] text-ink outline-none placeholder:text-muted"
              />
            </div>
          </div>

          {loading ? (
            <Card className="p-8 text-center">
              <p role="status" className="text-[15px] font-bold text-ink-2">
                {tActions('loading')}
              </p>
            </Card>
          ) : filteredBots.length === 0 ? (
            <Card className="flex flex-col items-start gap-3 p-8">
              <h2 className="text-[22px] font-black text-ink">
                {bots.length === 0 ? t('emptyTitle') : t('emptyFiltered')}
              </h2>
              {bots.length === 0 ? (
                <p className="max-w-xl text-[15px] leading-[1.6] text-ink-2">
                  {t('emptyBody')}
                </p>
              ) : null}
              {bots.length === 0 && canCreate ? (
                <Link
                  href="/bots/new"
                  className="mt-2 inline-flex min-h-[44px] items-center rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper transition duration-150 hover:bg-dark"
                >
                  {t('createCta')}
                </Link>
              ) : null}
            </Card>
          ) : (
            <div className="grid grid-cols-[repeat(auto-fill,minmax(290px,1fr))] gap-4">
              {filteredBots.map((bot) => {
                const canManage = canManageBot(
                  activeWorkspace?.role,
                  user?.id,
                  bot,
                );
                const isRunning = bot.status !== 'stopped';
                const isBusy = sandboxBusyId === bot.id;

                return (
                  <Card
                    key={bot.id}
                    interactive
                    className="flex h-full flex-col gap-3.5 p-5"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <Link
                        href={`/bots/${bot.id}`}
                        className="group flex min-w-0 items-center gap-3.5 rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                      >
                        <BotAvatar
                          seed={bot.avatarSeed}
                          mood={bot.mood}
                          size={60}
                          tile
                          decorative
                        />
                        <div className="flex min-w-0 flex-col gap-1">
                          <h2 className="truncate text-[20px] font-black leading-[1.2] text-ink transition duration-150 group-hover:text-green-600">
                            {bot.name}
                          </h2>
                          <StatusChip
                            status={mapBotStatusToChip(bot.status)}
                            label={t(`status.${bot.status}`)}
                            className="w-fit"
                          />
                        </div>
                      </Link>

                      {canManage ? (
                        <Link
                          href={`/bots/${bot.id}/settings`}
                          aria-label={t('editAction')}
                          title={t('editAction')}
                          className="grid h-10 w-10 shrink-0 place-items-center rounded-sm text-muted transition duration-150 hover:bg-mint-soft hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                        >
                          <SlidersHorizontal
                            className="h-4 w-4"
                            aria-hidden="true"
                          />
                        </Link>
                      ) : null}
                    </div>

                    {bot.description ? (
                      <p className="line-clamp-2 text-[14px] leading-[1.55] text-ink-2">
                        {bot.description}
                      </p>
                    ) : null}

                    <div className="rounded-sm bg-terminal px-3 py-2 font-mono text-[12px] text-on-dark">
                      <span className="text-green-light">$ </span>
                      <span className="truncate">
                        {`${bot.llmMode} · ${t(`status.${bot.status}`).toLowerCase()}`}
                      </span>
                    </div>

                    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-[13px]">
                      <dt className="text-muted">{t('brainLabel')}</dt>
                      <dd className="truncate font-mono text-ink">
                        {`${bot.llmProvider} · ${bot.llmModel}`}
                      </dd>
                      <dt className="text-muted">{t('computerLabel')}</dt>
                      <dd className="truncate font-mono text-ink">
                        {`${bot.driverType} · ${bot.avatarSeed}`}
                      </dd>
                    </dl>

                    <div className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
                      {canManage ? (
                        <>
                          <Button
                            variant="secondary"
                            disabled={isBusy}
                            onClick={() => void handleSandboxToggle(bot)}
                            className="min-h-[40px] px-3 text-[13px]"
                          >
                            {isRunning ? (
                              <Square
                                className="h-3.5 w-3.5"
                                aria-hidden="true"
                              />
                            ) : (
                              <Play
                                className="h-3.5 w-3.5"
                                aria-hidden="true"
                              />
                            )}
                            <span>
                              {isRunning
                                ? isBusy
                                  ? t('stoppingSandbox')
                                  : t('stopSandbox')
                                : isBusy
                                  ? t('startingSandbox')
                                  : t('startSandbox')}
                            </span>
                          </Button>
                          <Link
                            href={`/bots/${bot.id}`}
                            className="group inline-flex min-h-[44px] items-center gap-1.5 rounded-sm bg-green-600 px-4 text-[14px] font-bold text-paper transition duration-150 hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                          >
                            <span>{t('openWorkspace')}</span>
                            <ArrowRight
                              className="h-4 w-4 transition-transform duration-150 group-hover:translate-x-[3px]"
                              aria-hidden="true"
                            />
                          </Link>
                        </>
                      ) : (
                        <Link
                          href={`/bots/${bot.id}`}
                          className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm px-2.5 text-[14px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                        >
                          <span>{t('inspectAction')}</span>
                          <ArrowRight className="h-4 w-4" aria-hidden="true" />
                        </Link>
                      )}
                    </div>
                  </Card>
                );
              })}

              {canCreate ? (
                <Link
                  href="/bots/new"
                  className="flex min-h-[240px] flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed border-line-strong bg-paper/60 p-6 text-center transition duration-150 hover:border-green hover:bg-mint-soft/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                >
                  <span className="grid h-10 w-10 place-items-center rounded-pill bg-mint-soft text-green-600">
                    <Plus className="h-5 w-5" aria-hidden="true" />
                  </span>
                  <span className="text-[17px] font-black text-ink">
                    {t('newBotCardTitle')}
                  </span>
                  <span className="max-w-[220px] text-[13px] text-muted">
                    {t('newBotCardBody')}
                  </span>
                </Link>
              ) : null}
            </div>
          )}
        </section>

        <aside className="flex w-full flex-col gap-4 lg:w-[300px] lg:shrink-0">
          <Card className="flex flex-col gap-3 p-5">
            <h2 className="text-[18px] font-black text-ink">
              {t('asideActivityTitle')}
            </h2>
            {notifications.length === 0 ? (
              <p className="text-[14px] text-muted">
                {t('asideActivityEmpty')}
              </p>
            ) : (
              <ul className="flex flex-col gap-2.5">
                {notifications.slice(0, 5).map((note) => (
                  <li
                    key={note.id}
                    className="flex items-start gap-2.5 border-t border-line pt-2.5 first:border-t-0 first:pt-0"
                  >
                    <span className="grid h-7 w-7 shrink-0 place-items-center rounded-xs bg-paper-2 text-ink-2">
                      <Bell className="h-3.5 w-3.5" aria-hidden="true" />
                    </span>
                    <div className="min-w-0">
                      <p className="text-[13px] font-bold text-ink">
                        {note.kind === 'approval'
                          ? tTeam('kindApproval')
                          : note.kind === 'budget'
                            ? tTeam('kindBudget')
                            : tTeam('kindMention')}
                      </p>
                      <p className="text-[12px] text-muted">
                        {new Date(note.createdAt).toLocaleTimeString([], {
                          hour: '2-digit',
                          minute: '2-digit',
                        })}
                      </p>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card className="flex flex-col gap-3 p-5">
            <h2 className="text-[18px] font-black text-ink">
              {t('asideDriversTitle')}
            </h2>
            <div className="flex flex-col gap-3">
              <div className="flex items-start gap-3">
                <span className="grid h-8 w-8 shrink-0 place-items-center rounded-sm bg-mint-soft text-green-600">
                  <Box className="h-4 w-4" aria-hidden="true" />
                </span>
                <div>
                  <p className="text-[14px] font-bold text-ink">
                    {t('asideDriversDocker')}
                  </p>
                  <p className="text-[13px] text-muted">
                    {statusCounts.docker > 0
                      ? t('asideDriversCount', { count: statusCounts.docker })
                      : t('asideDriversNone')}
                  </p>
                </div>
              </div>
              <div className="flex items-start gap-3 border-t border-line pt-3">
                <span className="grid h-8 w-8 shrink-0 place-items-center rounded-sm bg-blue-tint text-blue">
                  <Box className="h-4 w-4" aria-hidden="true" />
                </span>
                <div>
                  <p className="text-[14px] font-bold text-ink">
                    {t('asideDriversK8s')}
                  </p>
                  <p className="text-[13px] text-muted">
                    {statusCounts.kubernetes > 0
                      ? t('asideDriversCount', {
                          count: statusCounts.kubernetes,
                        })
                      : t('asideDriversNone')}
                  </p>
                </div>
              </div>
            </div>
          </Card>
        </aside>
      </div>
    </div>
  );
}

/**
 * Authenticated workspace Fleet dashboard listing bots with deterministic avatars, overview tiles, and filters.
 */
export default function FleetPage() {
  return (
    <AppShell>
      <FleetContent />
    </AppShell>
  );
}
