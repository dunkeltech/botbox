'use client';

import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations, useFormatter } from 'next-intl';
import { Alert, Button, Card, Input } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type Summary = components['schemas']['BudgetSummary'];

/** Shows metered workspace spend and lets workspace administrators set bot limits. */
export function BudgetPanel({
  workspaceId,
  botId,
  canManage = false,
}: {
  workspaceId: string;
  botId?: string;
  canManage?: boolean;
}) {
  const t = useTranslations('budgets');
  const format = useFormatter();
  const client = useQueryClient();
  const [limit, setLimit] = useState('');
  const [period, setPeriod] = useState<'day' | 'month'>(
    botId ? 'day' : 'month',
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(false);
  const base = `/api/workspaces/${workspaceId}`;
  const query = useQuery({
    queryKey: ['budget', workspaceId, botId],
    queryFn: () =>
      requestJson<Summary>(`${base}/budgets${botId ? `?botId=${botId}` : ''}`),
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
    } catch {
      setError(true);
    } finally {
      setSaving(false);
    }
  }
  return (
    <Card className="flex flex-col gap-4 p-5">
      <h2 className="text-[20px] font-black">
        {t(botId ? 'botTitle' : 'title')}
      </h2>
      {query.data ? (
        <p>
          {t('today', {
            amount: format.number(query.data.spendTodayMicros / 1000000, {
              style: 'currency',
              currency: 'USD',
            }),
          })}
        </p>
      ) : null}
      {error || query.isError ? (
        <Alert tone="warning">{t('error')}</Alert>
      ) : null}
      {query.data?.budgets.map((b) => (
        <div
          key={b.id}
          className={`rounded-md border p-3 ${b.warning ? 'border-amber-line bg-amber-tint' : 'border-line bg-paper-2'} ${b.exceeded ? 'border-dashed' : ''}`}
        >
          <p>
            {t(b.period, {
              spent: format.number(b.spentMicros / 1000000, {
                style: 'currency',
                currency: 'USD',
              }),
              limit: format.number(b.limitMicros / 1000000, {
                style: 'currency',
                currency: 'USD',
              }),
            })}
          </p>
          <div className="mt-3 flex items-center justify-between gap-3 font-mono text-[12px] text-muted">
            <span>
              {t('used', {
                percent: Math.round((b.spentMicros / b.limitMicros) * 100),
              })}
            </span>
            <span>{t('threshold')}</span>
          </div>
          <div
            role="progressbar"
            aria-label={t(b.period, {
              spent: format.number(b.spentMicros / 1000000, {
                style: 'currency',
                currency: 'USD',
              }),
              limit: format.number(b.limitMicros / 1000000, {
                style: 'currency',
                currency: 'USD',
              }),
            })}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.min(
              100,
              Math.round((b.spentMicros / b.limitMicros) * 100),
            )}
            className="my-2 h-2 overflow-hidden rounded-full bg-mint-soft"
          >
            <div
              className={`h-full rounded-full ${b.warning ? 'bg-amber' : 'bg-green-600'}`}
              style={{
                width: `${Math.min(100, (b.spentMicros / b.limitMicros) * 100)}%`,
              }}
            />
          </div>
          {b.exceeded ? (
            <p>{t('exceeded')}</p>
          ) : b.warning ? (
            <p>{t('warning')}</p>
          ) : null}
        </div>
      ))}
      {botId ? (
        <p className="rounded-md border border-dashed border-amber-line bg-amber-tint p-4 text-[13px] text-amber">
          {t('hardCap')}
        </p>
      ) : null}
      {canManage ? (
        <form
          onSubmit={(event) => void save(event)}
          className="flex flex-col gap-3"
        >
          <Input
            label={t('limit')}
            type="number"
            min="0.000001"
            max="1000000"
            step="0.000001"
            value={limit}
            required
            onChange={(event) => setLimit(event.target.value)}
          />
          <label className="flex flex-col gap-2 text-[14px] font-bold">
            {t('period')}
            <select
              className="min-h-[44px] rounded-sm border border-line-strong bg-paper p-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-green"
              value={period}
              onChange={(event) =>
                setPeriod(event.target.value as 'day' | 'month')
              }
            >
              {botId ? <option value="day">{t('daily')}</option> : null}
              <option value="month">{t('monthly')}</option>
            </select>
          </label>
          <Button type="submit" disabled={saving || !limit}>
            {t('save')}
          </Button>
        </form>
      ) : null}
    </Card>
  );
}
