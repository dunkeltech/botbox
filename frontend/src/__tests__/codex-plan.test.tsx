// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NextIntlClientProvider } from 'next-intl';
import type { ReactNode } from 'react';
import { afterEach, expect, it, vi } from 'vitest';

import { CodexPlanPanel } from '@/components/bots/CodexPlanPanel';
import { BudgetPanel } from '@/components/chat/BudgetPanel';
import enMessages from '../../messages/en.json';

function renderPanel(children: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    </NextIntlClientProvider>,
  );
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it('reveals only a workstation pairing code and refreshes connection status', async () => {
  let connected = false;
  const fetch = vi.fn((path: string, init?: RequestInit) => {
    if (path.endsWith('/pairing') && init?.method === 'POST') {
      connected = true;
      return Promise.resolve(json({ code: 'one-use-fixture' }));
    }
    return Promise.resolve(
      json({
        status: connected ? 'connected' : 'disconnected',
        ownedByYou: connected,
      }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  renderPanel(<CodexPlanPanel workspaceId="ws" botId="bot" />);
  fireEvent.click(
    await screen.findByRole('button', { name: 'Continue with ChatGPT' }),
  );
  expect(await screen.findByDisplayValue('one-use-fixture')).toHaveAttribute(
    'readonly',
  );
  expect(screen.getByText('make codex-login')).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'Check connection' }));
  expect(
    await screen.findByRole('button', { name: 'Disconnect' }),
  ).toBeVisible();
  expect(screen.queryByDisplayValue('one-use-fixture')).not.toBeInTheDocument();
});

it('keeps another user’s connection from being reassigned through the pairing form', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve(json({ status: 'connected', ownedByYou: false })),
    ),
  );
  renderPanel(<CodexPlanPanel workspaceId="ws" botId="bot" />);
  expect(await screen.findByText('Connected by another user')).toBeVisible();
  expect(
    screen.queryByRole('button', { name: 'Reconnect' }),
  ).not.toBeInTheDocument();
});

it('shows subscription tokens and estimated charges by period', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve(
        json({
          budgets: [],
          spendTodayMicros: 0,
          countSubscriptionUsage: true,
          subscriptionUsage: [
            {
              model: 'codex-fixture',
              period: 'month',
              inputTokens: 120,
              outputTokens: 60,
              cacheTokens: 15,
              internalChargeMicros: 230000,
              unknownRequests: 1,
            },
          ],
        }),
      ),
    ),
  );
  renderPanel(<BudgetPanel workspaceId="ws" botId="bot" />);
  expect(await screen.findByText('codex-fixture')).toBeVisible();
  expect(
    screen.getByText('120 input · 60 output · 15 cached input'),
  ).toBeVisible();
  expect(screen.getByText('Internal budget charge: $0.23')).toBeVisible();
  expect(screen.getByText('1 request with unknown usage')).toBeVisible();
  fireEvent.change(screen.getByLabelText('Subscription usage period'), {
    target: { value: 'day' },
  });
  expect(
    screen.getByText('No subscription usage in this period.'),
  ).toBeVisible();
});

it('lets workspace owners or admins enable internal subscription pricing', async () => {
  let enabled = false;
  const fetch = vi.fn((path: string, init?: RequestInit) => {
    if (
      path.endsWith('/subscription-budget-policy') &&
      init?.method === 'PUT'
    ) {
      expect(init.body).toBe('{"enabled":true}');
      enabled = true;
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return Promise.resolve(
      json({
        budgets: [],
        spendTodayMicros: 0,
        countSubscriptionUsage: enabled,
        subscriptionUsage: [],
      }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  renderPanel(<BudgetPanel workspaceId="ws" canManage />);
  const checkbox = await screen.findByRole('checkbox', {
    name: /Count subscription usage toward budgets/,
  });
  fireEvent.click(checkbox);
  await waitFor(() => expect(checkbox).toBeChecked());
  expect(fetch).toHaveBeenCalledWith(
    '/api/workspaces/ws/subscription-budget-policy',
    expect.objectContaining({ method: 'PUT', body: '{"enabled":true}' }),
  );
});
