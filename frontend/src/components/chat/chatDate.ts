function padTwo(value: number): string {
  return String(value).padStart(2, '0');
}

const formatterCache = new Map<string, Intl.DateTimeFormat>();

function getFormatter(
  locale: string | undefined,
  kind: 'time' | 'full' | 'day',
  options: Intl.DateTimeFormatOptions,
): Intl.DateTimeFormat {
  const key = `${locale ?? ''}:${kind}`;
  let formatter = formatterCache.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(locale || undefined, options);
    formatterCache.set(key, formatter);
  }
  return formatter;
}

/** Parse an ISO timestamp string, returning null when invalid. */
export function parseValidDate(iso: string): Date | null {
  if (!iso) return null;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? null : date;
}

/** Return the local calendar day key (`YYYY-MM-DD`) for a timestamp, or `''` if invalid. */
export function localDayKey(iso: string): string {
  const date = parseValidDate(iso);
  if (!date) return '';
  return `${date.getFullYear()}-${padTwo(date.getMonth() + 1)}-${padTwo(date.getDate())}`;
}

/** Format a short local time string (HH:MM) for message timestamps. */
export function formatTimeLabel(iso: string, locale?: string): string {
  const date = parseValidDate(iso);
  if (!date) return '';
  return getFormatter(locale, 'time', {
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

/** Format the full localized date and time for timestamp hover/focus tooltips. */
export function formatFullDateTime(iso: string, locale?: string): string {
  const date = parseValidDate(iso);
  if (!date) return '';
  return getFormatter(locale, 'full', {
    weekday: 'short',
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date);
}

/**
 * Format a day divider label ("Today", "Yesterday", or localized calendar date)
 * in the caller's local calendar timezone.
 */
export function formatDayDividerLabel(
  iso: string,
  locale: string | undefined,
  labels: { today: string; yesterday: string },
  now: Date = new Date(),
): string {
  const date = parseValidDate(iso);
  if (!date) return '';

  const messageDay = localDayKey(iso);
  const todayDay = localDayKey(now.toISOString());
  if (messageDay === todayDay) {
    return labels.today;
  }

  const yesterdayDate = new Date(
    now.getFullYear(),
    now.getMonth(),
    now.getDate() - 1,
  );
  const yesterdayKey = `${yesterdayDate.getFullYear()}-${padTwo(yesterdayDate.getMonth() + 1)}-${padTwo(yesterdayDate.getDate())}`;
  if (messageDay === yesterdayKey) {
    return labels.yesterday;
  }

  return getFormatter(locale, 'day', {
    weekday: 'short',
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  }).format(date);
}

/**
 * Determine whether the message at `index` begins a new local calendar day
 * compared to the previous message (or `leadingDayKey` for index 0).
 */
export function isDayBoundary(
  messages: ReadonlyArray<{ createdAt: string }>,
  index: number,
  leadingDayKey = '',
): boolean {
  const current = localDayKey(messages[index]?.createdAt ?? '');
  if (!current) return false;
  if (index === 0) {
    return current !== leadingDayKey;
  }
  const previous = localDayKey(messages[index - 1]?.createdAt ?? '');
  return current !== previous;
}
