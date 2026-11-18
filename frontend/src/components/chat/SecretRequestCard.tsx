// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowRight, CheckCircle2, Lock, ShieldAlert } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { Alert, Button } from '@/components/ui';
import type { ErrorTranslationKey } from '@/lib/errors';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

export type SecretRequestSummary =
  components['schemas']['SecretRequestSummary'];

function statusBadgeConfig(status: SecretRequestSummary['status']) {
  switch (status) {
    case 'fulfilled':
      return {
        key: 'secretStatusFulfilled' as const,
        frame: 'border-mint-line bg-mint-soft',
        pill: 'border-mint-line bg-paper text-green-600',
        dot: 'bg-green',
      };
    case 'denied':
      return {
        key: 'secretStatusDenied' as const,
        frame: 'border-amber-line bg-paper',
        pill: 'border-amber-line bg-amber-tint text-amber',
        dot: 'bg-amber',
      };
    case 'expired':
      return {
        key: 'secretStatusExpired' as const,
        frame: 'border-amber-line bg-paper',
        pill: 'border-amber-line bg-amber-tint text-amber',
        dot: 'bg-amber',
      };
    case 'cancelled':
      return {
        key: 'secretStatusCancelled' as const,
        frame: 'border-line bg-paper-2',
        pill: 'border-line bg-paper text-muted',
        dot: 'bg-muted',
      };
    case 'failed':
      return {
        key: 'secretStatusFailed' as const,
        frame: 'border-amber-line bg-paper',
        pill: 'border-amber-line bg-amber-tint text-amber',
        dot: 'bg-amber',
      };
    case 'fulfilling':
      return {
        key: 'secretStatusFulfilling' as const,
        frame: 'border-blue-line bg-blue-tint/40',
        pill: 'border-blue-line bg-paper text-blue',
        dot: 'bg-blue',
      };
    default:
      return {
        key: 'secretStatusPending' as const,
        frame: 'border-dashed border-amber-line bg-amber-tint',
        pill: 'border-amber-line bg-paper text-amber',
        dot: 'bg-amber',
      };
  }
}

/**
 * Renders an inline zero-context secret request card inside DM or group chat
 * with explicit status labels and write-only modal launch controls.
 */
