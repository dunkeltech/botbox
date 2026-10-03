import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { Bell } from 'lucide-react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { StyleguideShowcase } from '@/components/dev/StyleguideShowcase';
import {
  Button,
  Card,
  Eyebrow,
  Input,
  StatusChip,
  type ChipStatus,
} from '@/components/ui';
import enMessages from '../../../../messages/en.json';

afterEach(() => {
  cleanup();
});

describe('UI primitives', () => {
  it('invokes onClick for enabled Button and ignores clicks when disabled', () => {
    const onEnabledClick = vi.fn();
    const onDisabledClick = vi.fn();

    render(
      <>
        <Button onClick={onEnabledClick} arrow>
          Save settings
        </Button>
        <Button disabled onClick={onDisabledClick}>
          Budget exhausted
        </Button>
        <Button variant="icon" aria-label="Open notifications">
          <Bell aria-hidden="true" />
        </Button>
      </>,
    );

    const enabled = screen.getByRole('button', { name: 'Save settings' });
    fireEvent.click(enabled);
    expect(onEnabledClick).toHaveBeenCalledTimes(1);

    const disabledBtn = screen.getByRole('button', {
      name: 'Budget exhausted',
    });
    expect(disabledBtn).toBeDisabled();
    fireEvent.click(disabledBtn);
    expect(onDisabledClick).not.toHaveBeenCalled();

    const iconBtn = screen.getByRole('button', { name: 'Open notifications' });
    expect(iconBtn).toHaveAttribute('aria-label', 'Open notifications');
  });

  it('associates Input label, readOnly state, and amber warning alert', () => {
    render(
      <>
        <Input
          label="Invited email"
          defaultValue="mara@dunkel.tech"
          helperText="Locked to invite"
          readOnly
          mono
        />
        <Input
          label="Daily spend cap"
          defaultValue="10.00"
          warning="Daily spend reached 84%"
        />
      </>,
    );

    const readonlyField = screen.getByLabelText('Invited email');
    expect(readonlyField).toHaveAttribute('readonly');
    expect(readonlyField.getAttribute('aria-describedby')).toBeTruthy();

    const warningField = screen.getByLabelText('Daily spend cap');
    expect(warningField).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Daily spend reached 84%',
    );
  });

  it('renders both dot and text label for every StatusChip state and handles invalid status safely', () => {
    const statuses: { status: ChipStatus; label: string }[] = [
      { status: 'working', label: 'Working' },
      { status: 'thinking', label: 'Thinking' },
      { status: 'idle', label: 'Idle' },
      { status: 'stopped', label: 'Stopped' },
      { status: 'budgetOk', label: 'Spend 61 % · OK' },
      { status: 'budgetWarning', label: 'Spend 84 % · Warning' },
      { status: 'budgetExceeded', label: 'Spend 100 % · Stopped' },
    ];

    render(
      <>
        {statuses.map((item) => (
          <StatusChip
            key={item.status}
            status={item.status}
            label={item.label}
          />
        ))}
        <StatusChip status={'unknown' as ChipStatus} label="Unknown fallback" />
      </>,
    );

    for (const item of statuses) {
      expect(screen.getByText(item.label)).toBeInTheDocument();
    }
    expect(screen.getByText('Unknown fallback')).toBeInTheDocument();
  });

  it('renders Eyebrow prefix and interactive Card', () => {
    render(
      <Card interactive data-testid="interactive-card">
        <Eyebrow>Fleet</Eyebrow>
      </Card>,
    );

    expect(screen.getByText('Fleet')).toBeInTheDocument();
    expect(screen.getByTestId('interactive-card')).toHaveClass(
      'hover:-translate-y-1',
    );
  });
});

describe('/dev/styleguide view', () => {
  it('renders all styleguide sections using next-intl messages without hardcoded strings', () => {
    const onError = vi.fn();

    render(
      <NextIntlClientProvider
        locale="en"
        messages={enMessages}
        onError={onError}
      >
        <StyleguideShowcase />
      </NextIntlClientProvider>,
    );

    expect(
      screen.getByRole('heading', { name: 'Living styleguide' }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: 'Palette & warning discipline' }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', {
        name: 'Lato scale & technical monospace',
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', {
        name: 'Buttons & touch targets (≥ 44 px)',
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', {
        name: 'Inputs, read-only & amber warnings',
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: 'Status & budget chips' }),
    ).toBeInTheDocument();

    expect(screen.getByText('#347a3b')).toBeInTheDocument();
    expect(screen.getByText('#b45309')).toBeInTheDocument();
    expect(onError).not.toHaveBeenCalled();
  });
});
