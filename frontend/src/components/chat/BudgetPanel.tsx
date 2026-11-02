'use client';

import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations, useFormatter } from 'next-intl';
import { Alert, BudgetMeter, Button, Card, Input } from '@/components/ui';
import { ApiClientError, requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type Summary = components['schemas']['BudgetSummary'];

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
  const [period, setPeriod] = useState<'day' | 'month'>(
    botId ? 'day' : 'month',
  );
  const [saving, setSaving] = useState(false);
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [usagePeriod, setUsagePeriod] = useState<'day' | 'month'>('month');
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

  async function save(event: FormEvent) {
    event.preventDefault();
    setError(false);
    setSaving(true);
    try {
      await requestJson(`${base}/budgets`, {
        method: 'PUT',
        body: {
          botId,
          period,
          limitMicros: Math.round(Number(limit) * 1000000),
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
                amount: format.number(query.data.spendTodayMicros / 1000000, {
                  style: 'currency',
                  currency: 'USD',
                }),
              })}
            </p>
          ) : null}
        </div>
        {botId ? (
          <span className="rounded-pill border border-amber-line bg-amber-tint px-2.5 py-0.5 font-mono text-[12px] font-bold text-amber">
            {t('hardCapCode')}
          </span>
        ) : null}
      </div>

      {error || query.isError ? (
        <Alert tone="warning">{t('error')}</Alert>
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
                setUsagePeriod(event.target.value as 'day' | 'month')
              }
            >
              <option value="day">{t('daily')}</option>
              <option value="month">{t('monthly')}</option>
            </select>
          </div>
          {usage.length ? (
            <>
              <p className="text-[14px] text-ink-2">
                {t('internalCharge', {
                  amount: format.number(internalCharge / 1000000, {
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

      {query.data && query.data.budgets.length > 0 ? (
        <div className="grid gap-4 md:grid-cols-2">
          {query.data.budgets.map((b) => {
            const spentText = format.number(b.spentMicros / 1000000, {
              style: 'currency',
              currency: 'USD',
            });
            const limitText = format.number(b.limitMicros / 1000000, {
              style: 'currency',
              currency: 'USD',
            });
            const percent =
              b.limitMicros > 0
                ? Math.round((b.spentMicros / b.limitMicros) * 100)
                : 0;
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
                    {b.period === 'day'
                      ? t('dailyCapLabel')
                      : t('monthlyCapLabel')}
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
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-dashed border-amber-line bg-amber-tint px-4 py-3 text-[13px] text-amber">
          <div>
            <p className="font-bold">{t('hardCapTitle')}</p>
            <p className="text-ink-2">{t('hardCap')}</p>
          </div>
          <span className="rounded-xs border border-amber-line bg-paper px-2 py-0.5 font-mono text-[12px] font-bold text-amber">
            {t('hardCapCode')}
          </span>
        </div>
      ) : null}

      {canManage ? (
        <form
          onSubmit={(event) => void save(event)}
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
              htmlFor={`budget-period-${botId ?? 'workspace'}`}
              className="text-[14px] font-bold text-ink"
            >
              {t('period')}
            </label>
            <select
              id={`budget-period-${botId ?? 'workspace'}`}
              className="min-h-[44px] rounded-sm border-[1.5px] border-line-strong bg-paper px-3 py-2 text-[15px] text-ink focus:border-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              value={period}
              onChange={(event) =>
                setPeriod(event.target.value as 'day' | 'month')
              }
            >
              {botId ? <option value="day">{t('daily')}</option> : null}
              <option value="month">{t('monthly')}</option>
            </select>
          </div>
          <Button type="submit" disabled={saving || !limit}>
            {t('save')}
          </Button>
        </form>
      ) : null}
    </Card>
  );
}
