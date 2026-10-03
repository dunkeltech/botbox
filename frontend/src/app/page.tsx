import Link from 'next/link';
import { getTranslations } from 'next-intl/server';

import { Card, Eyebrow } from '@/components/ui';

/**
 * Root landing page linking to the living design styleguide during Phase 0.
 */
export default async function HomePage() {
  const t = await getTranslations('home');
  const common = await getTranslations('common');

  return (
    <div className="min-h-screen bg-paper-2">
      <header className="h-16 border-b border-line bg-paper">
        <div className="mx-auto flex h-full max-w-[1360px] items-center justify-between px-4 sm:px-6">
          <span className="text-[22px] font-black tracking-[-0.01em] text-ink">
            {common('brandLeft')}
            <span className="text-green">{common('brandDot')}</span>
            {common('brandRight')}
          </span>
          <span className="text-[13px] font-bold text-muted">
            {common('endorsement')}
          </span>
        </div>
      </header>

      <main className="mx-auto flex max-w-[1360px] flex-col gap-6 px-4 py-10 sm:px-6">
        <div className="flex flex-col gap-3">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h1 className="text-[44px] font-black leading-[1.15] tracking-[-0.01em] text-ink">
            {t('title')}
          </h1>
          <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
            {t('lead')}
          </p>
        </div>

        <div className="flex flex-wrap gap-4">
          <Link
            href="/dev/styleguide"
            className="inline-flex min-h-[44px] items-center justify-center rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper transition duration-150 hover:-translate-y-px hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {t('styleguideCta')}
          </Link>
        </div>

        <Card>
          <h2 className="text-[22px] font-black leading-[1.3] text-ink">
            {t('architectureLabel')}
          </h2>
          <p className="mt-2 text-[16px] leading-[1.6] text-ink-2">
            {t('architectureDesc')}
          </p>
        </Card>
      </main>
    </div>
  );
}