export function SecretRequestCard({
  summary,
  role,
  busy = false,
  errorKey = null,
  onProvide,
  onTakeover,
  onDeny,
  className = '',
}: {
  summary: SecretRequestSummary;
  role: WorkspaceRole;
  busy?: boolean;
  errorKey?: ErrorTranslationKey | null;
  onProvide?: (summary: SecretRequestSummary) => void;
  onTakeover?: (summary: SecretRequestSummary) => void;
  onDeny?: (summary: SecretRequestSummary) => void;
  className?: string;
}) {
  const t = useTranslations('chat');
  const errors = useTranslations('errors');
  const cfg = statusBadgeConfig(summary.status);
  const canDecide = role !== 'viewer';
  const isBrowser = summary.kind === 'browser_login';
  const isBrowserBroker = isBrowser && Boolean(summary.brokerEnabled);
  const isUnsupportedBrowser = isBrowser && !summary.brokerEnabled;
  const isPending = summary.status === 'pending';

  return (
    <div
      className={`rounded-md border-[1.5px] p-3.5 ${cfg.frame} ${className}`.trim()}
      data-secret-request-id={summary.secretRequestId}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1.5 rounded-xs border border-line bg-paper px-2 py-0.5 font-mono text-[11px] font-bold text-ink-2">
          <Lock className="h-3.5 w-3.5 text-green-600" aria-hidden="true" />
          {isBrowser ? t('secretCardKindBrowser') : t('secretCardKindCli')}
        </span>
        <span
          className={`inline-flex items-center gap-1.5 rounded-pill border px-2.5 py-0.5 text-[12px] font-bold ${cfg.pill}`}
        >
          {summary.status === 'fulfilled' ? (
            <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
          ) : (
            <span
              className={`h-2 w-2 rounded-full ${cfg.dot}`}
              aria-hidden="true"
            />
          )}
          {t(cfg.key)}
        </span>
      </div>

      <p className="mt-2.5 text-[15px] font-black text-ink">
        {summary.adapterLabel || summary.adapterId}
      </p>
      <p className="mt-1 text-[14px] text-ink-2">{summary.reason}</p>

      <div className="mt-2.5 space-y-1 rounded-sm border border-line bg-paper px-3 py-2 font-mono text-[12px] text-ink-2">
        <div>
          {t('secretAdapterMeta', {
            adapter: summary.adapterId,
            origin: summary.targetOrigin,
          })}
        </div>
        {summary.headerScheme ? <div>{summary.headerScheme}</div> : null}
        {summary.envPlaceholderName && summary.placeholderValue ? (
          <div>
            {t('secretPlaceholderMeta', {
              env: summary.envPlaceholderName,
              value: summary.placeholderValue,
            })}
          </div>
        ) : null}
        {isBrowserBroker && summary.brokerSessionId ? (
          <div>
            {t('secretBrokerSessionMeta', {
              sessionId: summary.brokerSessionId,
            })}
          </div>
        ) : null}
      </div>

      {isUnsupportedBrowser ? (
        <div className="mt-2.5 flex items-start gap-2 rounded-sm border border-amber-line bg-paper px-3 py-2 text-[13px] text-amber">
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <span>{t('secretBrowserUnsupported')}</span>
        </div>
      ) : isBrowserBroker && summary.takeoverActive ? (
        <div className="mt-2.5 flex items-start gap-2 rounded-sm border border-amber-line bg-amber-tint px-3 py-2 text-[13px] font-bold text-amber">
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <span>{t('secretBrokerTakeoverActiveNote')}</span>
        </div>
      ) : summary.status === 'fulfilled' ? (
        <p className="mt-2.5 flex items-center gap-1.5 text-[13px] font-bold text-green-600">
          <Lock className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>
            {t('secretFulfilledNote', { origin: summary.targetOrigin })}
          </span>
        </p>
      ) : (
        <p className="mt-2.5 flex items-center gap-1.5 text-[12px] text-ink-2">
          <Lock
            className="h-3.5 w-3.5 shrink-0 text-green-600"
            aria-hidden="true"
          />
          <span>{t('secretZeroContextNote')}</span>
        </p>
      )}

      {errorKey ? (
        <Alert tone="warning" className="mt-2.5">
          {errors(errorKey)}
        </Alert>
      ) : null}

      {isPending && !isUnsupportedBrowser ? (
        canDecide ? (
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {onProvide ? (
              <Button
                type="button"
                disabled={busy}
                onClick={() => onProvide(summary)}
                className="min-h-[44px] flex-1"
              >
                <Lock className="h-4 w-4" aria-hidden="true" />
                <span>{t('secretProvideCta')}</span>
                <ArrowRight className="h-4 w-4" aria-hidden="true" />
              </Button>
            ) : null}
            {isBrowserBroker && onTakeover ? (
              <Button
                type="button"
                variant="secondary"
                disabled={busy}
                onClick={() => onTakeover(summary)}
                className="min-h-[44px]"
              >
                {t('secretTakeoverCta')}
              </Button>
            ) : null}
            {onDeny ? (
              <Button
                type="button"
                variant="secondary"
                disabled={busy}
                onClick={() => onDeny(summary)}
                className="min-h-[44px] border-amber-line text-amber hover:border-amber hover:bg-amber-tint"
              >
                {t('secretDenyCta')}
              </Button>
            ) : null}
          </div>
        ) : (
          <p className="mt-2.5 text-[12px] font-bold text-muted">
            {t('secretViewerHint')}
          </p>
        )
      ) : null}
    </div>
  );
}
