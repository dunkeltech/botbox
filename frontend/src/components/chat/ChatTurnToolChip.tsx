// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowRight, Terminal } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
  formatToolDuration,
  type ToolTraceTurn,
} from '@/components/chat/toolTrace';

/**
 * Compact turn summary chip placed beneath a bot reply (or as an inline anchor
 * for tool-only turns) to open and focus that turn in the Tool Calls sidebar.
 */
export function ChatTurnToolChip({
  turn,
  selected = false,
  onInspect,
}: {
  turn: ToolTraceTurn;
  selected?: boolean;
  onInspect: (turnKey: string) => void;
}) {
  const t = useTranslations('chat');
  const runningCount = turn.steps.filter((s) => s.status === 'running').length;
  const guardrailRedacts = turn.steps.filter(
    (s) => s.guardrail?.action === 'redacted',
  ).length;
  const guardrailBlocks = turn.steps.filter(
    (s) => s.guardrail?.action === 'blocked',
  ).length;
  const duration = formatToolDuration(turn.durationMs);

  let label: string;
  if (runningCount > 0) {
    label = t('toolRunningCount', { count: runningCount });
  } else if (duration) {
    label = t('toolStepCountWithDuration', {
      count: turn.steps.length,
      duration,
    });
  } else {
    label = t('toolStepCount', { count: turn.steps.length });
  }

  const guardrailParts: string[] = [];
  if (guardrailRedacts > 0) {
    guardrailParts.push(
      t('guardrailRedactedSummary', { count: guardrailRedacts }),
    );
  }
  if (guardrailBlocks > 0) {
    guardrailParts.push(
      t('guardrailBlockedSummary', { count: guardrailBlocks }),
    );
  }
  if (guardrailParts.length > 0) {
    label = `${label} · ${guardrailParts.join(' · ')}`;
  }

  const toneClass =
    turn.status === 'error'
      ? selected
        ? 'border-amber bg-amber-tint text-amber'
        : 'border-amber-line bg-amber-tint/70 text-amber hover:border-amber'
      : turn.status === 'running'
        ? selected
          ? 'border-blue bg-blue-tint text-blue'
          : 'border-blue-line bg-blue-tint/70 text-blue hover:border-blue'
        : selected
          ? 'border-green bg-mint-soft text-green-600'
          : 'border-line bg-paper-2 text-ink-2 hover:border-mint-line hover:bg-mint-soft';

  return (
    <button
      type="button"
      onClick={() => onInspect(turn.turnKey)}
      aria-pressed={selected}
      className={`mt-2 inline-flex min-h-[36px] items-center gap-2 rounded-sm border px-3 py-1.5 text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${toneClass}`}
    >
      <Terminal className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span className="font-mono">{label}</span>
      <ArrowRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    </button>
  );
}
