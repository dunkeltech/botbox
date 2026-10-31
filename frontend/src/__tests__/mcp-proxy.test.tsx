import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { type ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { BotMCPProxyPanel } from '@/components/bots/BotMCPProxyPanel';
import { MCPServersPanel } from '@/components/settings/MCPServersPanel';
import { ApprovalCard } from '@/components/team/ApprovalCard';
import type { components } from '@/types/openapi.gen';
import enMessages from '../../messages/en.json';

type MCPServer = components['schemas']['MCPServer'];
type BotMCPConfigResponse = components['schemas']['BotMCPConfigResponse'];
type BotMCPServerPolicy = components['schemas']['BotMCPServerPolicy'];
type MCPCallLogListResponse = components['schemas']['MCPCallLogListResponse'];
type Approval = components['schemas']['Approval'];

function renderIntl(ui: ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      {ui}
    </NextIntlClientProvider>,
  );
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function requestUrl(input: RequestInfo | URL): string {
  if (typeof input === 'string') {
    return input;
  }
  if (input instanceof URL) {
    return input.toString();
  }
  return input.url;
}

function parseJsonBody(init?: RequestInit): Record<string, unknown> {
  if (typeof init?.body !== 'string' || init.body === '') {
    return {};
  }
  return JSON.parse(init.body) as Record<string, unknown>;
}

function makeServer(overrides: Partial<MCPServer> = {}): MCPServer {
  return {
    id: 'srv-linear',
    workspaceId: 'ws-1',
    slug: 'linear',
    name: 'Linear Issue Tracker',
    endpointUrl: 'https://mcp.linear.app/mcp',
    authScheme: 'bearer',
    authHeaderName: '',
    authUsername: '',
    hasSecret: true,
    maskedPreview: '••••89ab',
    tools: [
      {
        toolName: 'search_issues',
        description: 'Search Linear issues by query',
        inputSchema: { type: 'object' },
        discoveredAt: '2026-11-18T10:00:00Z',
      },
    ],
    lastSyncedAt: '2026-11-18T10:00:00Z',
    lastSyncError: '',
    createdAt: '2026-11-18T09:50:00Z',
    updatedAt: '2026-11-18T10:00:00Z',
    ...overrides,
  };
}

function makeBotServerPolicy(
  overrides: Partial<BotMCPServerPolicy> = {},
): BotMCPServerPolicy {
  return {
    serverId: 'srv-linear',
    serverSlug: 'linear',
    serverName: 'Linear Issue Tracker',
    endpointUrl: 'https://mcp.linear.app/mcp',
    authScheme: 'bearer',
    hasSecret: true,
    enabled: true,
    defaultPolicy: 'deny',
    revision: 3,
    acknowledgedRevision: 3,
    enforcementStatus: 'applied',
    toolPolicies: {
      search_issues: 'allow',
      update_issue: 'ask',
      create_issue: 'deny',
    },
    tools: [
      {
        toolName: 'search_issues',
        description: 'Search Linear issues',
        inputSchema: { type: 'object' },
        discoveredAt: '2026-11-18T10:00:00Z',
      },
      {
        toolName: 'update_issue',
        description: 'Update an existing issue',
        inputSchema: { type: 'object' },
        discoveredAt: '2026-11-18T10:00:00Z',
      },
      {
        toolName: 'create_issue',
        description: 'Create a new Linear issue',
        inputSchema: { type: 'object' },
        discoveredAt: '2026-11-18T10:00:00Z',
      },
    ],
    updatedAt: '2026-11-18T10:05:00Z',
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('MCPServersPanel', () => {
  it('allows owner/admin to register, sync, and remove servers without leaking secrets', async () => {
    const initialServer = makeServer();
    const syncedServer = makeServer({
      tools: [
        ...initialServer.tools,
        {
          toolName: 'create_issue',
          description: 'Create issue',
          inputSchema: { type: 'object' },
          discoveredAt: '2026-11-18T10:15:00Z',
        },
      ],
    });

    const recordedRequests: Array<{
      url: string;
      method: string;
      body?: Record<string, unknown>;
    }> = [];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = init?.method ?? 'GET';
        recordedRequests.push({
          url,
          method,
          body:
            typeof init?.body === 'string' ? parseJsonBody(init) : undefined,
        });

        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/mcp-servers')
        ) {
          return Promise.resolve(jsonResponse({ servers: [] }));
        }
        if (
          method === 'POST' &&
          url.endsWith('/api/workspaces/ws-1/mcp-servers')
        ) {
          return Promise.resolve(jsonResponse(initialServer, 201));
        }
        if (
          method === 'POST' &&
          url.endsWith('/api/workspaces/ws-1/mcp-servers/srv-linear/sync')
        ) {
          return Promise.resolve(jsonResponse(syncedServer, 200));
        }
        if (
          method === 'DELETE' &&
          url.endsWith('/api/workspaces/ws-1/mcp-servers/srv-linear')
        ) {
          return Promise.resolve(new Response(null, { status: 204 }));
        }
        return Promise.resolve(
          jsonResponse({ error: { code: 'not_found', message: 'nope' } }, 404),
        );
      }),
    );

    renderIntl(<MCPServersPanel workspaceId="ws-1" role="owner" />);

    expect(
      await screen.findByText('No workspace MCP servers registered yet.'),
    ).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Display name'), {
      target: { value: 'Linear Issue Tracker' },
    });
    fireEvent.change(screen.getByLabelText('Slug'), {
      target: { value: 'linear' },
    });
    fireEvent.change(screen.getByLabelText('Canonical HTTPS endpoint'), {
      target: { value: 'https://mcp.linear.app/mcp' },
    });
    fireEvent.change(screen.getByLabelText('Secret value (write-only)'), {
      target: { value: 'lin_api_secret_token' },
    });
    fireEvent.change(
      screen.getByLabelText('Initial tool names (optional, comma-separated)'),
      { target: { value: 'search_issues' } },
    );

    fireEvent.click(
      screen.getByRole('button', { name: 'Register MCP server' }),
    );

    expect(await screen.findByText('Linear Issue Tracker')).toBeInTheDocument();
    expect(screen.queryByText('lin_api_secret_token')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Secret value (write-only)')).toHaveValue('');

    const postCreate = recordedRequests.find(
      (req) => req.method === 'POST' && req.url.endsWith('/mcp-servers'),
    );
    expect(postCreate?.body).toMatchObject({
      name: 'Linear Issue Tracker',
      slug: 'linear',
      endpointUrl: 'https://mcp.linear.app/mcp',
      authScheme: 'bearer',
      secretValue: 'lin_api_secret_token',
      tools: [{ toolName: 'search_issues' }],
    });

    fireEvent.click(screen.getByRole('button', { name: 'Sync tools' }));
    expect(await screen.findByText('2 tools discovered')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', { name: 'Remove Linear Issue Tracker' }),
    );
    expect(
      await screen.findByText('No workspace MCP servers registered yet.'),
    ).toBeInTheDocument();
  });

  it('shows warning alert when sync fails and renders read-only for members', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = init?.method ?? 'GET';
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/mcp-servers')
        ) {
          return Promise.resolve(jsonResponse({ servers: [makeServer()] }));
        }
        if (
          method === 'POST' &&
          url.endsWith('/api/workspaces/ws-1/mcp-servers/srv-linear/sync')
        ) {
          return Promise.resolve(
            jsonResponse(
              {
                error: {
                  code: 'mcp.sync_failed',
                  message: 'upstream refused',
                },
              },
              502,
            ),
          );
        }
        return Promise.resolve(
          jsonResponse({ error: { code: 'not_found', message: 'nope' } }, 404),
        );
      }),
    );

    const { rerender } = renderIntl(
      <MCPServersPanel workspaceId="ws-1" role="admin" />,
    );
    expect(await screen.findByText('Linear Issue Tracker')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Sync tools' }));
    expect(
      await screen.findByText(enMessages.errors.mcpSyncFailed),
    ).toBeInTheDocument();

    rerender(
      <NextIntlClientProvider locale="en" messages={enMessages}>
        <MCPServersPanel workspaceId="ws-1" role="member" />
      </NextIntlClientProvider>,
    );
    expect(screen.getByText('Linear Issue Tracker')).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Sync tools' }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Register MCP server' }),
    ).not.toBeInTheDocument();
  });

  it('hides cleanly on 404 when ignoreMissing is true', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          jsonResponse(
            { error: { code: 'not_found', message: 'missing' } },
            404,
          ),
        ),
      ),
    );

    const { container } = renderIntl(
      <MCPServersPanel workspaceId="ws-1" role="owner" ignoreMissing />,
    );

    await waitFor(() => {
      expect(container).toBeEmptyDOMElement();
    });
  });
});

