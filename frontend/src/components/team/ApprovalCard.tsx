// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useTranslations } from 'next-intl';
import { Alert, BotAvatar, Button } from '@/components/ui';
import { payloadField } from '@/components/team/approvals';
import type { ErrorTranslationKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

type Approval = components['schemas']['Approval'];
type Decision = 'approved' | 'denied';

/**
 * The ask-before card from the team design. Pending, approved, and denied
 * each include the word, and the buttons exist only while a decision is
 * still allowed.
 */
export function ApprovalCard({
  approval,
  content,
  botName,
  where,
  avatarSeed,
  canDecide,
  busy = false,
  errorKey,
  onDecide,
  className = '',
}: {
  approval?: Approval;
  content?: string;
  botName?: string;
  where?: string;
  avatarSeed?: string;
  canDecide: boolean;
  busy?: boolean;
  errorKey?: ErrorTranslationKey | null;
  onDecide?: (decision: Decision) => void;
  className?: string;
}) {
  const t = useTranslations('team');
  const errors = useTranslations('errors');
  const kind = approval?.kind;
  const chip =
    kind === 'new_host'
      ? t('chipNewHost')
      : kind === 'raise_budget'
        ? t('chipBudget')
        : kind === 'push_protected'
          ? t('chipPush')
          : kind === 'custom'
            ? t('chipCustom')
            : kind === 'mcp_tool'
              ? t('chipMcpTool')
              : t('chipApproval');
  const kindTitle =
    kind === 'new_host'
      ? t('titleNewHost')
      : kind === 'raise_budget'
        ? t('titleBudget')
        : kind === 'push_protected'
          ? t('titlePush')
          : kind === 'custom'
            ? t('titleCustom')
            : kind === 'mcp_tool'
              ? t('titleMcpTool')
              : '';
  const approveLabel =
    kind === 'raise_budget'
      ? t('approveBudget')
      : kind === 'push_protected'
        ? t('approvePush')
        : kind === 'custom'
          ? t('approveCustom')
          : kind === 'mcp_tool'
            ? t('approveMcpTool')
            : t('approveHost');
  const title = approval ? kindTitle : content || chip;
  const payload = approval ? payloadField(approval) : '';
  const detail =
    content && content !== title && content !== payload ? content : '';
  const status = approval?.status;
  const statusLabel =
    status === 'approved'
      ? t('statusApproved')
      : status === 'denied'
        ? t('statusDenied')
        : status === 'pending'
          ? t('statusPending')
          : '';
  const frame =
    status === 'approved'
      ? 'border-mint-line bg-paper'
      : status === 'denied'
        ? 'border-amber-line bg-paper'
        : 'border-amber-on-dark bg-amber-tint';
  const showButtons = Boolean(canDecide && status === 'pending' && onDecide);

  return (
    <article
      className={`flex flex-col gap-3 rounded-lg border-[1.5px] px-4 py-4 shadow-card ${frame} ${className}`.trim()}
      aria-busy={busy || undefined}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          {avatarSeed ? (
            <BotAvatar seed={avatarSeed} size={40} decorative />
          ) : null}
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="rounded-xs border border-amber-line bg-paper px-2 py-0.5 font-mono text-[12px] font-bold text-amber">
                {chip}
              </span>
              {botName ? (
                <p className="text-[13px] text-muted">
                  {where
                    ? t('requestedIn', { bot: botName, where })
                    : t('requestedBy', { bot: botName })}
                </p>
              ) : null}
            </div>
            <h2 className="mt-1 text-[18px] font-black leading-snug">
              {title}
            </h2>
          </div>
        </div>
        {statusLabel ? (
          <p
            className={`inline-flex items-center gap-1.5 rounded-pill border px-2.5 py-1 text-[12px] font-bold ${
              status === 'approved'
                ? 'border-mint-line bg-mint-soft text-green-600'
                : 'border-amber-line bg-paper text-amber'
            }`}
          >
            <span
              aria-hidden="true"
              className={`inline-block h-2 w-2 rounded-pill ${
                status === 'approved' ? 'bg-green-600' : 'bg-amber'
              }`}
            />
            {statusLabel}
          </p>
        ) : null}
      </div>
      {payload || detail ? (
        <div className="flex flex-col gap-1 rounded-sm border border-line bg-paper px-3 py-2.5">
          {payload ? (
            <p className="break-all font-mono text-[13px] font-bold">
              {payload}
            </p>
          ) : null}
          {detail ? (
            <p className="whitespace-pre-wrap break-words text-[14px] text-ink-2">
              {detail}
            </p>
          ) : null}
        </div>
      ) : null}
      {errorKey ? <Alert tone="warning">{errors(errorKey)}</Alert> : null}
      {showButtons ? (
        <div className="flex flex-wrap justify-end gap-2">
          <button
            type="button"
            disabled={busy}
            className="inline-flex min-h-[44px] items-center justify-center rounded-sm border-[1.5px] border-amber-line bg-paper px-4 text-[15px] font-bold text-amber focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60"
            onClick={() => onDecide?.('denied')}
          >
            {t('deny')}
          </button>
          <Button arrow disabled={busy} onClick={() => onDecide?.('approved')}>
            {approveLabel}
          </Button>
        </div>
      ) : null}
    </article>
  );
}
