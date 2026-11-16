'use client';

import { useEffect, useId, useRef, useState } from 'react';
import { useLocale } from 'next-intl';
import {
  formatFullDateTime,
  formatTimeLabel,
} from '@/components/chat/chatDate';

/**
 * Renders a short message timestamp (`<time>`) with an accessible hover/focus
 * tooltip displaying the full localized date and time.
 */
export function ChatTimestamp({
  iso,
  align = 'start',
  className = '',
}: {
  iso: string;
  /** Align tooltip left (`start`) or right (`end`, e.g. right-aligned user bubbles). */
  align?: 'start' | 'end';
  className?: string;
}) {
  const locale = useLocale();
  const tooltipId = useId();
  const triggerRef = useRef<HTMLTimeElement | null>(null);
  const [open, setOpen] = useState(false);
  const [placement, setPlacement] = useState<'above' | 'below'>('above');

  const timeLabel = formatTimeLabel(iso, locale);
  const fullLabel = formatFullDateTime(iso, locale);

  useEffect(() => {
    if (!open) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    }
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open]);

  if (!timeLabel) return null;

  function openWithPlacement() {
    const el = triggerRef.current;
    if (el && typeof el.getBoundingClientRect === 'function') {
      const rect = el.getBoundingClientRect();
      const log = el.closest('[role="log"]');
      const topBoundary = log ? log.getBoundingClientRect().top : 0;
      setPlacement(rect.top - topBoundary < 36 ? 'below' : 'above');
    }
    setOpen(true);
  }

  return (
    <span
      className="relative inline-flex items-center"
      onMouseEnter={openWithPlacement}
      onMouseLeave={() => setOpen(false)}
    >
      <time
        ref={triggerRef}
        dateTime={iso}
        tabIndex={0}
        aria-label={fullLabel}
        aria-describedby={open ? tooltipId : undefined}
        onFocus={openWithPlacement}
        onBlur={() => setOpen(false)}
        className={`cursor-default rounded-xs tabular-nums focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${className}`.trim()}
      >
        {timeLabel}
      </time>
      {open && fullLabel ? (
        <span
          className={`absolute z-30 ${
            placement === 'above' ? 'bottom-full pb-1' : 'top-full pt-1'
          } ${align === 'end' ? 'right-0' : 'left-0'}`}
        >
          <span
            id={tooltipId}
            role="tooltip"
            className="block whitespace-nowrap rounded-xs border border-line-strong/20 bg-terminal px-2 py-1 text-[11px] font-bold leading-tight text-paper shadow-card"
          >
            {fullLabel}
          </span>
        </span>
      ) : null}
    </span>
  );
}
