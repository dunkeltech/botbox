// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useEffect, useId, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { MarkdownMessage } from '@/components/chat/MarkdownMessage';

/** Shows a bounded Markdown role preview that expands to the full instructions. */
export function BotRoleDescription({
  content,
  label,
}: {
  content: string;
  label?: string;
}) {
  const t = useTranslations();
  const id = useId();
  const preview = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);

  useEffect(() => {
    const element = preview.current;
    if (!element || expanded) return;
    const measure = () => {
      setOverflowing(element.scrollHeight > element.clientHeight);
    };
    measure();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', measure);
      return () => window.removeEventListener('resize', measure);
    }
    // The inspector can resize independently of the window, and fonts can
    // change wrapping after the first render.
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    if (element.firstElementChild) observer.observe(element.firstElementChild);
    return () => observer.disconnect();
  }, [expanded]);

  return (
    <div
      role="region"
      aria-label={label ?? t('builder.descriptionLabel')}
      className="w-full min-w-0 text-[15px] leading-[1.55]"
    >
      <div
        id={id}
        ref={preview}
        className={expanded ? '' : 'max-h-[6.2em] overflow-hidden'}
        onFocusCapture={() => {
          // Keyboard users must see any Markdown link or code action they focus.
          if (overflowing) setExpanded(true);
        }}
      >
        <MarkdownMessage content={content} />
      </div>
      {overflowing ? (
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={id}
          onClick={() => setExpanded((value) => !value)}
          className="inline-flex min-h-11 min-w-11 items-center rounded-sm text-[14px] font-bold text-green-600 underline underline-offset-2 hover:text-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          {t(expanded ? 'team.roleLess' : 'team.roleMore')}
        </button>
      ) : null}
    </div>
  );
}
