'use client';

import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations, useFormatter } from 'next-intl';
import { Alert, BudgetMeter, Button, Card, Input } from '@/components/ui';
import { ApiClientError, requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type Summary = components['schemas']['BudgetSummary'];
type BudgetState = components['schemas']['BudgetState'];
type BudgetPeriod = BudgetState['period'];

const BOT_PERIODS: readonly BudgetPeriod[] = ['5h', 'day', 'week', 'month'];

const PERIOD_META: Record<
  BudgetPeriod,
  {
    capLabelKey:
      | 'fiveHourCapLabel'
      | 'dailyCapLabel'
      | 'weeklyCapLabel'
      | 'monthlyCapLabel';
    inputLabelKey:
      'fiveHourLimit' | 'dailyLimit' | 'weeklyLimit' | 'monthlyLimit';
    cadenceKey:
      'fiveHourCadence' | 'dailyCadence' | 'weeklyCadence' | 'monthlyCadence';
    filterLabelKey: 'fiveHour' | 'daily' | 'weekly' | 'monthly';
  }
> = {
  '5h': {
    capLabelKey: 'fiveHourCapLabel',
    inputLabelKey: 'fiveHourLimit',
    cadenceKey: 'fiveHourCadence',
    filterLabelKey: 'fiveHour',
  },
  day: {
    capLabelKey: 'dailyCapLabel',
    inputLabelKey: 'dailyLimit',
    cadenceKey: 'dailyCadence',
    filterLabelKey: 'daily',
  },
  week: {
    capLabelKey: 'weeklyCapLabel',
    inputLabelKey: 'weeklyLimit',
    cadenceKey: 'weeklyCadence',
    filterLabelKey: 'weekly',
  },
  month: {
    capLabelKey: 'monthlyCapLabel',
    inputLabelKey: 'monthlyLimit',
    cadenceKey: 'monthlyCadence',
    filterLabelKey: 'monthly',
  },
};

function formatMicrosInput(limitMicros: number): string {
  const usd = limitMicros / 1_000_000;
  return Number.isInteger(usd)
    ? String(usd)
    : usd.toFixed(6).replace(/0+$/, '').replace(/\.$/, '');
}

/** Shows metered workspace or bot spend with 80%/100% threshold markers and lets administrators set limits. */
export function BudgetPanel({
  workspaceId,
  botId,
  canManage = false,
  ignoreMissing = false,
}: {
  workspaceId: string;
  botId?: string;
  canManage?: boolean;
  ignoreMissing?: boolean;
}) {
  const t = useTranslations('budgets');
  const format = useFormatter();
  const client = useQueryClient();
  const [limit, setLimit] = useState('');
  const [drafts, setDrafts] = useState<Partial<Record<BudgetPeriod, string>>>(
    {},
  );
  const [saving, setSaving] = useState(false);
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [usagePeriod, setUsagePeriod] = useState<BudgetPeriod>('month');
  const [error, setError] = useState(false);
  const base = `/api/workspaces/${workspaceId}`;
  const query = useQuery({
    queryKey: ['budget', workspaceId, botId],
    retry: false,
    queryFn: async () => {
      try {
        return await requestJson<Summary>(
          `${base}/budgets${botId ? `?botId=${botId}` : ''}`,
        );
      } catch (err) {
        if (
          ignoreMissing &&
          err instanceof ApiClientError &&
          err.status === 404
        ) {
          return null;
        }
        throw err;
      }
    },
  });

  const budgets = query.data?.budgets ?? [];
  // ListBudgetState returns workspace-wide monthly caps alongside bot-specific caps.
  const workspaceCap = botId
    ? budgets.find((b) => !b.botId && b.period === 'month')
    : undefined;
  const displayedBudgets = (
    botId ? budgets.filter((b) => b !== workspaceCap) : budgets
  )
    .slice()
    .sort(
      (a, b) => BOT_PERIODS.indexOf(a.period) - BOT_PERIODS.indexOf(b.period),
    );

  const botBudgetByPeriod = new Map<BudgetPeriod, BudgetState>();
  for (const b of displayedBudgets) {
    botBudgetByPeriod.set(b.period, b);
  }

  function serverValueFor(period: BudgetPeriod): string {
    const existing = botBudgetByPeriod.get(period);
    return existing ? formatMicrosInput(existing.limitMicros) : '';
  }

  function inputValueFor(period: BudgetPeriod): string {
    return drafts[period] !== undefined
      ? drafts[period]
      : serverValueFor(period);
  }

  const isBotFormDirty = BOT_PERIODS.some((period) => {
    const draft = drafts[period];
    return draft !== undefined && draft.trim() !== serverValueFor(period);
  });

  async function saveWorkspaceBudget(event: FormEvent) {
    event.preventDefault();
    setError(false);
    setSaving(true);
    try {
      await requestJson(`${base}/budgets`, {
        method: 'PUT',
        body: {
          period: 'month',
          limitMicros: Math.round(Number(limit) * 1_000_000),
        },
      });
      await client.invalidateQueries({ queryKey: ['budget', workspaceId] });
      setLimit('');
    } catch {
      setError(true);
    } finally {
      setSaving(false);
    }
  }

  async function saveBotLimits(event: FormEvent) {
    event.preventDefault();
    if (!botId) {
      return;
    }
    setError(false);
    setSaving(true);
    try {
      for (const period of BOT_PERIODS) {
        const existing = botBudgetByPeriod.get(period);
        const raw = inputValueFor(period).trim();
        if (raw === serverValueFor(period)) {
          continue;
        }
        if (raw === '') {
          if (existing) {
            await requestJson(
              `${base}/budgets?botId=${encodeURIComponent(botId)}&period=${encodeURIComponent(period)}`,
              { method: 'DELETE' },
            );
          }
          continue;
        }
        const limitMicros = Math.round(Number(raw) * 1_000_000);
        if (
          !Number.isFinite(limitMicros) ||
          limitMicros < 1 ||
          limitMicros > 1e12
        ) {
          setError(true);
          return;
        }
        await requestJson(`${base}/budgets`, {
          method: 'PUT',
          body: {
            botId,
            period,
            limitMicros,
          },
        });
      }
      setDrafts({});
      await client.invalidateQueries({ queryKey: ['budget', workspaceId] });
    } catch {
      setError(true);
    } finally {
      setSaving(false);
    }
  }

  async function setSubscriptionPolicy(enabled: boolean) {
    setError(false);
    setSavingPolicy(true);
    try {
      await requestJson(`${base}/subscription-budget-policy`, {
        method: 'PUT',
        body: { enabled },
      });
      await client.invalidateQueries({ queryKey: ['budget', workspaceId] });
    } catch {
      setError(true);
    } finally {
      setSavingPolicy(false);
    }
  }

  const anyExceeded = budgets.some((b) => b.exceeded);
  const anyWarning = !anyExceeded && budgets.some((b) => b.warning);

  const usage = (query.data?.subscriptionUsage ?? []).filter(
    (row) => row.period === usagePeriod,
  );
  const internalCharge = usage.reduce(
    (sum, row) => sum + row.internalChargeMicros,
    0,
  );

  return (
    <Card className="flex flex-col gap-5 p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-[20px] font-black text-ink">
            {t(botId ? 'botTitle' : 'title')}
          </h2>
          {query.data ? (
            <p className="mt-0.5 text-[14px] text-ink-2">
              {t('today', {
                amount: format.number(query.data.spendTodayMicros / 1_000_000, {
                  style: 'currency',
                  currency: 'USD',
                }),
              })}
            </p>
          ) : null}
        </div>
        {botId && query.data ? (
          anyExceeded ? (
            <span className="rounded-pill border border-amber-line bg-amber-tint px-2.5 py-0.5 font-mono text-[12px] font-bold text-amber">
              {t('hardCapCode')}
            </span>
          ) : anyWarning ? (
            <span className="rounded-pill border border-amber-line bg-amber-tint px-2.5 py-0.5 font-mono text-[12px] font-bold text-amber">
              {t('warningBadge')}
            </span>
          ) : displayedBudgets.length > 0 ? (
            <span className="rounded-pill border border-mint-line bg-mint-soft px-2.5 py-0.5 text-[12px] font-bold text-green-600">
              {t('withinBudget')}
            </span>
          ) : (
            <span className="rounded-pill border border-line bg-paper-2 px-2.5 py-0.5 text-[12px] font-bold text-muted">
              {t('noLimitSet')}
            </span>
          )
        ) : null}
      </div>

      {error || query.isError ? (
        <Alert tone="warning">{t('error')}</Alert>
      ) : null}

      {workspaceCap ? (
        <div
          className={`flex flex-wrap items-center justify-between gap-2 rounded-sm border px-3.5 py-2 text-[13px] ${
            workspaceCap.exceeded
              ? 'border-dashed border-amber-line bg-amber-tint text-amber'
              : workspaceCap.warning
                ? 'border-amber-line bg-amber-tint/70 text-amber'
                : 'border-line bg-paper-2 text-ink-2'
          }`}
        >
          <span className="font-mono font-bold">
            {t('workspaceCapContext', {
              spent: format.number(workspaceCap.spentMicros / 1_000_000, {
                style: 'currency',
                currency: 'USD',
              }),
              limit: format.number(workspaceCap.limitMicros / 1_000_000, {
                style: 'currency',
                currency: 'USD',
              }),
              percent:
                workspaceCap.limitMicros > 0
                  ? Math.round(
                      (workspaceCap.spentMicros / workspaceCap.limitMicros) *
                        100,
                    )
                  : 0,
            })}
          </span>
        </div>
      ) : null}

      {canManage &&
      !botId &&
      query.data?.countSubscriptionUsage !== undefined ? (
        <label className="flex min-h-11 items-start gap-3 rounded-md border border-line p-3 text-[14px] text-ink">
          <input
            type="checkbox"
            className="mt-1"
            checked={query.data.countSubscriptionUsage}
            disabled={savingPolicy}
            onChange={(event) =>
              void setSubscriptionPolicy(event.target.checked)
            }
          />
          <span>
            <span className="block font-bold">{t('subscriptionPolicy')}</span>
            {t('subscriptionPolicyHelp')}
          </span>
        </label>
      ) : null}

      {query.data?.subscriptionUsage !== undefined ? (
        <section className="flex flex-col gap-2 border-t border-line pt-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="font-bold text-ink">{t('subscriptionUsage')}</h3>
            <select
              aria-label={t('usagePeriod')}
              className="min-h-[44px] rounded-sm border border-line-strong bg-paper px-3 text-ink"
              value={usagePeriod}
              onChange={(event) =>
                setUsagePeriod(event.target.value as BudgetPeriod)
              }
            >
              {BOT_PERIODS.map((p) => (
                <option key={p} value={p}>
                  {t(PERIOD_META[p].filterLabelKey)}
                </option>
              ))}
            </select>
          </div>
          {usage.length ? (
            <>
              <p className="text-[14px] text-ink-2">
                {t('internalCharge', {
                  amount: format.number(internalCharge / 1_000_000, {
                    style: 'currency',
                    currency: 'USD',
                  }),
                })}
              </p>
              <ul className="flex flex-col gap-2">
                {usage.map((row) => (
                  <li
                    key={row.model}
                    className="rounded-sm border border-line px-3 py-2 text-[14px] text-ink-2"
                  >
                    <span className="block break-all font-bold text-ink">
                      {row.model}
                    </span>
                    <span>
                      {t('tokenTotals', {
                        input: row.inputTokens,
                        output: row.outputTokens,
                        cache: row.cacheTokens,
                      })}
                    </span>
                    {row.unknownRequests > 0 ? (
                      <span className="block text-ink-2">
                        {t('usageUnknown', { count: row.unknownRequests })}
                      </span>
                    ) : null}
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className="text-[14px] text-ink-2">{t('usageEmpty')}</p>
          )}
        </section>
      ) : null}

      {query.data && displayedBudgets.length > 0 ? (
        <div className="grid gap-4 md:grid-cols-2">
          {displayedBudgets.map((b) => {
            const spentText = format.number(b.spentMicros / 1_000_000, {
              style: 'currency',
              currency: 'USD',
            });
            const limitText = format.number(b.limitMicros / 1_000_000, {
              style: 'currency',
              currency: 'USD',
            });
            const percent =
              b.limitMicros > 0
                ? Math.round((b.spentMicros / b.limitMicros) * 100)
                : 0;
            const meta = PERIOD_META[b.period];
            const label = t(b.period, {
              spent: spentText,
              limit: limitText,
            });
            return (
              <div
                key={b.id}
                className={`flex flex-col gap-3 rounded-md border p-4 ${
                  b.exceeded
                    ? 'border-dashed border-amber-line bg-amber-tint'
                    : b.warning
                      ? 'border-amber-line bg-amber-tint/70'
                      : 'border-line bg-paper-2'
                }`}
              >
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                    {t(meta.capLabelKey)}
                  </span>
                  <span className="font-mono text-[14px] font-black text-ink">
                    {t('ofLimit', { spent: spentText, limit: limitText })}
                  </span>
                </div>
                <p className="sr-only">{label}</p>
                <BudgetMeter
                  ariaLabel={label}
                  spentMicros={b.spentMicros}
                  limitMicros={b.limitMicros}
                  warning={b.warning}
                  exceeded={b.exceeded}
                  leftCaption={t('used', { percent })}
                  rightCaption={t('threshold')}
                />
                <p className="text-[12px] text-muted">{t(meta.cadenceKey)}</p>
                {b.exceeded ? (
                  <p className="text-[13px] font-bold text-amber">
                    {t('exceeded')}
                  </p>
                ) : b.warning ? (
                  <p className="text-[13px] font-bold text-amber">
                    {t('warning')}
                  </p>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : null}

      {botId ? (
        anyExceeded ? (
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-dashed border-amber-line bg-amber-tint px-4 py-3 text-[13px] text-amber">
            <div>
              <p className="font-bold">{t('hardCapTitle')}</p>
              <p className="text-ink-2">{t('hardCap')}</p>
            </div>
            <span className="rounded-xs border border-amber-line bg-paper px-2 py-0.5 font-mono text-[12px] font-bold text-amber">
              {t('hardCapCode')}
            </span>
          </div>
        ) : (
          <p className="rounded-sm border border-line bg-paper-2 px-3.5 py-2.5 text-[13px] text-ink-2">
            {t('hardCap')}
          </p>
        )
      ) : null}

      {canManage ? (
        botId ? (
          <form
            onSubmit={(event) => void saveBotLimits(event)}
            className="flex flex-col gap-4 rounded-md border border-line bg-paper-2 p-4"
          >
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {BOT_PERIODS.map((p) => {
                const meta = PERIOD_META[p];
                return (
                  <Input
                    key={p}
                    label={t(meta.inputLabelKey)}
                    helperText={t(meta.cadenceKey)}
                    type="number"
                    min="0.000001"
                    max="1000000"
                    step="0.000001"
                    mono
                    placeholder={t('unlimitedPlaceholder')}
                    value={inputValueFor(p)}
                    onChange={(event) =>
                      setDrafts((prev) => ({
                        ...prev,
                        [p]: event.target.value,
                      }))
                    }
                  />
                );
              })}
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line pt-3">
              <p className="text-[13px] text-muted">{t('botLimitsHint')}</p>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  variant="secondary"
                  disabled={saving || !isBotFormDirty}
                  onClick={() => setDrafts({})}
                >
                  {t('resetChanges')}
                </Button>
                <Button type="submit" disabled={saving || !isBotFormDirty}>
                  {t('saveBotLimits')}
                </Button>
              </div>
            </div>
          </form>
        ) : (
          <form
            onSubmit={(event) => void saveWorkspaceBudget(event)}
            className="grid items-end gap-3 border-t border-line pt-4 sm:grid-cols-[1fr_180px_auto]"
          >
            <Input
              label={t('limit')}
              type="number"
              min="0.000001"
              max="1000000"
              step="0.000001"
              mono
              value={limit}
              required
              onChange={(event) => setLimit(event.target.value)}
            />
            <div className="flex flex-col gap-1.5">
              <label
                htmlFor="budget-period-workspace"
                className="text-[14px] font-bold text-ink"
              >
                {t('period')}
              </label>
              <select
                id="budget-period-workspace"
                className="min-h-[44px] rounded-sm border-[1.5px] border-line-strong bg-paper px-3 py-2 text-[15px] text-ink focus:border-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                value="month"
                disabled
              >
                <option value="month">{t('monthly')}</option>
              </select>
            </div>
            <Button type="submit" disabled={saving || !limit}>
              {t('save')}
            </Button>
          </form>
        )
      ) : null}
    </Card>
  );
}
