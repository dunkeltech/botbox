// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { BotKnowledgeBasePanel } from '@/components/bots/BotKnowledgeBasePanel';
import { MemorySavedCard } from '@/components/chat/MemorySavedCard';
import { describeToolMessage } from '@/components/chat/toolMessage';
import { setAccessToken } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';
import enMessages from '../../messages/en.json';

type BotMemory = components['schemas']['BotMemory'];
type BotMemorySummary = components['schemas']['BotMemorySummary'];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function renderWithIntl(ui: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      {ui}
    </NextIntlClientProvider>,
  );
}

const sampleEntries: BotMemory[] = [
  {
    id: 'mem-1',
    workspaceId: 'ws-1',
    botId: 'bot-1',
    title: 'Conventional Commits only',
    content: 'All git commits must use single-line imperative English format.',
    mode: 'always',
    tags: ['git', 'policy'],
    sourceKind: 'chat',
    sourceLabel: '#eng · turn #4',
    tokenEstimate: 18,
    createdAt: '2026-11-18T10:00:00Z',
    updatedAt: '2026-11-18T10:05:00Z',
  },
  {
    id: 'mem-2',
    workspaceId: 'ws-1',
    botId: 'bot-1',
    title: 'Staging deploy cluster',
    content: 'Staging services run on eu-west cluster via ArgoCD.',
    mode: 'on_demand',
    tags: ['infra'],
    sourceKind: 'manual',
    sourceLabel: 'Manual · Owner',
    tokenEstimate: 14,
    createdAt: '2026-11-18T09:00:00Z',
    updatedAt: '2026-11-18T09:30:00Z',
  },
];

const highBudgetSummary: BotMemorySummary = {
  autoSave: true,
  totalCount: 2,
  alwaysCount: 1,
  onDemandCount: 1,
  alwaysTokens: 3400,
  alwaysTokenLimit: 4000,
  maxEntries: 200,
};

function requestUrl(input: RequestInfo | URL): string {
  return typeof input === 'string'
    ? input
    : input instanceof URL
      ? input.toString()
      : input.url;
}

