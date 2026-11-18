// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { Bell } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { Button, Card, Eyebrow, Input, StatusChip } from '@/components/ui';
import tokens from '../../../../design/tokens.json';

type ColorKey = keyof typeof tokens.color;

interface SwatchGroupConfig {
  groupKey: 'core' | 'blues' | 'amber' | 'dark';
  tokenKeys: readonly ColorKey[];
  darkPreview?: boolean;
}

const SWATCH_GROUPS: readonly SwatchGroupConfig[] = [
  {
    groupKey: 'core',
    tokenKeys: [
      'ink',
      'ink-2',
      'muted',
      'paper',
      'paper-2',
      'line',
      'line-strong',
      'green',
      'green-600',
      'green-light',
      'mint',
      'mint-soft',
      'mint-line',
      'dark',
      'dark-2',
      'terminal',
    ],
  },
  {
    groupKey: 'blues',
    tokenKeys: ['blue', 'blue-tint', 'blue-line', 'hex-1', 'hex-2', 'hex-3'],
  },
  {
    groupKey: 'amber',
    tokenKeys: ['amber', 'amber-tint', 'amber-line', 'amber-on-dark'],
  },
  {
    groupKey: 'dark',
    tokenKeys: ['on-dark', 'on-dark-muted', 'on-dark-faint'],
    darkPreview: true,
  },
];

/**
 * Renders the living bot.Box styleguide covering colour tokens, Lato typography,
 * eyebrows, buttons, form controls, cards, and semantic status chips.
 */
