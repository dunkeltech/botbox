import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { rfbInstances } = vi.hoisted(() => ({
  rfbInstances: [] as Array<{
    url: string;
    viewOnly: boolean;
    scaleViewport: boolean;
    emit: (event: string) => void;
    disconnected: boolean;
  }>,
}));

vi.mock('@novnc/novnc', () => ({
  default: class MockRFB {
    readonly url: string;
    viewOnly = false;
    scaleViewport = false;
    disconnected = false;
    private readonly listeners = new Map<string, () => void>();

    constructor(_target: HTMLElement, url: string) {
      this.url = url;
      rfbInstances.push(this);
      queueMicrotask(() => {
        this.listeners.get('connect')?.();
      });
    }

    emit = (event: string) => {
      this.listeners.get(event)?.();
    };

    disconnect() {
      this.disconnected = true;
    }

    addEventListener(type: string, listener: () => void) {
      this.listeners.set(type, listener);
    }
  },
}));

const mockPush = vi.fn();
const mockReplace = vi.fn();
let mockPathname = '/bots/bot-1';
let mockParams: Record<string, string> = { botId: 'bot-1' };

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: mockPush,
    replace: mockReplace,
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => mockPathname,
  useParams: () => mockParams,
}));

import BotWorkspacePage from '@/app/bots/[botId]/page';
import BotSettingsPage from '@/app/bots/[botId]/settings/page';
import FleetPage from '@/app/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  Bot,
  EnvironmentStateResponse,
  FileContentResponse,
  FileListResponse,
  StreamTicketResponse,
  TerminalExecResponse,
  WorkspaceSummary,
} from '@/types/api';
import enMessages from '../../messages/en.json';

function renderWithProviders(ui: ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={enMessages}>
      <QueryProvider>
        <AuthProvider>{ui}</AuthProvider>
      </QueryProvider>
    </NextIntlClientProvider>,
  );
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function makeWorkspace(
  overrides: Partial<WorkspaceSummary> = {},
): WorkspaceSummary {
  return {
    id: 'ws-1',
    name: 'Operations Lab',
    slug: 'operations-lab',
    defaultDriver: 'docker',
    defaultLocale: 'en',
    role: 'owner',
    createdAt: '2026-11-14T12:00:00Z',
    ...overrides,
  };
}

function makeSession(
  overrides: Partial<AuthSessionResponse> = {},
): AuthSessionResponse {
  const ws = makeWorkspace();
  return {
    user: {
      id: 'user-1',
      email: 'mara@dunkel.tech',
      displayName: 'Mara Lindqvist',
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces: [ws],
    activeWorkspace: ws,
    accessToken: 'jwt-phase2-token',
    ...overrides,
  };
}

function makeBot(overrides: Partial<Bot> = {}): Bot {
  return {
    id: 'bot-1',
    workspaceId: 'ws-1',
    ownerUserId: 'user-1',
    name: 'Atlas',
    description: 'Verifies release pipelines on a Linux desktop.',
    avatarSeed: 'atlas-005a',
    llmMode: 'cli',
    llmProvider: 'anthropic',
    llmModel: 'claude-sonnet-4-20250514',
    driverType: 'docker',
    replyLanguage: 'en',
    status: 'idle',
    mood: 'idle',
    createdAt: '2026-11-14T12:00:00Z',
    updatedAt: '2026-11-14T12:00:00Z',
    ...overrides,
  };
}

function makeEnv(
  overrides: Partial<EnvironmentStateResponse> = {},
): EnvironmentStateResponse {
  return {
    botId: 'bot-1',
    workspaceId: 'ws-1',
    driver: 'memory',
    envId: 'env-1',
    status: 'running',
    image: 'botbox/sandbox:dev',
    cpuMillis: 2000,
    memoryMiB: 4096,
    volumeRef: 'vol-bot-1',
    controlHolder: null,
    controlNote: '',
    stats: {
      cpuPercent: 24,
      memoryUsedMiB: 1024,
      memoryLimitMiB: 4096,
      volumeUsedMiB: 512,
      volumeLimitMiB: 10240,
    },
    watchers: [{ userId: 'user-1', displayName: 'Mara Lindqvist' }],
    startedAt: '2026-11-15T09:00:00Z',
    updatedAt: '2026-11-15T09:01:00Z',
    ...overrides,
  };
}

interface RecordedCall {
  url: string;
  method: string;
  headers: Headers;
  body: string | null;
}

type FetchHandler = (
  url: string,
  method: string,
  headers: Headers,
  body: string | null,
) => Promise<Response> | Response;

function installFetchMock(handler: FetchHandler): RecordedCall[] {
  const calls: RecordedCall[] = [];
  const fetchSpy = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method = (init?.method ?? 'GET').toUpperCase();
      const headers = new Headers(init?.headers);
      const body = typeof init?.body === 'string' ? init.body : null;
      calls.push({ url, method, headers, body });
      return handler(url, method, headers, body);
    },
  );
  vi.stubGlobal('fetch', fetchSpy);
  return calls;
}

