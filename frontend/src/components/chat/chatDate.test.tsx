// SPDX-License-Identifier: AGPL-3.0-only

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it } from 'vitest';
import { ChatDayDivider } from '@/components/chat/ChatDayDivider';
import { ChatTimestamp } from '@/components/chat/ChatTimestamp';
import {
  formatDayDividerLabel,
  formatFullDateTime,
  formatTimeLabel,
  isDayBoundary,
  localDayKey,
} from '@/components/chat/chatDate';
import messages from '../../../messages/en.json';

afterEach(() => {
  cleanup();
});

function renderWithIntl(child: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      {child}
    </NextIntlClientProvider>,
  );
}

describe('chatDate utilities', () => {
  it('computes localDayKey and handles invalid timestamps', () => {
    expect(localDayKey('')).toBe('');
    expect(localDayKey('not-a-date')).toBe('');
    const key = localDayKey('2026-11-15T10:30:00Z');
    expect(key).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('formats Today, Yesterday, and older calendar dates accurately', () => {
    const now = new Date(2026, 10, 19, 12, 30, 0);
    const todayIso = new Date(2026, 10, 19, 9, 15, 0).toISOString();
    const yesterdayIso = new Date(2026, 10, 18, 22, 45, 0).toISOString();
    const olderIso = new Date(2026, 10, 15, 14, 0, 0).toISOString();

    const labels = { today: 'Today', yesterday: 'Yesterday' };
    expect(formatDayDividerLabel(todayIso, 'en', labels, now)).toBe('Today');
    expect(formatDayDividerLabel(yesterdayIso, 'en', labels, now)).toBe(
      'Yesterday',
    );

    const olderLabel = formatDayDividerLabel(olderIso, 'en', labels, now);
    expect(olderLabel).not.toBe('Today');
    expect(olderLabel).toContain('2026');
    expect(olderLabel).toContain('15');
    expect(formatDayDividerLabel('bad', 'en', labels, now)).toBe('');
  });

  it('detects day boundaries across ordered messages and leading tool turns', () => {
    const day1a = new Date(2026, 10, 15, 9, 0, 0).toISOString();
    const day1b = new Date(2026, 10, 15, 18, 0, 0).toISOString();
    const day2 = new Date(2026, 10, 16, 8, 0, 0).toISOString();
    const rows = [
      { createdAt: day1a },
      { createdAt: day1b },
      { createdAt: day2 },
      { createdAt: 'invalid' },
    ];

    expect(isDayBoundary(rows, 0)).toBe(true);
    expect(isDayBoundary(rows, 0, localDayKey(day1a))).toBe(false);
    expect(isDayBoundary(rows, 1)).toBe(false);
    expect(isDayBoundary(rows, 2)).toBe(true);
    expect(isDayBoundary(rows, 3)).toBe(false);
  });
});

describe('ChatDayDivider and ChatTimestamp', () => {
  it('renders a separator with the message calendar date rather than Today for older days', () => {
    const olderIso = new Date(2026, 10, 15, 10, 0, 0).toISOString();
    renderWithIntl(<ChatDayDivider dateIso={olderIso} />);
    const sep = screen.getByRole('separator');
    expect(sep).toHaveTextContent('2026');
    expect(sep).not.toHaveTextContent('Today');
  });

  it('shows full date and time tooltip on hover and keyboard focus and dismisses on Escape', () => {
    const iso = new Date(2026, 10, 15, 10, 5, 30).toISOString();
    const expectedTime = formatTimeLabel(iso, 'en');
    const expectedFull = formatFullDateTime(iso, 'en');

    renderWithIntl(<ChatTimestamp iso={iso} />);
    const timeEl = screen.getByText(expectedTime);
    expect(timeEl.tagName).toBe('TIME');
    expect(timeEl).toHaveAttribute('dateTime', iso);
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();

    fireEvent.mouseEnter(timeEl);
    const tooltip = screen.getByRole('tooltip');
    expect(tooltip).toHaveTextContent(expectedFull);
    expect(timeEl).toHaveAttribute('aria-describedby', tooltip.id);

    fireEvent.keyDown(document, { key: 'Escape' });
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();

    fireEvent.focus(timeEl);
    expect(screen.getByRole('tooltip')).toHaveTextContent(expectedFull);
    fireEvent.blur(timeEl);
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
  });

  it('renders nothing for invalid timestamps', () => {
    const { container } = renderWithIntl(<ChatTimestamp iso="invalid-ts" />);
    expect(container).toBeEmptyDOMElement();
  });
});
