// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowRight } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { BotAvatar } from '@/components/ui';
import type { ToolTraceTurn } from '@/components/chat/toolTrace';

/**
 * Calm status strip shown above the composer while a bot is actively executing
 * tools, linking directly to the Tool Calls sidebar.
 */
export function LiveToolStatusBar({
  name,
  avatarSeed,
  statusText,
  liveTurn,
  onInspect,
}: {
  name: string;
  avatarSeed: string;
  statusText: string;
  liveTurn?: ToolTraceTurn;
  onInspect?: (turnKey: string) => void;
}) {
  const t = useTranslations('chat');
  const latestStep = liveTurn?.steps[liveTurn.steps.length - 1];
  const stepText = latestStep
    ? latestStep.callCopy.values
      ? t(latestStep.callCopy.key, latestStep.callCopy.values)
      : t(latestStep.callCopy.key)
    : null;

  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-blue-line bg-blue-tint px-3 py-2 text-[13px] font-bold text-blue"
    >
      <div className="flex min-w-0 items-center gap-2">
        <BotAvatar seed={avatarSeed} mood="thinking" size={22} decorative />
        <span className="truncate">
          {stepText
            ? t('toolLiveRunning', { name, step: stepText })
            : statusText}
        </span>
      </div>
      {liveTurn && onInspect ? (
        <button
          type="button"
          onClick={() => onInspect(liveTurn.turnKey)}
          className="inline-flex min-h-[32px] shrink-0 items-center gap-1 rounded-xs px-2 text-[12px] font-bold text-blue underline decoration-blue-line underline-offset-2 hover:bg-paper/60 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <span>{t('toolLiveInspect')}</span>
          <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      ) : null}
    </div>
  );
}