export function StyleguideShowcase() {
  const t = useTranslations('styleguide');
  const common = useTranslations('common');

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

      <main className="mx-auto flex max-w-[1360px] flex-col gap-7 px-4 py-10 sm:px-6">
        <section className="flex flex-col gap-2">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h1 className="text-[44px] font-black leading-[1.15] tracking-[-0.01em] text-ink">
            {t('title')}
          </h1>
          <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
            {t('lead')}
          </p>
        </section>

        <Card>
          <div className="flex flex-col gap-4">
            <div>
              <Eyebrow>{t('sections.palette.eyebrow')}</Eyebrow>
              <h2 className="mt-1 text-[22px] font-black leading-[1.3] text-ink">
                {t('sections.palette.title')}
              </h2>
              <p className="mt-1 text-[14px] text-ink-2">
                {t('sections.palette.lead')}
              </p>
            </div>

            {SWATCH_GROUPS.map((group) => (
              <div key={group.groupKey} className="flex flex-col gap-2.5">
                <h3 className="text-[13px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t(`sections.palette.groups.${group.groupKey}`)}
                </h3>
                <div
                  className={`grid gap-3 sm:grid-cols-2 lg:grid-cols-4 ${group.darkPreview ? 'rounded-md bg-terminal p-4' : ''}`.trim()}
                >
                  {group.tokenKeys.map((tokenKey) => {
                    const hex = tokens.color[tokenKey];
                    return (
                      <div
                        key={tokenKey}
                        className="flex items-center gap-3 rounded-sm border border-line bg-paper p-2.5"
                      >
                        <span
                          aria-hidden="true"
                          className="h-9 w-9 shrink-0 rounded-xs border border-line-strong"
                          style={{ backgroundColor: hex }}
                        />
                        <div className="min-w-0">
                          <p className="truncate font-mono text-[13px] font-bold text-ink">
                            {hex}
                          </p>
                          <p className="text-[13px] text-muted">
                            {t(`sections.palette.tokens.${tokenKey}`)}
                          </p>
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        </Card>

        <Card>
          <div className="flex flex-col gap-4">
            <div>
              <Eyebrow>{t('sections.typography.eyebrow')}</Eyebrow>
              <h2 className="mt-1 text-[22px] font-black leading-[1.3] text-ink">
                {t('sections.typography.title')}
              </h2>
              <p className="mt-1 text-[14px] text-ink-2">
                {t('sections.typography.lead')}
              </p>
            </div>

            <div className="grid gap-4 border-t border-line pt-4">
              <div>
                <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t('sections.typography.samples.pageTitleLabel')}
                </p>
                <p className="text-[44px] font-black leading-[1.15] tracking-[-0.01em] text-ink">
                  {t('sections.typography.samples.pageTitleSample')}
                </p>
              </div>
              <div>
                <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t('sections.typography.samples.entityTitleLabel')}
                </p>
                <p className="text-[30px] font-black leading-[1.15] tracking-[-0.01em] text-ink">
                  {t('sections.typography.samples.entityTitleSample')}
                </p>
              </div>
              <div>
                <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t('sections.typography.samples.sectionTitleLabel')}
                </p>
                <p className="text-[22px] font-black leading-[1.3] text-ink">
                  {t('sections.typography.samples.sectionTitleSample')}
                </p>
              </div>
              <div>
                <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t('sections.typography.samples.bodyLabel')}
                </p>
                <p className="text-[16px] leading-[1.6] text-ink-2">
                  {t('sections.typography.samples.bodySample')}
                </p>
              </div>
              <div>
                <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t('sections.typography.samples.monoLabel')}
                </p>
                <p className="rounded-xs bg-terminal px-3 py-2 font-mono text-[13px] text-on-dark">
                  {t('sections.typography.samples.monoSample')}
                </p>
              </div>
            </div>
          </div>
        </Card>

        <Card>
          <Eyebrow>{t('sections.eyebrowSection.eyebrow')}</Eyebrow>
          <h2 className="mt-1 text-[22px] font-black leading-[1.3] text-ink">
            {t('sections.eyebrowSection.title')}
          </h2>
          <div className="mt-4 flex flex-wrap gap-6">
            <Eyebrow>{t('sections.eyebrowSection.sampleOne')}</Eyebrow>
            <Eyebrow>{t('sections.eyebrowSection.sampleTwo')}</Eyebrow>
            <Eyebrow>{t('sections.eyebrowSection.sampleThree')}</Eyebrow>
          </div>
        </Card>

        <Card>
          <Eyebrow>{t('sections.buttons.eyebrow')}</Eyebrow>
          <h2 className="mt-1 text-[22px] font-black leading-[1.3] text-ink">
            {t('sections.buttons.title')}
          </h2>
          <div className="mt-4 flex flex-wrap items-center gap-3">
            <Button variant="primary">{t('sections.buttons.primary')}</Button>
            <Button variant="primary" arrow>
              {t('sections.buttons.primaryArrow')}
            </Button>
            <Button variant="secondary">
              {t('sections.buttons.secondary')}
            </Button>
            <div className="rounded-sm bg-terminal p-2">
              <Button variant="onDark">{t('sections.buttons.onDark')}</Button>
            </div>
            <Button variant="icon" aria-label={t('sections.buttons.iconLabel')}>
              <Bell className="h-[18px] w-[18px]" aria-hidden="true" />
            </Button>
            <Button variant="primary" disabled>
              {t('sections.buttons.disabled')}
            </Button>
          </div>
        </Card>

        <Card>
          <Eyebrow>{t('sections.inputs.eyebrow')}</Eyebrow>
          <h2 className="mt-1 text-[22px] font-black leading-[1.3] text-ink">
            {t('sections.inputs.title')}
          </h2>
          <div className="mt-4 grid gap-4 md:grid-cols-2">
            <Input
              label={t('sections.inputs.defaultLabel')}
              placeholder={t('sections.inputs.defaultPlaceholder')}
              helperText={t('sections.inputs.defaultHelper')}
            />
            <Input
              label={t('sections.inputs.monoLabel')}
              defaultValue={t('sections.inputs.monoValue')}
              helperText={t('sections.inputs.monoHelper')}
              mono
            />
            <Input
              label={t('sections.inputs.readonlyLabel')}
              defaultValue={t('sections.inputs.readonlyValue')}
              helperText={t('sections.inputs.readonlyHelper')}
              readOnly
              mono
            />
            <Input
              label={t('sections.inputs.warningLabel')}
              defaultValue={t('sections.inputs.warningValue')}
              warning={t('sections.inputs.warningMessage')}
              mono
            />
          </div>
        </Card>

        <section className="flex flex-col gap-3">
          <Eyebrow>{t('sections.cards.eyebrow')}</Eyebrow>
          <h2 className="text-[22px] font-black leading-[1.3] text-ink">
            {t('sections.cards.title')}
          </h2>
          <div className="grid gap-4 md:grid-cols-2">
            <Card>
              <h3 className="text-[18px] font-black text-ink">
                {t('sections.cards.standardTitle')}
              </h3>
              <p className="mt-1.5 text-[15px] leading-[1.55] text-ink-2">
                {t('sections.cards.standardBody')}
              </p>
            </Card>
            <Card interactive>
              <h3 className="text-[18px] font-black text-ink">
                {t('sections.cards.interactiveTitle')}
              </h3>
              <p className="mt-1.5 text-[15px] leading-[1.55] text-ink-2">
                {t('sections.cards.interactiveBody')}
              </p>
            </Card>
          </div>
        </section>

        <Card>
          <Eyebrow>{t('sections.chips.eyebrow')}</Eyebrow>
          <h2 className="mt-1 text-[22px] font-black leading-[1.3] text-ink">
            {t('sections.chips.title')}
          </h2>
          <p className="mt-1 text-[14px] text-ink-2">
            {t('sections.chips.lead')}
          </p>
          <div className="mt-4 flex flex-wrap gap-3">
            <StatusChip status="working" label={t('sections.chips.working')} />
            <StatusChip
              status="thinking"
              label={t('sections.chips.thinking')}
            />
            <StatusChip status="idle" label={t('sections.chips.idle')} />
            <StatusChip status="stopped" label={t('sections.chips.stopped')} />
            <StatusChip
              status="budgetOk"
              label={t('sections.chips.budgetOk')}
            />
            <StatusChip
              status="budgetWarning"
              label={t('sections.chips.budgetWarning')}
            />
            <StatusChip
              status="budgetExceeded"
              label={t('sections.chips.budgetExceeded')}
            />
          </div>
        </Card>
      </main>
    </div>
  );
}
