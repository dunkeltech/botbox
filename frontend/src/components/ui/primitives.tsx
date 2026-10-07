'use client';

import { AlertTriangle, CheckCircle2, Info, X } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
  useEffect,
  useId,
  useRef,
} from 'react';

import { Button } from '@/components/ui/Button';
import type { WorkspaceRole } from '@/types/api';

/**
 * Shared bot.Box geometric hexagon logo mark + brand wordmark rendered with translated spans.
 */
export function BrandLogo({ dark = false }: { dark?: boolean }) {
  const common = useTranslations('common');

  return (
    <span className="inline-flex items-center gap-2.5">
      <svg
        width="28"
        height="28"
        viewBox="0 0 32 32"
        fill="none"
        aria-hidden="true"
        className="shrink-0"
      >
        <polygon
          points="16,2 28,9 28,23 16,30 4,23 4,9"
          className="fill-hex-2 stroke-hex-1"
          strokeWidth="2"
        />
        <polygon
          points="16,2 28,9 16,16 4,9"
          className="fill-hex-1 opacity-90"
        />
        <polygon points="16,16 28,9 28,23 16,30" className="fill-hex-3" />
      </svg>
      <span
        className={`text-[21px] font-black tracking-[-0.01em] ${dark ? 'text-paper' : 'text-ink'}`}
      >
        {common('brandLeft')}
        <span className={dark ? 'text-green-light' : 'text-green'}>
          {common('brandDot')}
        </span>
        {common('brandRight')}
      </span>
    </span>
  );
}

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label: string;
  helperText?: string;
  warning?: string;
  mono?: boolean;
}

/**
 * Accessible labelled multi-line textarea matching Input surface and validation styling.
 */
export function Textarea({
  label,
  helperText,
  warning,
  mono = false,
  id,
  readOnly = false,
  className = '',
  ...rest
}: TextareaProps) {
  const generatedId = useId();
  const fieldId = id ?? generatedId;
  const helperId = helperText ? `${fieldId}-helper` : undefined;
  const warningId = warning ? `${fieldId}-warning` : undefined;
  const describedBy =
    [helperId, warningId].filter(Boolean).join(' ') || undefined;

  const surfaceClasses = warning
    ? 'border-dashed border-amber-line bg-amber-tint text-ink'
    : readOnly
      ? 'border-line bg-paper-2 text-ink-2'
      : 'border-line-strong bg-paper text-ink focus:border-green-600';

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={fieldId} className="text-[14px] font-bold text-ink">
        {label}
      </label>
      <textarea
        id={fieldId}
        readOnly={readOnly}
        aria-invalid={warning ? true : undefined}
        aria-describedby={describedBy}
        className={`min-h-[96px] w-full rounded-sm border-[1.5px] px-3.5 py-2.5 text-[15px] leading-[1.5] transition duration-150 placeholder:text-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${mono ? 'font-mono text-[13px]' : ''} ${surfaceClasses} ${className}`.trim()}
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

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label: string;
  helperText?: string;
  warning?: string;
  mono?: boolean;
  hideLabel?: boolean;
}

/**
 * Accessible labelled native select control with 44 px minimum touch target.
 */
