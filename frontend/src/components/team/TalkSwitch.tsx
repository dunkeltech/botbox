'use client';

import { useId } from 'react';

/** 44 px switch. The track is smaller; the button is the touch target. */
export function TalkSwitch({
  checked,
  label,
  hint,
  onToggle,
  className = '',
}: {
  checked: boolean;
  label: string;
  hint?: string;
  onToggle: () => void;
  className?: string;
}) {
  const labelId = useId();
  return (
    <div className={`flex items-center gap-3 ${className}`.trim()}>
      <span className="min-w-0 flex-1">
        <span id={labelId} className="text-[14px] font-bold leading-[1.35]">
          {label}
        </span>
        {hint ? (
          <span className="mt-0.5 block text-[14px] font-normal leading-[1.4] text-muted">
            {hint}
          </span>
        ) : null}
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-labelledby={labelId}
        onClick={onToggle}
        className="inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
      >
        <span
          className={`flex h-7 w-12 items-center rounded-pill p-[3px] ${checked ? 'justify-end bg-green-600' : 'justify-start bg-line-strong'}`}
        >
          <span className="h-[22px] w-[22px] rounded-pill bg-paper shadow-card" />
        </span>
      </button>
    </div>
  );
}
