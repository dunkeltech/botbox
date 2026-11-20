// SPDX-License-Identifier: AGPL-3.0-only

import Link from 'next/link';
import { useTranslations } from 'next-intl';

import { Alert } from '@/components/ui';
import type { components } from '@/types/openapi.gen';

/** Shows the exact workspace price required to run the bot computer. */
export function MissingModelPriceNotice({
  price,
  canManagePrices,
}: {
  price: components['schemas']['MissingModelPrice'];
  canManagePrices: boolean;
}) {
  const t = useTranslations('workspaceSettings.modelPrices');
  return (
    <Alert tone="warning">
      <span>{t('missing', price)}</span>{' '}
      {canManagePrices ? (
        <Link
          href="/settings/budgets#model-prices"
          className="inline-flex min-h-[44px] items-center font-bold underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-green"
        >
          {t('openSettings')}
        </Link>
      ) : (
        <span>{t('askAdmin')}</span>
      )}
    </Alert>
  );
}
