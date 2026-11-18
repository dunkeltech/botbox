// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowRight, Shield, ShieldAlert } from 'lucide-react';
import Link from 'next/link';
import { useTranslations } from 'next-intl';

import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

export type ChatMessageGuardrail =
  components['schemas']['ChatMessageGuardrail'];

export interface GuardrailNoticeCardProps {
  guardrail: ChatMessageGuardrail;
  role?: WorkspaceRole;
  onInspect?: (turnKey: string) => void;
  className?: string;
}

/**
 * Inline chat card surfacing LLM proxy guardrail interventions (blue notice
 * for redacted prompts/responses, dashed amber alert for blocked 403 calls).
 */
export function GuardrailNoticeCard({
  guardrail,
  role,
  onInspect,
  className = '',
}: GuardrailNoticeCardProps) {
  const t = useTranslations('chat');
  const isBlocked = guardrail.action === 'blocked';
  const isOutput = guardrail.direction === 'output';
  const canManageGuardrails = role === 'owner' || role === 'admin';

  const directionLabel = isOutput
    ? t('guardrailDirOutput')
    : t('guardrailDirInput');
  const rulesText =
    guardrail.ruleSlugs.length > 0 ? guardrail.ruleSlugs.join(', ') : 'policy';

  if (isBlocked) {
    return (
      <div
        data-guardrail-event-id={guardrail.eventId}
        role="status"
        className={`flex flex-col gap-2.5 rounded-md border-[1.5px] border-dashed border-amber-line bg-amber-tint p-3.5 text-ink ${className}`.trim()}
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <ShieldAlert
              className="h-4 w-4 shrink-0 text-amber"
              aria-hidden="true"
            />
            <span className="text-[14px] font-black text-amber">
              {isOutput
                ? t('guardrailBlockedOutputTitle')
                : t('guardrailBlockedInputTitle')}
            </span>
          </div>
          <span className="rounded-pill border border-amber-line bg-paper px-2.5 py-0.5 text-[12px] font-bold text-amber">
            {t('guardrailBlockedBadge')}
          </span>
        </div>

        <p className="font-mono text-[12px] text-ink-2">
          {t('guardrailRuleMeta', {
            rules: rulesText,
            direction: directionLabel,
            provider: guardrail.provider,
          })}
        </p>

        <p className="text-[13px] leading-[1.5] text-ink">
          {guardrail.summary}
        </p>
        <p className="text-[12px] text-muted">{t('guardrailBlockedHint')}</p>

        <div className="flex flex-wrap items-center gap-2 pt-0.5">
          {canManageGuardrails ? (
            <Link
              href="/settings/guardrails"
              className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-amber-line bg-paper px-3.5 text-[13px] font-bold text-amber transition duration-150 hover:bg-amber-tint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <span>{t('guardrailOpenSettings')}</span>
              <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
            </Link>
          ) : null}

          {guardrail.turnId && onInspect ? (
            <button
              type="button"
              onClick={() => onInspect(guardrail.turnId!)}
              className="inline-flex min-h-[44px] items-center gap-1 rounded-sm px-3 text-[13px] font-bold text-ink-2 hover:bg-paper/70 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <span>{t('guardrailInspectTrace')}</span>
            </button>
          ) : null}
        </div>
      </div>
    );
  }

  return (
    <div
      data-guardrail-event-id={guardrail.eventId}
      role="status"
      className={`flex flex-col gap-2 rounded-md border border-blue-line bg-blue-tint p-3.5 text-ink ${className}`.trim()}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Shield className="h-4 w-4 shrink-0 text-blue" aria-hidden="true" />
          <span className="text-[14px] font-black text-blue">
            {isOutput
              ? t('guardrailRedactedOutputTitle')
              : t('guardrailRedactedInputTitle')}
          </span>
        </div>
        <span className="rounded-pill border border-blue-line bg-paper px-2.5 py-0.5 text-[12px] font-bold text-blue">
          {t('guardrailMatchesPill', {
            direction: directionLabel,
            count: guardrail.matchCount,
          })}
        </span>
      </div>

      <p className="text-[13px] leading-[1.5] text-ink">{guardrail.summary}</p>

      {guardrail.turnId && onInspect ? (
        <div>
          <button
            type="button"
            onClick={() => onInspect(guardrail.turnId!)}
            className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm px-2 text-[13px] font-bold text-blue hover:bg-paper/70 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <span>{t('guardrailInspectTools')}</span>
            <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
      ) : null}
    </div>
  );
}
