export interface BudgetMeterProps {
  /** Accessible label describing the budget progressbar. */
  ariaLabel: string;
  /** Spent amount in micros. */
  spentMicros: number;
  /** Budget limit in micros. Zero or negative disables percentage overflow. */
  limitMicros: number;
  /** True when usage reached the 80% warning threshold. */
  warning?: boolean;
  /** True when usage reached the 100% hard-stop cap. */
  exceeded?: boolean;
  /** Optional left-aligned caption below/above the track (e.g. "80% used"). */
  leftCaption?: string;
  /** Optional right-aligned threshold hint (e.g. "80% warn · 100% stop"). */
  rightCaption?: string;
  className?: string;
}

/**
 * Accessible budget progress meter implementing Stitch screen 382c2e3cc2854be796ffa8ff42c03345:
 * 80% warning and 100% cap tick markers, green-to-amber warning transition, and dashed exhaustion outline.
 */
export function BudgetMeter({
  ariaLabel,
  spentMicros,
  limitMicros,
  warning = false,
  exceeded = false,
  leftCaption,
  rightCaption,
  className = '',
}: BudgetMeterProps) {
  const safeLimit = limitMicros > 0 ? limitMicros : 0;
  const rawPercent =
    safeLimit > 0 ? Math.round((spentMicros / safeLimit) * 100) : 0;
  const clampedPercent = Math.min(100, Math.max(0, rawPercent));
  const isWarn = exceeded || warning || clampedPercent >= 80;

  return (
    <div className={`flex flex-col gap-1.5 ${className}`.trim()}>
      <div
        role="progressbar"
        aria-label={ariaLabel}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={clampedPercent}
        className={`relative h-2 w-full overflow-hidden rounded-pill bg-line ${
          exceeded ? 'ring-1 ring-dashed ring-amber-line' : ''
        }`}
      >
        <div
          className={`h-full rounded-pill transition-all duration-150 ${
            isWarn ? 'bg-amber' : 'bg-green'
          }`}
          style={{ width: `${clampedPercent}%` }}
        />
        <span
          aria-hidden="true"
          className="pointer-events-none absolute inset-y-0 left-[80%] w-[1.5px] bg-amber/70"
        />
        <span
          aria-hidden="true"
          className="pointer-events-none absolute inset-y-0 right-0 w-[1.5px] bg-ink/30"
        />
      </div>
      {leftCaption || rightCaption ? (
        <div className="flex flex-wrap items-center justify-between gap-2 font-mono text-[12px] text-muted">
          <span>{leftCaption}</span>
          <span className={isWarn ? 'font-bold text-amber' : undefined}>
            {rightCaption}
          </span>
        </div>
      ) : null}
    </div>
  );
}
