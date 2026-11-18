// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useLocale, useTranslations } from 'next-intl';
import { formatDayDividerLabel } from '@/components/chat/chatDate';

/** Horizontal calendar-day separator rendered between messages from different local days. */
export function ChatDayDivider({
  dateIso,
  className = '',
}: {
  dateIso: string;
  className?: string;
}) {
  const t = useTranslations('chat');
  const locale = useLocale();
  const label = formatDayDividerLabel(dateIso, locale, {
    today: t('todayDivider'),
    yesterday: t('yesterdayDivider'),
  });
  if (!label) return null;

  return (
    <div
      role="separator"
      aria-label={label}
      className={`my-1 flex items-center gap-3 ${className}`.trim()}
    >
      <span className="h-px flex-1 bg-line" />
      <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
        {label}
      </span>
      <span className="h-px flex-1 bg-line" />
    </div>
  );
}
