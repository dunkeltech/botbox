// SPDX-License-Identifier: AGPL-3.0-only

import { afterEach, expect, it, vi } from 'vitest';
import { connectConversation } from './conversation-events';

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it('reconnects, reconciles snapshots, and ignores events after disposal', () => {
  vi.useFakeTimers();
  class Socket {
    static all: Socket[] = [];
    onmessage?: (event: MessageEvent<string>) => void;
    onopen?: () => void;
    onclose?: () => void;
    close = vi.fn();
    constructor(
      _url: string,
      readonly protocols: string[],
    ) {
      Socket.all.push(this);
    }
  }
  vi.stubGlobal('WebSocket', Socket);
  const message = vi.fn();
  const refresh = vi.fn();
  let token = 'fixture-token';
  const stop = connectConversation(
    'ws',
    'group',
    () => token,
    message,
    refresh,
  );
  const first = Socket.all[0]!;
  first.onopen?.();
  expect(refresh).toHaveBeenCalledTimes(1);
  token = 'refreshed-token';
  first.onclose?.();
  vi.advanceTimersByTime(1000);
  expect(Socket.all).toHaveLength(2);
  const second = Socket.all[1]!;
  expect(second.protocols).toContain('botbox.auth.refreshed-token');
  first.onmessage?.({ data: '{}' } as MessageEvent<string>);
  second.onopen?.();
  expect(refresh).toHaveBeenCalledTimes(2);
  stop();
  second.onmessage?.({ data: '{}' } as MessageEvent<string>);
  second.onclose?.();
  vi.advanceTimersByTime(5000);
  expect(message).not.toHaveBeenCalled();
  expect(Socket.all).toHaveLength(2);
  expect(second.close).toHaveBeenCalledOnce();
});