beforeEach(() => {
  rfbInstances.length = 0;
  mockPush.mockReset();
  mockReplace.mockReset();
  mockPathname = '/bots/bot-1';
  mockParams = { botId: 'bot-1' };
  setAccessToken(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('Phase 2 Bot Workspace, Terminal, Files, Control Lock & Fleet controls', () => {
  it('starts stopped sandbox, loads VNC ticket, takes control, and releases with a hand-back note', async () => {
    const session = makeSession();
    const bot = makeBot({ status: 'stopped', mood: 'offline' });
    let env = makeEnv({ status: 'stopped', envId: '', startedAt: null });

    const calls = installFetchMock((url, method, _headers, body) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment/start' &&
        method === 'POST'
      ) {
        env = makeEnv({ status: 'running', envId: 'env-started-1' });
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        const ticket: StreamTicketResponse = {
          ticket: env.controlHolder ? 'ticket.ctrl' : 'ticket.view',
          wsPath: '/ws/bots/ws-1/bot-1/vnc',
          canControl: Boolean(env.controlHolder),
          expiresAt: '2026-11-15T10:00:00Z',
        };
        return jsonResponse(ticket, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/control/take' &&
        method === 'POST'
      ) {
        env = makeEnv({
          status: 'running',
          envId: 'env-started-1',
          controlHolder: {
            userId: session.user.id,
            displayName: session.user.displayName,
            email: session.user.email,
          },
        });
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/control/release' &&
        method === 'POST'
      ) {
        const parsed = body ? (JSON.parse(body) as { note?: string }) : {};
        env = makeEnv({
          status: 'running',
          envId: 'env-started-1',
          controlHolder: null,
          controlNote: parsed.note ?? '',
        });
        return jsonResponse(env, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotWorkspacePage />);

    expect(
      await screen.findByText(enMessages.workspace.computer.stoppedTitle),
    ).toBeInTheDocument();
    expect(screen.queryByTestId('vnc-stage')).not.toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.startSandbox,
      }),
    );

    expect(await screen.findByTestId('vnc-stage')).toBeInTheDocument();
    await waitFor(() => {
      expect(rfbInstances.length).toBeGreaterThanOrEqual(1);
    });
    expect(rfbInstances[0]?.viewOnly).toBe(true);
    expect(screen.getByText('botbox/sandbox:dev')).toBeInTheDocument();
    expect(screen.getByText('24%')).toBeInTheDocument();
    expect(screen.getByText('1024 MiB')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.takeControl,
      }),
    );

    expect(
      await screen.findByText(
        enMessages.workspace.computer.takeoverBannerTitle.replace(
          '{name}',
          'Atlas',
        ),
      ),
    ).toBeInTheDocument();

    await waitFor(() => {
      const latestRfb = rfbInstances[rfbInstances.length - 1];
      expect(latestRfb?.viewOnly).toBe(false);
    });

    fireEvent.change(
      screen.getByLabelText(enMessages.workspace.computer.handbackNoteLabel),
      { target: { value: 'Logged into staging Odoo' } },
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: /Hand back to Atlas/,
      }),
    );

    expect(
      await screen.findByText('Logged into staging Odoo'),
    ).toBeInTheDocument();

    const releaseCall = calls.find(
      (c) => c.url === '/api/workspaces/ws-1/bots/bot-1/control/release',
    );
    expect(releaseCall?.headers.get('X-BotBox-CSRF')).toBe('1');
    expect(releaseCall?.body).toBe(
      JSON.stringify({ note: 'Logged into staging Odoo' }),
    );
  });

  it('executes commands on the Terminal tab and lists, reads, edits, and saves files on the Files tab', async () => {
    const session = makeSession();
    const bot = makeBot();
    const env = makeEnv({ status: 'running' });
    let savedContent = 'initial notes\n';

    const calls = installFetchMock((url, method, _headers, body) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        return jsonResponse(
          {
            ticket: 't1',
            wsPath: '/ws/bots/ws-1/bot-1/vnc',
            canControl: false,
            expiresAt: '2026-11-15T10:00:00Z',
          } satisfies StreamTicketResponse,
          200,
        );
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/terminal/exec' &&
        method === 'POST'
      ) {
        const req = body
          ? (JSON.parse(body) as { command: string })
          : { command: '' };
        const resp: TerminalExecResponse =
          req.command === 'false'
            ? {
                stdout: '',
                stderr: 'command failed\n',
                exitCode: 2,
                cwd: '/home/bot',
              }
            : {
                stdout: 'hello\n',
                stderr: '',
                exitCode: 0,
                cwd: '/home/bot',
              };
        return jsonResponse(resp, 200);
      }
      if (
        url.startsWith('/api/workspaces/ws-1/bots/bot-1/files?') &&
        method === 'GET'
      ) {
        const listResp: FileListResponse = {
          path: '/home/bot',
          entries: [
            {
              name: 'projects',
              path: '/home/bot/projects',
              isDir: true,
              sizeBytes: 0,
              updatedAt: '2026-11-15T09:00:00Z',
            },
            {
              name: 'notes.txt',
              path: '/home/bot/notes.txt',
              isDir: false,
              sizeBytes: savedContent.length,
              updatedAt: '2026-11-15T09:00:00Z',
            },
          ],
        };
        return jsonResponse(listResp, 200);
      }
      if (
        url ===
          '/api/workspaces/ws-1/bots/bot-1/files/content?path=%2Fhome%2Fbot%2Fnotes.txt' &&
        method === 'GET'
      ) {
        const fileResp: FileContentResponse = {
          path: '/home/bot/notes.txt',
          content: savedContent,
          sizeBytes: savedContent.length,
        };
        return jsonResponse(fileResp, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/files/content' &&
        method === 'PUT'
      ) {
        const parsed = body
          ? (JSON.parse(body) as { path: string; content: string })
          : { path: '', content: '' };
        savedContent = parsed.content;
        const writeResp: FileContentResponse = {
          path: parsed.path,
          content: savedContent,
          sizeBytes: savedContent.length,
        };
        return jsonResponse(writeResp, 200);
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotWorkspacePage />);

    expect(
      await screen.findByRole('button', {
        name: enMessages.workspace.computer.tabs.terminal,
      }),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.tabs.terminal,
      }),
    );

    fireEvent.change(
      screen.getByLabelText(enMessages.workspace.terminal.commandLabel),
      { target: { value: 'echo hello' } },
    );
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.terminal.runButton,
      }),
    );

    expect(await screen.findByText('hello')).toBeInTheDocument();
    expect(screen.getByText('exit 0')).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.tabs.files,
      }),
    );

    const fileBtn = await screen.findByRole('button', { name: /notes\.txt/ });
    fireEvent.click(fileBtn);

    const contentBox = await screen.findByLabelText<HTMLTextAreaElement>(
      enMessages.workspace.files.editorContentLabel,
    );
    await waitFor(() => {
      expect(contentBox.value).toBe('initial notes\n');
    });

    fireEvent.change(contentBox, {
      target: { value: 'updated from workspace editor' },
    });
    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.files.saveFile,
      }),
    );

    expect(
      await screen.findByText('Saved /home/bot/notes.txt.'),
    ).toBeInTheDocument();

    const writeCall = calls.find(
      (c) =>
        c.url === '/api/workspaces/ws-1/bots/bot-1/files/content' &&
        c.method === 'PUT',
    );
    expect(writeCall?.body).toBe(
      JSON.stringify({
        path: '/home/bot/notes.txt',
        content: 'updated from workspace editor',
      }),
    );
  });

  it('enforces Viewer read-only restrictions and second-user control lock + 409 error feedback', async () => {
    const viewerWs = makeWorkspace({ role: 'viewer' });
    const viewerSession = makeSession({
      workspaces: [viewerWs],
      activeWorkspace: viewerWs,
    });
    const bot = makeBot();
    const env = makeEnv({ status: 'running' });

    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(viewerSession, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        return jsonResponse(
          {
            ticket: 't-viewer',
            wsPath: '/ws/bots/ws-1/bot-1/vnc',
            canControl: false,
            expiresAt: '2026-11-15T10:00:00Z',
          } satisfies StreamTicketResponse,
          200,
        );
      }
      if (
        url.startsWith('/api/workspaces/ws-1/bots/bot-1/files?') &&
        method === 'GET'
      ) {
        return jsonResponse(
          { path: '/home/bot', entries: [] } satisfies FileListResponse,
          200,
        );
      }
      return jsonResponse({}, 404);
    });

    const firstRender = renderWithProviders(<BotWorkspacePage />);

    const stopBtn = await screen.findByRole('button', {
      name: enMessages.workspace.stopSandbox,
    });
    expect(stopBtn).toBeDisabled();

    const viewOnlyBtn = screen.getByRole('button', {
      name: enMessages.workspace.computer.viewerReadonlyControl,
    });
    expect(viewOnlyBtn).toBeDisabled();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.tabs.terminal,
      }),
    );
    expect(
      screen.getByText(enMessages.workspace.terminal.viewerNotice),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', {
        name: enMessages.workspace.terminal.runButton,
      }),
    ).toBeDisabled();

    fireEvent.click(
      screen.getByRole('button', {
        name: enMessages.workspace.computer.tabs.files,
      }),
    );
    expect(
      await screen.findByText(enMessages.workspace.files.viewerReadonly),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', {
        name: enMessages.workspace.files.saveFile,
      }),
    ).toBeDisabled();

    firstRender.unmount();

    // Second user sees lock held by Kai, and race 409 surfaces control.locked warning
    const memberWs = makeWorkspace({ role: 'member' });
    const memberSession = makeSession({
      user: {
        id: 'user-3',
        email: 'alex@dunkel.tech',
        displayName: 'Alex Novak',
        locale: 'en',
        createdAt: '2026-11-14T12:00:00Z',
      },
      workspaces: [memberWs],
      activeWorkspace: memberWs,
    });
    let envCalls = 0;

    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(memberSession, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        envCalls += 1;
        return jsonResponse(
          makeEnv({
            status: 'running',
            controlHolder:
              envCalls > 0
                ? {
                    userId: 'user-2',
                    displayName: 'Kai Weber',
                    email: 'kai@dunkel.tech',
                  }
                : null,
          }),
          200,
        );
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        return jsonResponse(
          {
            ticket: 't-member',
            wsPath: '/ws/bots/ws-1/bot-1/vnc',
            canControl: false,
            expiresAt: '2026-11-15T10:00:00Z',
          } satisfies StreamTicketResponse,
          200,
        );
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotWorkspacePage />);

    const lockedBtn = await screen.findByRole('button', {
      name: 'Locked by Kai Weber',
    });
    expect(lockedBtn).toBeDisabled();
    expect(
      screen.getByText(
        'Kai Weber (kai@dunkel.tech) currently holds desktop control. View-only stream until handed back.',
      ),
    ).toBeInTheDocument();
  });

  it('surfaces control.locked warning if claiming control races with another user', async () => {
    const session = makeSession();
    const bot = makeBot();
    const env = makeEnv({ status: 'running', controlHolder: null });

    installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots/bot-1' && method === 'GET') {
        return jsonResponse(bot, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/environment' &&
        method === 'GET'
      ) {
        return jsonResponse(env, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/vnc-ticket' &&
        method === 'POST'
      ) {
        return jsonResponse(
          {
            ticket: 't-1',
            wsPath: '/ws/bots/ws-1/bot-1/vnc',
            canControl: false,
            expiresAt: '2026-11-15T10:00:00Z',
          } satisfies StreamTicketResponse,
          200,
        );
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-1/control/take' &&
        method === 'POST'
      ) {
        return jsonResponse(
          {
            error: {
              code: 'control.locked',
              message: 'another user holds control',
            },
          },
          409,
        );
      }
      return jsonResponse({}, 404);
    });

    renderWithProviders(<BotWorkspacePage />);

    const takeBtn = await screen.findByRole('button', {
      name: enMessages.workspace.computer.takeControl,
    });
    fireEvent.click(takeBtn);

    expect(
      await screen.findByText(enMessages.errors.controlLocked),
    ).toBeInTheDocument();
  });

  it('supports Fleet quick start/stop toggle and /bots/[botId]/settings navigation', async () => {
    mockPathname = '/';
    mockParams = {};
    const session = makeSession();
    const nova = makeBot({
      id: 'bot-2',
      name: 'Nova',
      status: 'stopped',
      mood: 'offline',
    });

    const calls = installFetchMock((url, method) => {
      if (url === '/api/setup/status') {
        return jsonResponse(
          { error: { code: 'setup.already_completed', message: 'done' } },
          404,
        );
      }
      if (url === '/api/auth/refresh') {
        return jsonResponse(session, 200);
      }
      if (url === '/api/workspaces/ws-1/bots' && method === 'GET') {
        return jsonResponse({ bots: [nova] }, 200);
      }
      if (
        url === '/api/workspaces/ws-1/bots/bot-2/environment/start' &&
        method === 'POST'
      ) {
        return jsonResponse(
          makeEnv({ botId: 'bot-2', status: 'running' }),
          200,
        );
      }
      if (url === '/api/workspaces/ws-1/bots/bot-2' && method === 'GET') {
        return jsonResponse(nova, 200);
      }
      return jsonResponse({}, 404);
    });

    const fleetRender = renderWithProviders(<FleetPage />);

    expect(await screen.findByText('Nova')).toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: enMessages.fleet.editAction }),
    ).toHaveAttribute('href', '/bots/bot-2/settings');
    expect(
      screen.getByRole('link', { name: enMessages.fleet.openWorkspace }),
    ).toHaveAttribute('href', '/bots/bot-2');

    fireEvent.click(
      screen.getByRole('button', { name: enMessages.fleet.startSandbox }),
    );

    expect(
      await screen.findByRole('button', { name: enMessages.fleet.stopSandbox }),
    ).toBeInTheDocument();
    expect(
      calls.some(
        (c) =>
          c.url === '/api/workspaces/ws-1/bots/bot-2/environment/start' &&
          c.method === 'POST',
      ),
    ).toBe(true);

    fleetRender.unmount();

    mockPathname = '/bots/bot-2/settings';
    mockParams = { botId: 'bot-2' };
    renderWithProviders(<BotSettingsPage />);

    expect(
      await screen.findByRole('heading', { name: 'Configure Nova' }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: enMessages.builder.backToWorkspace }),
    ).toHaveAttribute('href', '/bots/bot-2');
  });
});
