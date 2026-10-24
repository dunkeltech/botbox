import type { ReactNode } from 'react';

export interface SegmentedOption<T extends string> {
  value: T;
  label: ReactNode;
  badge?: ReactNode;
}

export interface SegmentedControlProps<T extends string> {
  ariaLabel: string;
  value: T;
  onChange: (value: T) => void;
  options: ReadonlyArray<SegmentedOption<T>>;
  size?: 'sm' | 'md';
  className?: string;
}

/**
 * STYLEGUIDE segmented control track using `dark` for the active button and `aria-pressed` semantics.
 */
export function SegmentedControl<T extends string>({
  ariaLabel,
  value,
  onChange,
  options,
  size = 'md',
  className = '',
}: SegmentedControlProps<T>) {
  const heightClass =
    size === 'sm' ? 'min-h-[38px] px-3' : 'min-h-[44px] px-3.5';

  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className={`inline-flex flex-wrap items-center gap-1 rounded-md border border-line bg-paper p-1 ${className}`.trim()}
    >
      {options.map((option) => {
        const active = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
            className={`inline-flex ${heightClass} items-center gap-1.5 rounded-sm text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
              active
                ? 'bg-dark text-paper'
                : 'bg-transparent text-ink-2 hover:bg-paper-2 hover:text-ink'
            }`}
          >
            <span>{option.label}</span>
            {option.badge !== undefined ? (
              <span
                className={`text-[13px] font-normal ${
                  active ? 'text-on-dark' : 'text-muted'
                }`}
              >
                {option.badge}
              </span>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}