describe('BotMCPProxyPanel', () => {
  it('supports filtering, 3-state policy updates, saving with expectedRevision, and 409 conflict reload', async () => {
    const initialPolicy = makeBotServerPolicy();
    const updatedPolicy = makeBotServerPolicy({
      revision: 4,
      acknowledgedRevision: 4,
      toolPolicies: {
        search_issues: 'allow',
        update_issue: 'ask',
        create_issue: 'ask',
      },
    });

    let putAttempt = 0;
    const putBodies: Array<Record<string, unknown>> = [];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = init?.method ?? 'GET';
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp')
        ) {
          const payload: BotMCPConfigResponse = {
            botId: 'bot-1',
            workspaceId: 'ws-1',
            servers: [putAttempt === 0 ? initialPolicy : updatedPolicy],
          };
          return Promise.resolve(jsonResponse(payload));
        }
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp/logs')
        ) {
          const logs: MCPCallLogListResponse = {
            logs: [
              {
                id: 'log-1',
                botId: 'bot-1',
                serverId: 'srv-linear',
                serverSlug: 'linear',
                toolName: 'create_issue',
                action: 'blocked',
                statusCode: 403,
                latencyMs: 3,
                createdAt: '2026-11-18T10:04:00Z',
              },
            ],
          };
          return Promise.resolve(jsonResponse(logs));
        }
        if (
          method === 'PUT' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp/srv-linear')
        ) {
          putAttempt += 1;
          putBodies.push(parseJsonBody(init));
          if (putAttempt === 1) {
            return Promise.resolve(jsonResponse(updatedPolicy));
          }
          return Promise.resolve(
            jsonResponse(
              {
                error: {
                  code: 'policy_conflict',
                  message: 'conflict',
                },
              },
              409,
            ),
          );
        }
        return Promise.resolve(
          jsonResponse({ error: { code: 'not_found', message: 'nope' } }, 404),
        );
      }),
    );

    renderIntl(
      <BotMCPProxyPanel
        workspaceId="ws-1"
        botId="bot-1"
        botName="Researcher Bot"
        canManage
      />,
    );

    expect(await screen.findByText('Linear Issue Tracker')).toBeInTheDocument();
    expect(
      screen.getByText('1 Allowed · 1 Ask approval · 1 Blocked'),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('switch', { name: 'Assigned to Researcher Bot' }),
    ).toHaveAttribute('aria-checked', 'true');

    fireEvent.change(screen.getByLabelText('Filter discovered tools…'), {
      target: { value: 'create_issue' },
    });
    expect(screen.getByText('create_issue')).toBeInTheDocument();
    expect(screen.queryByText('search_issues')).not.toBeInTheDocument();

    const createGroup = screen.getByRole('group', { name: 'create_issue' });
    expect(
      within(createGroup).getByRole('button', { name: 'Block' }),
    ).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(
      within(createGroup).getByRole('button', { name: 'Ask approval' }),
    );
    expect(
      screen.getByText('1 Allowed · 2 Ask approval · 0 Blocked'),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Save MCP policy' }));
    expect(
      await screen.findByText('MCP command policy saved.'),
    ).toBeInTheDocument();

    expect(putBodies[0]).toEqual({
      enabled: true,
      defaultPolicy: 'deny',
      expectedRevision: 3,
      toolPolicies: {
        search_issues: 'allow',
        update_issue: 'ask',
        create_issue: 'ask',
      },
    });
    expect(screen.getByText('mcp__linear__create_issue')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Save MCP policy' }));
    expect(
      await screen.findByText(enMessages.errors.policyConflict),
    ).toBeInTheDocument();
  });

  it('supports log triage and enforces read-only view when canManage is false', async () => {
    const initialPolicy = makeBotServerPolicy();
    const putPayloads: Array<Record<string, unknown>> = [];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = init?.method ?? 'GET';
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp')
        ) {
          return Promise.resolve(
            jsonResponse({
              botId: 'bot-1',
              workspaceId: 'ws-1',
              servers: [initialPolicy],
            }),
          );
        }
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp/logs')
        ) {
          return Promise.resolve(
            jsonResponse({
              logs: [
                {
                  id: 'log-1',
                  botId: 'bot-1',
                  serverId: 'srv-linear',
                  serverSlug: 'linear',
                  toolName: 'create_issue',
                  action: 'blocked',
                  statusCode: 403,
                  latencyMs: 2,
                  createdAt: '2026-11-18T10:04:00Z',
                },
              ],
            }),
          );
        }
        if (
          method === 'PUT' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp/srv-linear')
        ) {
          putPayloads.push(parseJsonBody(init));
          return Promise.resolve(
            jsonResponse(
              makeBotServerPolicy({
                revision: 4,
                toolPolicies: {
                  ...initialPolicy.toolPolicies,
                  create_issue: 'allow',
                },
              }),
            ),
          );
        }
        return Promise.resolve(
          jsonResponse({ error: { code: 'not_found', message: 'nope' } }, 404),
        );
      }),
    );

    const { rerender } = renderIntl(
      <BotMCPProxyPanel
        workspaceId="ws-1"
        botId="bot-1"
        botName="Researcher Bot"
        canManage={false}
      />,
    );

    expect(
      await screen.findByText(enMessages.mcpProxy.botViewerNote),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('switch', { name: 'Assigned to Researcher Bot' }),
    ).toBeDisabled();
    expect(
      screen.queryByRole('button', { name: 'Save MCP policy' }),
    ).not.toBeInTheDocument();

    const readOnlyGroup = screen.getByRole('group', { name: 'create_issue' });
    expect(
      within(readOnlyGroup).getByRole('button', { name: 'Allow' }),
    ).toBeDisabled();
    const readOnlyLogRow = screen
      .getByText('linear/create_issue')
      .closest('tr');
    expect(readOnlyLogRow).not.toBeNull();
    expect(
      within(readOnlyLogRow!).queryByRole('button', { name: 'Allow' }),
    ).not.toBeInTheDocument();

    rerender(
      <NextIntlClientProvider locale="en" messages={enMessages}>
        <BotMCPProxyPanel
          workspaceId="ws-1"
          botId="bot-1"
          botName="Researcher Bot"
          canManage
        />
      </NextIntlClientProvider>,
    );

    const logCell = await screen.findByText('linear/create_issue');
    const logRow = logCell.closest('tr');
    expect(logRow).not.toBeNull();
    fireEvent.click(within(logRow!).getByRole('button', { name: 'Allow' }));

    await waitFor(() => {
      expect(putPayloads).toHaveLength(1);
    });
    expect(putPayloads[0]).toEqual({
      enabled: true,
      defaultPolicy: 'deny',
      expectedRevision: 3,
      toolPolicies: {
        search_issues: 'allow',
        update_issue: 'ask',
        create_issue: 'allow',
      },
    });
  });

  it('auto-enables server assignment switch when promoting a tool to Allow or Ask approval', async () => {
    const initialPolicy = makeBotServerPolicy({
      enabled: false,
      revision: 0,
      acknowledgedRevision: 0,
      enforcementStatus: 'not_running',
      toolPolicies: {},
    });

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const method = init?.method ?? 'GET';
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp')
        ) {
          return Promise.resolve(
            jsonResponse({
              botId: 'bot-1',
              workspaceId: 'ws-1',
              servers: [initialPolicy],
            }),
          );
        }
        if (
          method === 'GET' &&
          url.endsWith('/api/workspaces/ws-1/bots/bot-1/mcp/logs')
        ) {
          return Promise.resolve(jsonResponse({ logs: [] }));
        }
        return Promise.resolve(
          jsonResponse({ error: { code: 'not_found', message: 'nope' } }, 404),
        );
      }),
    );

    renderIntl(
      <BotMCPProxyPanel
        workspaceId="ws-1"
        botId="bot-1"
        botName="Mace Windu"
        canManage
      />,
    );

    const toggle = await screen.findByRole('switch', {
      name: 'Assigned to Mace Windu',
    });
    expect(toggle).toHaveAttribute('aria-checked', 'false');

    const getJobGroup = screen.getByRole('group', { name: 'create_issue' });
    fireEvent.click(within(getJobGroup).getByRole('button', { name: 'Allow' }));
    expect(toggle).toHaveAttribute('aria-checked', 'true');
  });

  it('hides cleanly on 404 when ignoreMissing is true', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          jsonResponse(
            { error: { code: 'not_found', message: 'missing' } },
            404,
          ),
        ),
      ),
    );

    const { container } = renderIntl(
      <BotMCPProxyPanel
        workspaceId="ws-1"
        botId="bot-1"
        botName="Researcher Bot"
        canManage
        ignoreMissing
      />,
    );

    await waitFor(() => {
      expect(container).toBeEmptyDOMElement();
    });
  });
});

describe('ApprovalCard mcp_tool variant', () => {
  it('renders mcp command chip, title, payload, and approve command action', () => {
    const onDecide = vi.fn();
    const approval: Approval = {
      id: 'apr-1',
      botId: 'bot-1',
      kind: 'mcp_tool',
      status: 'pending',
      label: 'linear/create_issue',
      createdAt: '2026-11-18T10:10:00Z',
    };

    renderIntl(
      <ApprovalCard
        approval={approval}
        botName="Researcher Bot"
        canDecide
        onDecide={onDecide}
      />,
    );

    expect(screen.getByText('mcp command')).toBeInTheDocument();
    expect(screen.getByText('MCP command approval')).toBeInTheDocument();
    expect(screen.getByText('linear/create_issue')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Approve command' }));
    expect(onDecide).toHaveBeenCalledWith('approved');
  });
});
