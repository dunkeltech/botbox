import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  VncViewport,
  type RFBClient,
} from '@/components/workspace/VncViewport';
import enMessages from '../../../../messages/en.json';

afterEach(() => {
  cleanup();
});

function renderWithIntl(ui: React.ReactElement) {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      {ui}
    </NextIntlClientProvider>,
  );
}

describe('VncViewport', () => {
  it('stays disconnected and refuses connection when ticket is missing', () => {
    const connectRFB = vi.fn();

    renderWithIntl(
      <VncViewport
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket=""
        connectRFB={connectRFB}
      />,
    );

    expect(connectRFB).not.toHaveBeenCalled();
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Missing signed stream ticket',
    );
    expect(screen.getByText('Disconnected')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Take control' })).toBeDisabled();
  });

  it('connects in viewOnly mode and toggles viewOnly when taking and releasing control', async () => {
    const disconnect = vi.fn();
    const listeners = new Map<string, () => void>();
    const fakeClient: RFBClient = {
      viewOnly: false,
      scaleViewport: false,
      disconnect,
      addEventListener: (type, fn) => {
        listeners.set(type, fn);
      },
    };
    const connectRFB = vi.fn(() => Promise.resolve(fakeClient));
    const onControlChange = vi.fn();

    const { unmount } = renderWithIntl(
      <VncViewport
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
        onControlChange={onControlChange}
      />,
    );

    await waitFor(() => {
      expect(connectRFB).toHaveBeenCalledWith(
        screen.getByTestId('vnc-stage'),
        'ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc?ticket=signed.ticket',
      );
    });

    expect(fakeClient.viewOnly).toBe(true);
    expect(fakeClient.scaleViewport).toBe(true);

    const toggle = screen.getByRole('button', { name: 'Take control' });
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(toggle);

    expect(fakeClient.viewOnly).toBe(false);
    expect(onControlChange).toHaveBeenCalledWith(true);
    expect(
      screen.getByRole('button', { name: 'Hand back to bot' }),
    ).toHaveAttribute('aria-pressed', 'true');

    unmount();
    expect(disconnect).toHaveBeenCalledTimes(1);
  });
});
