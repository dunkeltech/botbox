// SPDX-License-Identifier: AGPL-3.0-only

import { type InputHTMLAttributes, useId } from 'react';

export interface InputProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'size'
> {
  label: string;
  helperText?: string;
  warning?: string;
  mono?: boolean;
}

/**
 * Accessible labelled text input with optional monospace styling,
 * read-only styling, and amber warning feedback.
 */
export function Input({
  label,
  helperText,
  warning,
  mono = false,
  id,
  readOnly = false,
  className = '',
  ...rest
}: InputProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const helperId = helperText ? `${inputId}-helper` : undefined;
  const warningId = warning ? `${inputId}-warning` : undefined;
  const describedBy =
    [helperId, warningId].filter(Boolean).join(' ') || undefined;

  const surfaceClasses = warning
    ? 'border-dashed border-amber-line bg-amber-tint text-ink'
    : readOnly
      ? 'border-line bg-paper-2 text-ink-2'
      : 'border-line-strong bg-paper text-ink focus:border-green-600';

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={inputId} className="text-[14px] font-bold text-ink">
        {label}
      </label>
      <input
        id={inputId}
        readOnly={readOnly}
        aria-invalid={warning ? true : undefined}
        aria-describedby={describedBy}
        className={`min-h-[44px] w-full rounded-sm border-[1.5px] px-3.5 py-2 text-[15px] transition duration-150 placeholder:text-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${mono ? 'font-mono text-[13px]' : ''} ${surfaceClasses} ${className}`.trim()}
        {...rest}
      />
      {helperText ? (
        <p id={helperId} className="text-[14px] leading-[1.5] text-muted">
          {helperText}
        </p>
      ) : null}
      {warning ? (
        <p
          id={warningId}
          role="alert"
          className="rounded-xs border border-dashed border-amber-line bg-amber-tint px-3 py-1.5 text-[13px] font-bold text-amber"
        >
          {warning}
        </p>
      ) : null}
    </div>
  );
}