export function Select({
  label,
  helperText,
  warning,
  mono = false,
  hideLabel = false,
  id,
  className = '',
  children,
  ...rest
}: SelectProps) {
  const generatedId = useId();
  const fieldId = id ?? generatedId;
  const helperId = helperText ? `${fieldId}-helper` : undefined;
  const warningId = warning ? `${fieldId}-warning` : undefined;
  const describedBy =
    [helperId, warningId].filter(Boolean).join(' ') || undefined;

  const surfaceClasses = warning
    ? 'border-dashed border-amber-line bg-amber-tint text-ink'
    : 'border-line-strong bg-paper text-ink focus:border-green-600';

  return (
    <div className="flex flex-col gap-1.5">
      <label
        htmlFor={fieldId}
        className={hideLabel ? 'sr-only' : 'text-[14px] font-bold text-ink'}
      >
        {label}
      </label>
      <select
        id={fieldId}
        aria-invalid={warning ? true : undefined}
        aria-describedby={describedBy}
        className={`min-h-[44px] w-full rounded-sm border-[1.5px] px-3 py-2 text-[15px] transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${mono ? 'font-mono text-[13px]' : ''} ${surfaceClasses} ${className}`.trim()}
        {...rest}
      >
        {children}
      </select>
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

const ROLE_STYLES: Record<WorkspaceRole, string> = {
  owner: 'border-mint-line bg-mint-soft text-green-600',
  admin: 'border-blue-line bg-blue-tint text-blue',
  member: 'border-line bg-paper-2 text-ink-2',
  viewer: 'border-amber-line border-dashed bg-amber-tint text-amber',
};

/**
 * Semantic workspace role pill displaying translated text alongside token styling.
 */
export function RoleBadge({ role }: { role: WorkspaceRole }) {
  const tRoles = useTranslations('roles');

  return (
    <span
      className={`inline-flex items-center rounded-pill border px-2.5 py-0.5 text-[12px] font-bold ${ROLE_STYLES[role]}`}
    >
      {tRoles(role)}
    </span>
  );
}

export type AlertTone = 'warning' | 'info' | 'success';

export interface AlertProps {
  tone?: AlertTone;
  title?: string;
  children: ReactNode;
  action?: ReactNode;
  className?: string;
}

const ALERT_STYLES: Record<AlertTone, string> = {
  warning: 'border-dashed border-amber-line bg-amber-tint text-ink',
  info: 'border-blue-line bg-blue-tint text-ink',
  success: 'border-mint-line bg-mint-soft text-ink',
};

/**
 * Accessible alert banner strictly avoiding red (uses amber for warnings/errors per STYLEGUIDE.md).
 */
export function Alert({
  tone = 'warning',
  title,
  children,
  action,
  className = '',
}: AlertProps) {
  const Icon =
    tone === 'warning'
      ? AlertTriangle
      : tone === 'success'
        ? CheckCircle2
        : Info;
  const iconColor =
    tone === 'warning'
      ? 'text-amber'
      : tone === 'success'
        ? 'text-green-600'
        : 'text-blue';

  return (
    <div
      role={tone === 'warning' ? 'alert' : 'status'}
      className={`flex flex-col gap-2 rounded-sm border px-4 py-3 text-[14px] leading-[1.5] sm:flex-row sm:items-start sm:justify-between ${ALERT_STYLES[tone]} ${className}`.trim()}
    >
      <div className="flex items-start gap-2.5">
        <Icon
          className={`mt-0.5 h-4 w-4 shrink-0 ${iconColor}`}
          aria-hidden="true"
        />
        <div className="flex flex-col gap-0.5">
          {title ? <p className="font-bold text-ink">{title}</p> : null}
          <div className="text-ink-2">{children}</div>
        </div>
      </div>
      {action ? <div className="shrink-0">{action}</div> : null}
    </div>
  );
}

export interface RadioCardOption<T extends string> {
  value: T;
  title: string;
  description?: string;
  disabled?: boolean;
}

export interface RadioCardGroupProps<T extends string> {
  legend: string;
  name: string;
  value: T;
  onChange: (value: T) => void;
  options: Array<RadioCardOption<T>>;
  disabled?: boolean;
}

/**
 * Accessible radio card selector exposing 2-5 mutually exclusive choices as visible radio controls.
 */
export function RadioCardGroup<T extends string>({
  legend,
  name,
  value,
  onChange,
  options,
  disabled = false,
}: RadioCardGroupProps<T>) {
  return (
    <fieldset className="flex flex-col gap-2" disabled={disabled}>
      <legend className="text-[14px] font-bold text-ink">{legend}</legend>
      <div className="grid gap-3 sm:grid-cols-2">
        {options.map((opt) => {
          const selected = value === opt.value;
          const isDisabled = disabled || opt.disabled === true;
          return (
            <label
              key={opt.value}
              className={`flex min-h-[44px] cursor-pointer items-start gap-3 rounded-sm border-[1.5px] p-3.5 transition duration-150 ${
                selected
                  ? 'border-green bg-mint-soft'
                  : 'border-line-strong bg-paper hover:border-green-light'
              } ${isDisabled ? 'cursor-not-allowed opacity-70' : ''}`.trim()}
            >
              <input
                type="radio"
                name={name}
                value={opt.value}
                checked={selected}
                disabled={isDisabled}
                onChange={() => onChange(opt.value)}
                className="mt-1 h-4 w-4 shrink-0 accent-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              />
              <span className="flex flex-col gap-0.5">
                <span className="text-[14px] font-bold text-ink">
                  {opt.title}
                </span>
                {opt.description ? (
                  <span className="text-[13px] leading-[1.45] text-ink-2">
                    {opt.description}
                  </span>
                ) : null}
              </span>
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}

export interface ModalDialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  subtitle?: string;
  closeLabel: string;
  children: ReactNode;
}

/**
 * Accessible modal backed by the native HTML <dialog> top layer via showModal().
 * Does not apply redundant role="dialog" or aria-modal attributes.
 */
export function ModalDialog({
  open,
  onClose,
  title,
  subtitle,
  closeLabel,
  children,
}: ModalDialogProps) {
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) {
      return;
    }

    if (open) {
      if (typeof dialog.showModal === 'function') {
        if (!dialog.open) {
          dialog.showModal();
        }
      } else {
        dialog.setAttribute('open', '');
      }
    } else if (typeof dialog.close === 'function') {
      if (dialog.open) {
        dialog.close();
      }
    } else {
      dialog.removeAttribute('open');
    }
  }, [open]);

  if (!open) {
    return null;
  }

  return (
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target === dialogRef.current) {
          onClose();
        }
      }}
      className="m-auto w-[min(520px,calc(100vw-2rem))] rounded-md border border-line bg-paper p-0 text-ink shadow-card"
    >
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <h2
              id={titleId}
              className="text-[22px] font-black leading-[1.3] text-ink"
            >
              {title}
            </h2>
            {subtitle ? (
              <p className="text-[14px] leading-[1.5] text-ink-2">{subtitle}</p>
            ) : null}
          </div>
          <Button
            variant="icon"
            type="button"
            aria-label={closeLabel}
            onClick={onClose}
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </Button>
        </div>
        {children}
      </div>
    </dialog>
  );
}