describe('BotKnowledgeBasePanel, MemorySavedCard, and tool trace labels', () => {
  beforeEach(() => {
    setAccessToken('jwt-kb-test');
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('renders budget warning, toggles auto-save policy, filters entries, and handles create errors', async () => {
    let currentAutoSave = true;
    let createAttempt = 0;

    const fetchMock = vi.fn(
      (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url =
          typeof input === 'string'
            ? input
            : input instanceof URL
              ? input.toString()
              : input.url;
        const method = (init?.method ?? 'GET').toUpperCase();

        if (
          url.startsWith('/api/workspaces/ws-1/bots/bot-1/memories?') &&
          method === 'GET'
        ) {
          return Promise.resolve(
            jsonResponse(
              {
                summary: { ...highBudgetSummary, autoSave: currentAutoSave },
                entries: sampleEntries,
                filteredCount: sampleEntries.length,
                tags: ['git', 'infra', 'policy'],
              },
              200,
            ),
          );
        }

        if (
          url === '/api/workspaces/ws-1/bots/bot-1/memories/policy' &&
          method === 'PUT'
        ) {
          const parsed = JSON.parse(
            typeof init?.body === 'string' ? init.body : '{}',
          ) as {
            autoSave: boolean;
          };
          currentAutoSave = parsed.autoSave;
          return Promise.resolve(
            jsonResponse({ ...highBudgetSummary, autoSave: currentAutoSave }),
          );
        }

        if (
          url === '/api/workspaces/ws-1/bots/bot-1/memories' &&
          method === 'POST'
        ) {
          createAttempt += 1;
          if (createAttempt === 1) {
            return Promise.resolve(
              jsonResponse(
                {
                  error: {
                    code: 'memory.secret_rejected',
                    message: 'secret detected',
                  },
                },
                400,
              ),
            );
          }
          return Promise.resolve(
            jsonResponse(
              {
                ...sampleEntries[1],
                id: 'mem-3',
                title: 'New deploy note',
              },
              201,
            ),
          );
        }

        if (
          url === '/api/workspaces/ws-1/bots/bot-1/memories/mem-1' &&
          method === 'DELETE'
        ) {
          return Promise.resolve(new Response(null, { status: 204 }));
        }

        if (
          url === '/api/workspaces/ws-1/bots/bot-1/memories' &&
          method === 'DELETE'
        ) {
          return Promise.resolve(jsonResponse({ deletedCount: 2 }, 200));
        }

        return Promise.resolve(jsonResponse({}, 404));
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    renderWithIntl(
      <BotKnowledgeBasePanel
        workspaceId="ws-1"
        botId="bot-1"
        botName="Atlas"
        canManage={true}
      />,
    );

    // Renders entries and >80% always-budget warning.
    expect(
      await screen.findByText('Conventional Commits only'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(enMessages.botSettings.knowledge.budgetWarning),
    ).toBeInTheDocument();
    expect(
      screen.getByText(enMessages.botSettings.knowledge.isolationFooter),
    ).toBeInTheDocument();

    // Toggling auto-save calls PUT /memories/policy.
    const policySwitch = screen.getByRole('switch', {
      name: enMessages.botSettings.knowledge.autoSaveLabel,
    });
    expect(policySwitch).toHaveAttribute('aria-checked', 'true');
    fireEvent.click(policySwitch);
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/bots/bot-1/memories/policy',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify({ autoSave: false }),
        }),
      );
    });

    // Filtering triggers query params.
    fireEvent.change(
      screen.getByPlaceholderText(
        enMessages.botSettings.knowledge.searchPlaceholder,
      ),
      { target: { value: 'ArgoCD' } },
    );
    fireEvent.click(screen.getByRole('button', { name: '#infra' }));
    await waitFor(() => {
      const calls = fetchMock.mock.calls.map(([u]) => requestUrl(u));
      expect(
        calls.some((u) => u.includes('q=ArgoCD') && u.includes('tag=infra')),
      ).toBe(true);
    });

    // Add memory modal rejects secret pattern and surfaces translated error, then succeeds cleanly.
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.botSettings.knowledge.addMemory,
      }),
    );
    fireEvent.change(
      screen.getByPlaceholderText(
        enMessages.botSettings.knowledge.fieldTitlePlaceholder,
      ),
      { target: { value: 'API Token Leak' } },
    );
    fireEvent.change(
      screen.getByPlaceholderText(
        enMessages.botSettings.knowledge.fieldContentPlaceholder,
      ),
      { target: { value: 'Token sk-ant-secretvalue12345678' } },
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.botSettings.knowledge.saveCreate,
      }),
    );

    expect(
      await screen.findByText(enMessages.errors.memorySecretRejected),
    ).toBeInTheDocument();

    fireEvent.change(
      screen.getByPlaceholderText(
        enMessages.botSettings.knowledge.fieldContentPlaceholder,
      ),
      { target: { value: 'Always verify ArgoCD sync status cleanly.' } },
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.botSettings.knowledge.saveCreate,
      }),
    );
    await waitFor(() => {
      expect(createAttempt).toBe(2);
    });

    // Clear danger zone triggers DELETE /memories.
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.botSettings.knowledge.clearButton,
      }),
    );
    fireEvent.click(
      await screen.findByRole('button', {
        name: enMessages.botSettings.knowledge.clearConfirm,
      }),
    );
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/bots/bot-1/memories',
        expect.objectContaining({ method: 'DELETE' }),
      );
    });
  });

  it('keeps Knowledge Base panel read-only when canManage is false', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          jsonResponse({
            summary: highBudgetSummary,
            entries: sampleEntries,
            filteredCount: 2,
            tags: ['git', 'infra'],
          }),
        ),
      ),
    );

    renderWithIntl(
      <BotKnowledgeBasePanel
        workspaceId="ws-1"
        botId="bot-1"
        botName="Atlas"
        canManage={false}
      />,
    );

    expect(
      await screen.findByText(enMessages.botSettings.knowledge.readOnlyNotice),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', {
        name: enMessages.botSettings.knowledge.addMemory,
      }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', {
        name: enMessages.botSettings.knowledge.clearButton,
      }),
    ).not.toBeInTheDocument();
  });

  it('renders MemorySavedCard states, supports Edit/Undo when allowed, and hides actions when read-only', async () => {
    const onUpdated = vi.fn();
    const fetchMock = vi.fn(() =>
      Promise.resolve(new Response(null, { status: 204 })),
    );
    vi.stubGlobal('fetch', fetchMock);

    const { rerender } = renderWithIntl(
      <MemorySavedCard
        workspaceId="ws-1"
        botId="bot-1"
        canManage={true}
        onUpdated={onUpdated}
        memory={{
          memoryId: 'mem-99',
          title: 'Deploy Tuesdays',
          content: 'Releases deploy Tuesdays at 14:00 UTC.',
          mode: 'always',
          tags: ['release', 'ops'],
          tokenEstimate: 12,
          sourceLabel: 'DM · turn #2',
          deleted: false,
        }}
      />,
    );

    expect(
      screen.getByText(enMessages.chat.memorySavedBadge),
    ).toBeInTheDocument();
    expect(screen.getByText('Deploy Tuesdays')).toBeInTheDocument();
    expect(screen.getByText('#release')).toBeInTheDocument();
    const openLink = screen.getByRole('link', {
      name: new RegExp(enMessages.chat.memoryOpenKnowledgeBase, 'i'),
    });
    expect(openLink).toHaveAttribute('href', '/bots/bot-1/settings/knowledge');

    // Undo deletes entry and triggers onUpdated.
    fireEvent.click(
      screen.getByRole('button', { name: enMessages.chat.memoryUndo }),
    );
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/bots/bot-1/memories/mem-99',
        expect.objectContaining({ method: 'DELETE' }),
      );
      expect(onUpdated).toHaveBeenCalledTimes(1);
    });

    // Read-only or deleted states hide Edit/Undo.
    rerender(
      <NextIntlClientProvider locale="en" messages={enMessages}>
        <MemorySavedCard
          workspaceId="ws-1"
          botId="bot-1"
          canManage={false}
          onUpdated={onUpdated}
          memory={{
            memoryId: 'mem-99',
            title: 'Deploy Tuesdays',
            content: 'Releases deploy Tuesdays at 14:00 UTC.',
            mode: 'on_demand',
            tags: [],
            tokenEstimate: 12,
            sourceLabel: 'DM · turn #2',
            deleted: true,
          }}
        />
      </NextIntlClientProvider>,
    );
    expect(
      screen.getByText(enMessages.chat.memoryRemovedBadge),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: enMessages.chat.memoryEdit }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: enMessages.chat.memoryUndo }),
    ).not.toBeInTheDocument();
  });

  it('describes memory tool calls and sanitized JSON outcomes cleanly', () => {
    expect(
      describeToolMessage(
        'tool',
        'mcp__botbox-memory__memory_save\n{"title":"Secret title","content":"Hidden content"}',
      ),
    ).toEqual({ key: 'toolMemorySave', image: false });

    expect(
      describeToolMessage('tool', 'memory_search\n{"query":"deploy"}'),
    ).toEqual({ key: 'toolMemorySearch', image: false });

    expect(
      describeToolMessage('tool', 'memory_update\n{"memory_id":"mem-1"}'),
    ).toEqual({ key: 'toolMemoryUpdate', image: false });

    expect(
      describeToolMessage('tool', 'memory_delete\n{"memory_id":"mem-1"}'),
    ).toEqual({ key: 'toolMemoryDelete', image: false });

    expect(
      describeToolMessage('tool', '{"saved":true,"memory_id":"mem-1"}'),
    ).toEqual({ key: 'toolMemorySaved', image: false });

    expect(
      describeToolMessage('tool', '{"count":1,"entries":[{"title":"x"}]}'),
    ).toEqual({ key: 'toolMemoryFound', image: false });

    expect(describeToolMessage('tool', '{"updated":true}')).toEqual({
      key: 'toolMemoryUpdated',
      image: false,
    });

    expect(describeToolMessage('tool', '{"deleted":true}')).toEqual({
      key: 'toolMemoryDeleted',
      image: false,
    });
  });
});
