// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useTranslations } from 'next-intl';

import { Card, Eyebrow } from '@/components/ui';

/**
 * Zero-layout-shift workspace skeleton paired with a branded bot.Box hexagon-robot
 * loading indicator inside the 16:10 terminal viewport.
 */
export function BotWorkspaceSkeleton() {
  const t = useTranslations('workspace');

  return (
    <section
      aria-label={t('loading.regionLabel')}
      aria-busy="true"
      className="flex flex-col gap-5"
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <span
            aria-hidden="true"
            className="h-5 w-24 animate-pulse rounded-pill bg-line"
          />
          <span aria-hidden="true" className="text-line-strong">
            /
          </span>
          <Eyebrow>{t('eyebrow')}</Eyebrow>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex min-w-0 flex-1 items-center gap-4">
            <div
              aria-hidden="true"
              className="grid h-14 w-14 shrink-0 place-items-center rounded-md border border-mint-line bg-mint-soft"
            >
              <svg
                width="32"
                height="32"
                viewBox="0 0 160 160"
                fill="none"
                aria-hidden="true"
              >
                <line
                  x1="80"
                  y1="20"
                  x2="80"
                  y2="10"
                  stroke="#347a3b"
                  strokeWidth="8"
                  strokeLinecap="round"
                />
                <circle cx="80" cy="8" r="7" fill="#3f9147" />
                <path
                  d="M49 20.3 111 20.3 142 74 111 127.7 49 127.7 18 74Z"
                  fill="none"
                  stroke="#347a3b"
                  strokeWidth="7"
                  strokeLinejoin="round"
                />
                <circle cx="60" cy="64" r="10" fill="#3f9147" />
                <circle cx="100" cy="64" r="10" fill="#3f9147" />
              </svg>
            </div>
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <div className="flex flex-wrap items-center gap-3">
                <span
                  aria-hidden="true"
                  className="h-8 w-44 animate-pulse rounded-sm bg-line"
                />
                <span
                  aria-hidden="true"
                  className="h-6 w-24 animate-pulse rounded-pill border border-mint-line bg-mint-soft"
                />
              </div>
              <div aria-hidden="true" className="flex flex-wrap gap-2">
                <span className="h-6 w-36 animate-pulse rounded-xs border border-line bg-paper" />
                <span className="h-6 w-40 animate-pulse rounded-xs border border-line bg-paper" />
                <span className="h-6 w-32 animate-pulse rounded-xs border border-mint-line bg-mint-soft" />
              </div>
            </div>
          </div>

          <div
            aria-hidden="true"
            className="flex flex-wrap items-center gap-2.5"
          >
            <span className="h-11 w-24 animate-pulse rounded-sm border border-line bg-paper" />
            <span className="h-11 w-28 animate-pulse rounded-sm border border-line bg-paper" />
            <span className="h-11 w-24 animate-pulse rounded-sm border border-line bg-paper" />
          </div>
        </div>
      </div>

      <div className="grid gap-5 lg:grid-cols-[400px_minmax(0,1fr)] lg:items-stretch">
        <Card className="flex min-h-[540px] flex-col justify-between gap-4 p-4">
          <div className="flex flex-col gap-3">
            <div
              aria-hidden="true"
              className="flex items-center justify-between border-b border-line pb-3"
            >
              <div className="flex flex-col gap-1.5">
                <span className="h-5 w-24 animate-pulse rounded-xs bg-line" />
                <span className="h-3.5 w-44 animate-pulse rounded-xs bg-paper-2" />
              </div>
              <span className="h-6 w-16 animate-pulse rounded-xs border border-line bg-paper-2" />
            </div>

            <div
              aria-hidden="true"
              className="h-10 w-full animate-pulse rounded-sm border border-line bg-paper-2"
            />

            <div aria-hidden="true" className="flex flex-col gap-4 pt-2">
              <div className="ml-auto flex w-3/4 flex-col gap-2 rounded-[14px_14px_4px_14px] bg-dark/15 p-3">
                <span className="h-3.5 w-full animate-pulse rounded-xs bg-line-strong" />
                <span className="h-3.5 w-2/3 animate-pulse rounded-xs bg-line-strong" />
              </div>

              <div className="flex items-start gap-2.5">
                <span className="h-8 w-8 shrink-0 animate-pulse rounded-sm border border-mint-line bg-mint-soft" />
                <div className="flex flex-1 flex-col gap-2 rounded-md border border-line bg-paper-2 p-3">
                  <span className="h-3.5 w-5/6 animate-pulse rounded-xs bg-line" />
                  <span className="h-3.5 w-full animate-pulse rounded-xs bg-line" />
                  <span className="h-3.5 w-1/2 animate-pulse rounded-xs bg-line" />
                  <span className="mt-1 h-6 w-32 animate-pulse rounded-pill border border-blue-line bg-blue-tint" />
                </div>
              </div>

              <div className="flex items-start gap-2.5">
                <span className="h-8 w-8 shrink-0 animate-pulse rounded-sm border border-mint-line bg-mint-soft" />
                <div className="flex flex-1 flex-col gap-2 rounded-md border border-line bg-paper-2 p-3">
                  <span className="h-3.5 w-4/5 animate-pulse rounded-xs bg-line" />
                  <span className="h-3.5 w-3/5 animate-pulse rounded-xs bg-line" />
                </div>
              </div>
            </div>
          </div>

          <div
            aria-hidden="true"
            className="flex items-center justify-between rounded-md border-[1.5px] border-line-strong bg-paper px-3.5 py-3"
          >
            <span className="h-4 w-36 animate-pulse rounded-xs bg-line" />
            <span className="h-9 w-16 animate-pulse rounded-sm bg-mint-soft" />
          </div>
        </Card>

        <Card className="flex flex-col overflow-hidden p-0">
          <div
            aria-hidden="true"
            className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3"
          >
            <div className="flex gap-1 rounded-md bg-paper-2 p-1">
              <span className="h-10 w-24 rounded-sm bg-dark" />
              <span className="h-10 w-24 animate-pulse rounded-sm bg-line/60" />
              <span className="h-10 w-20 animate-pulse rounded-sm bg-line/60" />
              <span className="h-10 w-16 animate-pulse rounded-sm bg-line/60" />
            </div>
            <div className="flex items-center gap-2">
              <span className="h-8 w-28 animate-pulse rounded-pill border border-line bg-paper-2" />
              <span className="h-11 w-28 animate-pulse rounded-sm border border-line bg-paper" />
              <span className="h-11 w-11 animate-pulse rounded-sm border border-line bg-paper" />
            </div>
          </div>

          <div className="relative flex aspect-[16/10] w-full flex-col items-center justify-center overflow-hidden bg-terminal p-6 text-center">
            <div
              aria-hidden="true"
              className="pointer-events-none absolute inset-x-0 top-0 flex items-center justify-between border-b border-dark bg-dark-2/80 px-4 py-2 font-mono text-[12px] text-on-dark-muted"
            >
              <span>{t('loading.streamBadge')}</span>
              <span>{t('loading.sandboxBadge')}</span>
            </div>

            <div className="relative z-10 flex w-full max-w-[400px] flex-col items-center rounded-lg border border-dark bg-dark-2 px-6 py-7 shadow-card-hover">
              <div
                aria-hidden="true"
                className="relative mb-4 grid h-[68px] w-[68px] place-items-center"
              >
                <span className="absolute inset-0 animate-pulse rounded-full border border-green-light/30 bg-green-light/10" />
                <svg
                  width="46"
                  height="46"
                  viewBox="0 0 160 160"
                  fill="none"
                  aria-hidden="true"
                >
                  <line
                    x1="80"
                    y1="20"
                    x2="80"
                    y2="10"
                    stroke="#c9d8c9"
                    strokeWidth="6"
                    strokeLinecap="round"
                  />
                  <circle cx="80" cy="8" r="7" fill="#5eab60" />
                  <path
                    d="M49 20.3 111 20.3 142 74 111 127.7 49 127.7 18 74Z"
                    fill="#14201a"
                    stroke="#c9d8c9"
                    strokeWidth="6"
                    strokeLinejoin="round"
                  />
                  <circle cx="60" cy="64" r="12" fill="#5eab60" />
                  <circle cx="100" cy="64" r="12" fill="#5eab60" />
                  <polygon
                    points="62,91 68.1,94.5 68.1,101.5 62,105 55.9,101.5 55.9,94.5"
                    fill="#8aa8db"
                  />
                  <polygon
                    points="80,91 86.1,94.5 86.1,101.5 80,105 73.9,101.5 73.9,94.5"
                    fill="#6d8fce"
                  />
                  <polygon
                    points="98,91 104.1,94.5 104.1,101.5 98,105 91.9,101.5 91.9,94.5"
                    fill="#3f5f9c"
                  />
                </svg>
              </div>

              <p
                role="status"
                aria-live="polite"
                className="text-[18px] font-black leading-[1.3] tracking-[-0.01em] text-paper"
              >
                {t('loadingBot')}
              </p>
              <p className="mt-1 font-mono text-[12px] text-on-dark-muted">
                {t('loading.subtitle')}
              </p>

              <div
                aria-hidden="true"
                className="mt-4 h-2 w-full max-w-[260px] overflow-hidden rounded-pill border border-dark bg-terminal"
              >
                <div className="h-full w-2/3 animate-pulse rounded-pill bg-green-light" />
              </div>

              <div
                aria-hidden="true"
                className="mt-4 flex flex-wrap items-center justify-center gap-2 font-mono text-[11px]"
              >
                <span className="rounded-xs border border-green/60 bg-dark px-2 py-0.5 font-bold text-mint">
                  {t('loading.stepProfile')}
                </span>
                <span className="animate-pulse rounded-xs border border-green-light/70 bg-dark px-2 py-0.5 font-bold text-green-light">
                  {t('loading.stepSandbox')}
                </span>
                <span className="rounded-xs border border-dark bg-terminal px-2 py-0.5 text-on-dark-muted">
                  {t('loading.stepDesktop')}
                </span>
              </div>
            </div>
          </div>

          <div
            aria-hidden="true"
            className="grid gap-4 border-t border-line bg-paper px-4 py-3 sm:grid-cols-2 xl:grid-cols-4"
          >
            {[0, 1, 2, 3].map((slot) => (
              <div key={slot} className="flex flex-col gap-1.5">
                <span className="h-3 w-16 animate-pulse rounded-xs bg-line" />
                <span className="h-4 w-28 animate-pulse rounded-xs bg-line" />
                <span className="h-1.5 w-full animate-pulse rounded-pill bg-paper-2" />
              </div>
            ))}
          </div>
        </Card>
      </div>
    </section>
  );
}
