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
import { SubscriptionReconciliationPanel } from '@/components/settings/SubscriptionReconciliationPanel';
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

it('validates cached input and reconciles a request with an explicitly estimated token count', async () => {
  let settled = false;
  const fetch = vi.fn((path: string, init?: RequestInit) => {
    if (path.endsWith('/unresolved')) {
      return Promise.resolve(
        json(
          settled
            ? []
            : [
                {
                  requestId: 'request-1',
                  botName: 'Mace Windu',
                  model: 'gpt-fixture',
                  createdAt: '2026-11-19T19:46:47Z',
                },
              ],
        ),
      );
    }
    if (path.endsWith('/reconcile') && init?.method === 'POST') {
      settled = true;
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return Promise.resolve(json({}));
  });
  vi.stubGlobal('fetch', fetch);
  renderPanel(<SubscriptionReconciliationPanel workspaceId="ws" />);
  fireEvent.click(await screen.findByRole('button', { name: 'Reconcile' }));
  fireEvent.change(screen.getByLabelText('Provider'), {
    target: { value: 'openai' },
  });
  fireEvent.change(screen.getByLabelText('Basis'), {
    target: { value: 'operator_estimate' },
  });
  fireEvent.change(screen.getByLabelText('Input tokens (including cached)'), {
    target: { value: '1' },
  });
  fireEvent.change(screen.getByLabelText('Output tokens'), {
    target: { value: '0' },
  });
  fireEvent.change(screen.getByLabelText('Cached input tokens'), {
    target: { value: '2' },
  });
  const save = screen.getByRole('button', { name: 'Save adjustment' });
  fireEvent.click(save);
  expect(
    await screen.findByText(
      enMessages.workspaceSettings.subscriptionReconciliation.invalid,
    ),
  ).toBeVisible();
  expect(settled).toBe(false);

  fireEvent.change(screen.getByLabelText('Cached input tokens'), {
    target: { value: '0' },
  });
  fireEvent.click(save);
  expect(
    await screen.findByText('Subscription usage reconciled.'),
  ).toBeVisible();
  expect(
    await screen.findByText('No unresolved subscription requests.'),
  ).toBeVisible();
  expect(fetch).toHaveBeenCalledWith(
    '/api/workspaces/ws/subscription-usage/request-1/reconcile',
    expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        provider: 'openai',
        basis: 'operator_estimate',
        inputTokens: 1,
        outputTokens: 0,
        cacheTokens: 0,
      }),
    }),
  );
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

it('allows workspace admins to choose workspace scope and notifies secret listeners', async () => {
  let connected = false;
  const onSecretsChanged = vi.fn();
  const fetch = vi.fn((path: string, init?: RequestInit) => {
    if (path.endsWith('/pairing') && init?.method === 'POST') {
      expect(init.body).toBe(JSON.stringify({ secretScope: 'workspace' }));
      connected = true;
      return Promise.resolve(json({ code: 'workspace-pair-code' }));
    }
    if (path.endsWith('/codex-plan') && init?.method === 'DELETE') {
      connected = false;
      return Promise.resolve(json({ revocationConfirmed: true }));
    }
    return Promise.resolve(
      json({
        status: connected ? 'connected' : 'disconnected',
        ownedByYou: connected,
        secretScope: connected ? 'workspace' : undefined,
      }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  renderPanel(
    <CodexPlanPanel
      workspaceId="ws"
      botId="bot"
      canManageWorkspaceSecrets
      onSecretsChanged={onSecretsChanged}
    />,
  );
  const scopeSelect = await screen.findByLabelText(
    enMessages.codexPlan.secretScopeLabel,
  );
  fireEvent.change(scopeSelect, { target: { value: 'workspace' } });
  fireEvent.click(
    screen.getByRole('button', { name: enMessages.codexPlan.connect }),
  );
  expect(await screen.findByDisplayValue('workspace-pair-code')).toBeVisible();
  fireEvent.click(
    screen.getByRole('button', { name: enMessages.codexPlan.check }),
  );
  expect(
    await screen.findByText(enMessages.codexPlan.connectedWorkspace),
  ).toBeVisible();
  expect(onSecretsChanged).toHaveBeenCalledTimes(1);

  fireEvent.click(
    screen.getByRole('button', { name: enMessages.codexPlan.disconnect }),
  );
  await waitFor(() =>
    expect(screen.getByText(enMessages.codexPlan.disconnected)).toBeVisible(),
  );
  expect(onSecretsChanged).toHaveBeenCalledTimes(2);
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
