// SPDX-License-Identifier: AGPL-3.0-only

import {
  act,
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
  silenceExpectedVncClose,
  vncRetryDelayMs,
  type RFBClient,
} from '@/components/workspace/VncViewport';
import enMessages from '../../../../messages/en.json';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const STAGE_URL =
  'ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc?ticket=signed.ticket';

async function flushMicrotasks() {
  await act(async () => {
    await new Promise<void>((resolve) => {
      queueMicrotask(() => resolve());
    });
  });
}

function makeClient() {
  const listeners = new Map<string, () => void>();
  const client = {
    viewOnly: false,
    scaleViewport: false,
    disconnect: vi.fn(),
    addEventListener: (type: string, fn: () => void) => {
      listeners.set(type, fn);
    },
    _rfbConnectionState: 'connecting',
    fail() {
      client._rfbConnectionState = 'disconnected';
      listeners.get('disconnect')?.();
    },
    succeed() {
      client._rfbConnectionState = 'connected';
      listeners.get('connect')?.();
    },
  };
  return client;
}

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
    const onConnectFailed = vi.fn();

    const { unmount } = renderWithIntl(
      <VncViewport
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
        onControlChange={onControlChange}
        onConnectFailed={onConnectFailed}
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
    expect(onConnectFailed).not.toHaveBeenCalled();
  });

  it('does not disconnect again after the socket already closed', async () => {
    const disconnect = vi.fn(() => {
      throw new Error('Tried changing state of a disconnected RFB object');
    });
    const listeners = new Map<string, () => void>();
    const fakeClient = {
      viewOnly: false,
      scaleViewport: false,
      disconnect,
      addEventListener: (type: string, fn: () => void) => {
        listeners.set(type, fn);
      },
      _rfbConnectionState: 'connected',
    };
    const connectRFB = vi.fn(() => Promise.resolve(fakeClient));

    const { unmount } = renderWithIntl(
      <VncViewport
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
      />,
    );

    await waitFor(() => expect(connectRFB).toHaveBeenCalled());
    fakeClient._rfbConnectionState = 'disconnected';
    listeners.get('disconnect')?.();

    expect(() => unmount()).not.toThrow();
    expect(disconnect).not.toHaveBeenCalled();
    expect(connectRFB).toHaveBeenCalledTimes(1);
  });

  it('does not disconnect a client that closed before the effect was cancelled', async () => {
    const disconnect = vi.fn(() => {
      throw new Error('Tried changing state of a disconnected RFB object');
    });
    let resolveClient: (client: RFBClient) => void = () => {};
    const closedClient = {
      viewOnly: false,
      scaleViewport: false,
      disconnect,
      addEventListener: () => {},
      _rfbConnectionState: 'disconnected',
    } as RFBClient;
    const connectRFB = vi.fn(
      () =>
        new Promise<RFBClient>((resolve) => {
          resolveClient = resolve;
        }),
    );

    const { unmount } = renderWithIntl(
      <VncViewport
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
      />,
    );

    unmount();
    resolveClient(closedClient);
    await Promise.resolve();
    expect(disconnect).not.toHaveBeenCalled();
  });

  it('retries the same ticket after the socket closes before connect', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const clients = [makeClient(), makeClient(), makeClient(), makeClient()];
    let created = 0;
    const connectRFB = vi.fn(() => {
      const client = clients[created];
      created += 1;
      if (!client) throw new Error('unexpected extra connect');
      return Promise.resolve(client);
    });
    const onConnectFailed = vi.fn();

    const { unmount } = renderWithIntl(
      <VncViewport
        hideHeader
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
        onConnectFailed={onConnectFailed}
      />,
    );

    await flushMicrotasks();
    expect(connectRFB).toHaveBeenCalledTimes(1);
    expect(connectRFB).toHaveBeenCalledWith(
      screen.getByTestId('vnc-stage'),
      STAGE_URL,
    );

    const first = clients[0];
    if (!first) throw new Error('missing client');
    first.fail();
    expect(first.disconnect).not.toHaveBeenCalled();
    expect(onConnectFailed).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(499);
    });
    expect(connectRFB).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushMicrotasks();
    expect(connectRFB).toHaveBeenLastCalledWith(
      screen.getByTestId('vnc-stage'),
      STAGE_URL,
    );

    const second = clients[1];
    if (!second) throw new Error('missing client');
    second.fail();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(999);
    });
    expect(connectRFB).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushMicrotasks();
    expect(connectRFB).toHaveBeenCalledTimes(3);

    const third = clients[2];
    if (!third) throw new Error('missing client');
    third.succeed();
    third.fail();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(499);
    });
    expect(connectRFB).toHaveBeenCalledTimes(3);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushMicrotasks();
    expect(connectRFB).toHaveBeenCalledTimes(4);

    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(connectRFB).toHaveBeenCalledTimes(4);
    expect(onConnectFailed).toHaveBeenCalledTimes(3);
  });

  it('does not open another socket after unmount during backoff', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const disconnect = vi.fn(() => {
      throw new Error('Tried changing state of a disconnected RFB object');
    });
    const listeners = new Map<string, () => void>();
    const client = {
      viewOnly: false,
      scaleViewport: false,
      disconnect,
      addEventListener: (type: string, fn: () => void) => {
        listeners.set(type, fn);
      },
      _rfbConnectionState: 'connecting',
    };
    const connectRFB = vi.fn(() => Promise.resolve(client));
    const onConnectFailed = vi.fn();

    const { unmount } = renderWithIntl(
      <VncViewport
        hideHeader
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
        onConnectFailed={onConnectFailed}
      />,
    );

    await flushMicrotasks();
    client._rfbConnectionState = 'disconnected';
    listeners.get('disconnect')?.();
    expect(() => unmount()).not.toThrow();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    expect(connectRFB).toHaveBeenCalledTimes(1);
    expect(disconnect).not.toHaveBeenCalled();
    expect(onConnectFailed).toHaveBeenCalledTimes(1);
  });

  it('retries when the connector rejects and keeps the same url', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const client = makeClient();
    const connectRFB = vi
      .fn()
      .mockRejectedValueOnce(new Error('rfb import failed'))
      .mockResolvedValueOnce(client);
    const onConnectFailed = vi.fn();

    renderWithIntl(
      <VncViewport
        hideHeader
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
        onConnectFailed={onConnectFailed}
      />,
    );

    await flushMicrotasks();
    expect(connectRFB).toHaveBeenCalledTimes(1);
    expect(onConnectFailed).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(499);
    });
    expect(connectRFB).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushMicrotasks();
    expect(connectRFB).toHaveBeenNthCalledWith(
      2,
      screen.getByTestId('vnc-stage'),
      STAGE_URL,
    );
  });

  it('caps the retry delay', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const clients = Array.from({ length: 6 }, () => makeClient());
    let created = 0;
    const connectRFB = vi.fn(() => {
      const client = clients[created];
      created += 1;
      if (!client) throw new Error('unexpected extra connect');
      return Promise.resolve(client);
    });

    renderWithIntl(
      <VncViewport
        hideHeader
        wsUrl="ws://127.0.0.1:8000/ws/bots/ws-1/atlas/vnc"
        ticket="signed.ticket"
        connectRFB={connectRFB}
      />,
    );

    await flushMicrotasks();
    const delays = [500, 1000, 2000, 4000];
    for (const delay of delays) {
      clients[created - 1]?.fail();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(delay - 1);
      });
      const opened = created;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      await flushMicrotasks();
      expect(created).toBe(opened + 1);
    }

    clients[created - 1]?.fail();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3999);
    });
    expect(connectRFB).toHaveBeenCalledTimes(5);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushMicrotasks();
    expect(connectRFB).toHaveBeenCalledTimes(6);
  });
});

describe('vnc startup helpers', () => {
  it('doubles the retry delay until the cap', () => {
    expect(vncRetryDelayMs(0)).toBe(500);
    expect(vncRetryDelayMs(1)).toBe(1000);
    expect(vncRetryDelayMs(2)).toBe(2000);
    expect(vncRetryDelayMs(3)).toBe(4000);
    expect(vncRetryDelayMs(8)).toBe(4000);
    expect(vncRetryDelayMs(-1)).toBe(500);
  });

  it('drops the expected startup close and forwards other errors', () => {
    const report = vi.fn();
    const wrapped = silenceExpectedVncClose(report);

    wrapped('Failed when connecting: Connection closed (code: 1006)');
    expect(report).not.toHaveBeenCalled();

    wrapped('Failed when connecting: Something else');
    expect(report).toHaveBeenCalledWith(
      'Failed when connecting: Something else',
    );

    wrapped('Connection closed (code: 1006)');
    expect(report).toHaveBeenCalledWith('Connection closed (code: 1006)');
  });
});
