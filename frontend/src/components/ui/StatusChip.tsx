export type ChipStatus =
  | 'working'
  | 'thinking'
  | 'idle'
  | 'stopped'
  | 'budgetOk'
  | 'budgetWarning'
  | 'budgetExceeded';

export interface StatusChipProps {
  status: ChipStatus;
  /** Visible text label rendered next to the status indicator dot. */
  label: string;
  className?: string;
}

const STATUS_STYLE: Record<
  ChipStatus,
  { container: string; dot: string; dashed?: boolean }
> = {
  working: {
    container: 'bg-mint-soft text-green-600 border-mint-line',
    dot: 'bg-green',
  },
  thinking: {
    container: 'bg-blue-tint text-blue border-blue-line',
    dot: 'bg-blue',
  },
  idle: {
    container: 'bg-paper-2 text-muted border-line',
    dot: 'bg-on-dark-faint',
  },
  stopped: {
    container: 'bg-paper text-muted border-line',
    dot: 'bg-line-strong',
  },
  budgetOk: {
    container: 'bg-mint-soft text-green-600 border-mint-line',
    dot: 'bg-green',
  },
  budgetWarning: {
    container: 'bg-amber-tint text-amber border-amber-line',
    dot: 'bg-amber',
  },
  budgetExceeded: {
    container: 'bg-amber-tint text-amber border-amber-line',
    dot: 'bg-amber',
    dashed: true,
  },
};

/**
 * Status badge that always pairs an indicator dot with an explicit text label
 * so state is never communicated by colour alone.
 */
export function StatusChip({ status, label, className = '' }: StatusChipProps) {
  const style = STATUS_STYLE[status] ?? STATUS_STYLE.idle;
  const borderStyle = style.dashed ? 'border-dashed' : 'border-solid';

  return (
    <span
      className={`inline-flex items-center gap-2 rounded-pill border px-3 py-1 text-[13px] font-bold leading-[1.4] ${borderStyle} ${style.container} ${className}`.trim()}
    >
      <span
        aria-hidden="true"
        className={`h-2 w-2 shrink-0 rounded-pill ${style.dot}`}
      />
      <span>{label}</span>
    </span>
  );
}
