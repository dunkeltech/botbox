// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useFormatter, useTranslations } from 'next-intl';

import { Alert, Button, Card, Input, Select } from '@/components/ui';
import {
  listUnresolvedSubscriptionUsage,
  reconcileSubscriptionUsage,
} from '@/lib/api-client';
import { resolveErrorKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

type Adjustment = components['schemas']['ReconcileSubscriptionUsageRequest'];

function tokenCount(form: FormData, field: string): number {
  const value = form.get(field);
  return typeof value === 'string' && /^\d+$/.test(value)
    ? Number(value)
    : Number.NaN;
}

/** Lets workspace owners and admins reconcile incomplete subscription charges. */
export function SubscriptionReconciliationPanel({
  workspaceId,
}: {
  workspaceId: string;
}) {
  const t = useTranslations('workspaceSettings.subscriptionReconciliation');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');
  const format = useFormatter();
  const client = useQueryClient();
  const queryKey = ['unresolved-subscription-usage', workspaceId];
  const [selectedId, setSelectedId] = useState<string>();
  const [invalid, setInvalid] = useState(false);

  const query = useQuery({
    queryKey,
    queryFn: () => listUnresolvedSubscriptionUsage(workspaceId),
    retry: false,
  });
  const selected = query.data?.find((row) => row.requestId === selectedId);
  const mutation = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Adjustment }) =>
      reconcileSubscriptionUsage(workspaceId, id, body),
    onSuccess: async () => {
      setSelectedId(undefined);
      await Promise.all([
        client.invalidateQueries({ queryKey }),
        client.invalidateQueries({ queryKey: ['budget', workspaceId] }),
      ]);
    },
  });

  function saveAdjustment(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected || mutation.isPending) {
      return;
    }
    const data = new FormData(event.currentTarget);
    const provider = data.get('provider');
    const basis = data.get('basis');
    const inputTokens = tokenCount(data, 'inputTokens');
    const outputTokens = tokenCount(data, 'outputTokens');
    const cacheTokens = tokenCount(data, 'cacheTokens');
    if (
      (provider !== 'openai' && provider !== 'anthropic') ||
      (basis !== 'provider_report' && basis !== 'operator_estimate') ||
      ![inputTokens, outputTokens, cacheTokens].every(Number.isSafeInteger) ||
      cacheTokens > inputTokens
    ) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    mutation.mutate({
      id: selected.requestId,
      body: { provider, basis, inputTokens, outputTokens, cacheTokens },
    });
  }

  return (
    <Card id="subscription-reconciliation" className="flex flex-col gap-4 p-6">
      <div className="flex flex-col gap-1">
        <h2 className="text-[20px] font-black text-ink">{t('title')}</h2>
        <p className="text-[14px] leading-[1.6] text-ink-2">{t('lead')}</p>
      </div>
      {mutation.isSuccess ? <Alert tone="success">{t('saved')}</Alert> : null}
      {query.isError ? <Alert tone="warning">{t('loadError')}</Alert> : null}
      {query.isPending ? (
        <p role="status" className="text-[14px] text-muted">
          {t('loading')}
        </p>
      ) : query.data?.length === 0 ? (
        <p className="text-[14px] text-muted">{t('empty')}</p>
      ) : null}
      {query.data?.length ? (
        <ul className="divide-y divide-line">
          {query.data.map((row) => (
            <li
              key={row.requestId}
              className="flex flex-wrap items-center justify-between gap-2 py-2"
            >
              <span className="text-[14px] text-ink">
                {t('request', {
                  bot: row.botName,
                  model: row.model,
                  time: format.dateTime(new Date(row.createdAt), {
                    dateStyle: 'medium',
                    timeStyle: 'long',
                    timeZone: 'UTC',
                  }),
                })}
              </span>
              <Button
                variant="secondary"
                disabled={mutation.isPending}
                aria-expanded={selectedId === row.requestId}
                onClick={() => {
                  mutation.reset();
                  setInvalid(false);
                  setSelectedId(row.requestId);
                }}
              >
                {t('edit')}
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      {selected ? (
        <form
          key={selected.requestId}
          onSubmit={saveAdjustment}
          className="flex flex-col gap-3 border-t border-line pt-4"
        >
          {mutation.isError ? (
            <Alert tone="warning">
              {tErrors(resolveErrorKey(mutation.error))}
            </Alert>
          ) : null}
          {invalid ? <Alert tone="warning">{t('invalid')}</Alert> : null}
          <div className="grid gap-3 sm:grid-cols-2">
            <Select
              label={t('provider')}
              name="provider"
              required
              defaultValue=""
            >
              <option value="" disabled>
                {t('chooseProvider')}
              </option>
              <option value="openai">{t('openai')}</option>
              <option value="anthropic">{t('anthropic')}</option>
            </Select>
            <Select label={t('basis')} name="basis" required defaultValue="">
              <option value="" disabled>
                {t('chooseBasis')}
              </option>
              <option value="provider_report">{t('providerReport')}</option>
              <option value="operator_estimate">{t('operatorEstimate')}</option>
            </Select>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            <Input
              label={t('input')}
              name="inputTokens"
              type="number"
              min="0"
              step="1"
              required
            />
            <Input
              label={t('output')}
              name="outputTokens"
              type="number"
              min="0"
              step="1"
              required
            />
            <Input
              label={t('cache')}
              name="cacheTokens"
              type="number"
              min="0"
              step="1"
              required
            />
          </div>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? tActions('saving') : t('submit')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={mutation.isPending}
              onClick={() => setSelectedId(undefined)}
            >
              {tActions('cancel')}
            </Button>
          </div>
        </form>
      ) : null}
    </Card>
  );
}
