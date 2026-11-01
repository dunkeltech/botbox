'use client';

import { type FormEvent, useCallback, useEffect, useState } from 'react';
import { useFormatter, useTranslations } from 'next-intl';

import { Alert, Button, Card, Eyebrow, Input } from '@/components/ui';
import {
  ApiClientError,
  listModelPrices,
  setModelPrice,
} from '@/lib/api-client';
import type { ModelPrice } from '@/types/api';

export interface ModelPricesPanelProps {
  workspaceId: string;
  canManage: boolean;
  /** Ignore 404 in older test harnesses that only mock PUT /model-prices. */
  ignoreMissing?: boolean;
}

function formatMicrosAsUsdInput(micros: number): string {
  return (micros / 1_000_000).toFixed(2);
}

/**
 * Displays configured workspace model_prices rows and lets owners/admins add or update rates.
 */
export function ModelPricesPanel({
  workspaceId,
  canManage,
  ignoreMissing = false,
}: ModelPricesPanelProps) {
  const t = useTranslations('workspaceSettings.modelPrices');
  const tMembers = useTranslations('members');
  const format = useFormatter();

  const [prices, setPrices] = useState<ModelPrice[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);

  const [priceProvider, setPriceProvider] = useState('anthropic');
  const [priceModel, setPriceModel] = useState('');
  const [priceInputUsd, setPriceInputUsd] = useState('');
  const [priceOutputUsd, setPriceOutputUsd] = useState('');
  const [priceCacheUsd, setPriceCacheUsd] = useState('0');
  const [savingPrice, setSavingPrice] = useState(false);
  const [priceSaved, setPriceSaved] = useState(false);
  const [priceError, setPriceError] = useState(false);

  const loadPrices = useCallback(async () => {
    setLoading(true);
    setLoadError(false);
    try {
      const res = await listModelPrices(workspaceId);
      setPrices(res.prices ?? []);
    } catch (err) {
      if (
        ignoreMissing &&
        err instanceof ApiClientError &&
        err.status === 404
      ) {
        setPrices([]);
      } else {
        setLoadError(true);
      }
    } finally {
      setLoading(false);
    }
  }, [ignoreMissing, workspaceId]);

  useEffect(() => {
    void loadPrices();
  }, [loadPrices]);

  function handleSelectRow(row: ModelPrice) {
    setPriceProvider(row.provider);
    setPriceModel(row.model);
    setPriceInputUsd(formatMicrosAsUsdInput(row.inputMicrosPerMillion));
    setPriceOutputUsd(formatMicrosAsUsdInput(row.outputMicrosPerMillion));
    setPriceCacheUsd(formatMicrosAsUsdInput(row.cacheMicrosPerMillion));
    setPriceSaved(false);
    setPriceError(false);
  }

  async function handleSaveModelPrice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canManage || savingPrice) {
      return;
    }
    setSavingPrice(true);
    setPriceSaved(false);
    setPriceError(false);
    const nextEntry: ModelPrice = {
      provider: priceProvider.trim(),
      model: priceModel.trim(),
      inputMicrosPerMillion: Math.round(Number(priceInputUsd) * 1_000_000),
      outputMicrosPerMillion: Math.round(Number(priceOutputUsd) * 1_000_000),
      cacheMicrosPerMillion: Math.round(
        Number(priceCacheUsd || '0') * 1_000_000,
      ),
    };
    try {
      await setModelPrice(workspaceId, nextEntry);
      setPrices((prev) => {
        const filtered = prev.filter(
          (item) =>
            !(
              item.provider === nextEntry.provider &&
              item.model === nextEntry.model
            ),
        );
        return [...filtered, nextEntry].sort((a, b) =>
          a.provider === b.provider
            ? a.model.localeCompare(b.model)
            : a.provider.localeCompare(b.provider),
        );
      });
      setPriceSaved(true);
    } catch {
      setPriceError(true);
    } finally {
      setSavingPrice(false);
    }
  }

  return (
    <Card className="flex flex-col gap-5 p-6">
      <div className="flex flex-col gap-1">
        <Eyebrow>{t('eyebrow')}</Eyebrow>
        <h2 className="text-[20px] font-black text-ink">{t('title')}</h2>
        <p className="text-[14px] leading-[1.6] text-ink-2">{t('lead')}</p>
      </div>

      {loadError ? (
        <Alert tone="warning">{tMembers('modelPriceError')}</Alert>
      ) : null}

      {loading ? (
        <p role="status" className="text-[14px] text-muted">
          {t('loading')}
        </p>
      ) : prices.length === 0 ? (
        <p className="rounded-sm border border-line bg-paper-2 px-4 py-3 text-[14px] text-muted">
          {t('empty')}
        </p>
      ) : (
        <div className="overflow-x-auto rounded-md border border-line">
          <table className="w-full border-collapse text-left">
            <thead>
              <tr className="border-b border-line bg-paper-2 text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                <th className="px-3 py-2.5">{t('colProvider')}</th>
                <th className="px-3 py-2.5">{t('colModel')}</th>
                <th className="px-3 py-2.5">{t('colInput')}</th>
                <th className="px-3 py-2.5">{t('colOutput')}</th>
                <th className="px-3 py-2.5">{t('colCache')}</th>
                {canManage ? (
                  <th className="px-3 py-2.5 text-right">{t('colAction')}</th>
                ) : null}
              </tr>
            </thead>
            <tbody className="divide-y divide-line font-mono text-[13px]">
              {prices.map((row) => (
                <tr key={`${row.provider}:${row.model}`}>
                  <td className="px-3 py-2.5 font-bold text-ink">
                    {row.provider}
                  </td>
                  <td className="px-3 py-2.5 text-blue">{row.model}</td>
                  <td className="px-3 py-2.5 text-ink-2">
                    {format.number(row.inputMicrosPerMillion / 1_000_000, {
                      style: 'currency',
                      currency: 'USD',
                    })}
                  </td>
                  <td className="px-3 py-2.5 text-ink-2">
                    {format.number(row.outputMicrosPerMillion / 1_000_000, {
                      style: 'currency',
                      currency: 'USD',
                    })}
                  </td>
                  <td className="px-3 py-2.5 text-ink-2">
                    {format.number(row.cacheMicrosPerMillion / 1_000_000, {
                      style: 'currency',
                      currency: 'USD',
                    })}
                  </td>
                  {canManage ? (
                    <td className="px-3 py-2.5 text-right font-sans">
                      <Button
                        type="button"
                        variant="secondary"
                        onClick={() => handleSelectRow(row)}
                      >
                        {t('editAction')}
                      </Button>
                    </td>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {!canManage ? (
        <Alert tone="info">{t('readOnlyNotice')}</Alert>
      ) : (
        <div className="flex flex-col gap-4 border-t border-line pt-4">
          <h3 className="text-[16px] font-bold text-ink">{t('formTitle')}</h3>
          {priceSaved ? (
            <Alert tone="success">{tMembers('modelPriceSaved')}</Alert>
          ) : null}
          {priceError ? (
            <Alert tone="warning">{tMembers('modelPriceError')}</Alert>
          ) : null}
          <form
            onSubmit={(event) => void handleSaveModelPrice(event)}
            className="flex flex-col gap-3"
          >
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
              <Input
                label={tMembers('modelPriceProvider')}
                mono
                value={priceProvider}
                required
                onChange={(event) => setPriceProvider(event.target.value)}
              />
              <Input
                label={tMembers('modelPriceModel')}
                mono
                placeholder="claude-sonnet-4"
                value={priceModel}
                required
                onChange={(event) => setPriceModel(event.target.value)}
              />
              <Input
                label={tMembers('modelPriceInput')}
                type="number"
                min="0"
                step="0.01"
                mono
                value={priceInputUsd}
                required
                onChange={(event) => setPriceInputUsd(event.target.value)}
              />
              <Input
                label={tMembers('modelPriceOutput')}
                type="number"
                min="0"
                step="0.01"
                mono
                value={priceOutputUsd}
                required
                onChange={(event) => setPriceOutputUsd(event.target.value)}
              />
              <Input
                label={tMembers('modelPriceCache')}
                type="number"
                min="0"
                step="0.01"
                mono
                value={priceCacheUsd}
                required
                onChange={(event) => setPriceCacheUsd(event.target.value)}
              />
            </div>
            <div className="flex justify-end">
              <Button type="submit" variant="secondary" disabled={savingPrice}>
                {savingPrice
                  ? tMembers('modelPriceSaving')
                  : tMembers('modelPriceSave')}
              </Button>
            </div>
          </form>
        </div>
      )}
    </Card>
  );
}
