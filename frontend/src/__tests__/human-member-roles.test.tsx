// SPDX-License-Identifier: AGPL-3.0-only

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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import MembersSettingsPage from '@/app/settings/members/page';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { GroupDetails } from '@/components/team/GroupDetails';
import { GroupMessageComposer } from '@/components/team/GroupMessageComposer';
import { AuthProvider } from '@/context/AuthContext';
import { setAccessToken } from '@/lib/api-client';
import type {
  AuthSessionResponse,
  WorkspaceMember,
  WorkspaceRole,
  WorkspaceSummary,
} from '@/types/api';
import type { components } from '@/types/openapi.gen';
import enMessages from '../../messages/en.json';

type Group = components['schemas']['Group'];

const mockPush = vi.fn();
const mockReplace = vi.fn();
const mockPathname = '/settings/members';

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    push: mockPush,
    replace: mockReplace,
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
  usePathname: () => mockPathname,
  useParams: () => ({}),
}));

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

function makeWorkspace(role: WorkspaceRole): WorkspaceSummary {
  return {
    id: 'ws-1',
    name: 'dunkel.Tech',
    slug: 'dunkel-tech',
    defaultDriver: 'docker',
    defaultLocale: 'en',
    role,
    createdAt: '2026-11-14T12:00:00Z',
  };
}

function makeSession(userId: string, role: WorkspaceRole): AuthSessionResponse {
  const ws = makeWorkspace(role);
  return {
    user: {
      id: userId,
      email: `${userId}@dunkel.tech`,
      displayName: userId,
      locale: 'en',
      createdAt: '2026-11-14T12:00:00Z',
    },
    workspaces: [ws],
    activeWorkspace: ws,
    accessToken: 'jwt-role-test',
  };
}

const baseMembers: WorkspaceMember[] = [
  {
    workspaceId: 'ws-1',
    userId: 'owner-1',
    email: 'owner@dunkel.tech',
    displayName: 'Jan Weber',
    locale: 'en',
    role: 'owner',
    roleDescription: 'Security Lead',
    joinedAt: '2026-11-14T12:00:00Z',
  },
  {
    workspaceId: 'ws-1',
    userId: 'admin-1',
    email: 'admin1@dunkel.tech',
    displayName: 'Lukas Meyer',
    locale: 'en',
    role: 'admin',
    roleDescription: '',
    joinedAt: '2026-11-14T12:05:00Z',
  },
  {
    workspaceId: 'ws-1',
    userId: 'admin-2',
    email: 'admin2@dunkel.tech',
    displayName: 'Nora Becker',
    locale: 'en',
    role: 'admin',
    roleDescription: 'Infra Admin',
    joinedAt: '2026-11-14T12:10:00Z',
  },
  {
    workspaceId: 'ws-1',
    userId: 'member-1',
    email: 'maya@dunkel.tech',
    displayName: 'Maya Lin',
    locale: 'en',
    role: 'member',
    roleDescription: 'Release Reviewer',
    joinedAt: '2026-11-14T12:15:00Z',
  },
  {
    workspaceId: 'ws-1',
    userId: 'viewer-1',
    email: 'sara@dunkel.tech',
    displayName: 'Sara Klein',
    locale: 'en',
    role: 'viewer',
    roleDescription: '',
    joinedAt: '2026-11-14T12:20:00Z',
  },
];

function installMembersFetch(
  actorId: string,
  actorRole: WorkspaceRole,
  options?: { failPatch?: boolean },
) {
  let members = baseMembers.map((m) => ({ ...m }));
  const patchBodies: Array<{ url: string; body: unknown }> = [];

  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url =
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url;
      const method = (init?.method ?? 'GET').toUpperCase();
      const bodyText = typeof init?.body === 'string' ? init.body : null;

      if (url === '/api/setup/status') {
        return Promise.resolve(
          jsonResponse(
            { error: { code: 'setup.already_completed', message: 'done' } },
            404,
          ),
        );
      }
      if (url === '/api/auth/refresh') {
        return Promise.resolve(
          jsonResponse(makeSession(actorId, actorRole), 200),
        );
      }
      if (url.endsWith('/approvals')) {
        return Promise.resolve(jsonResponse({ approvals: [] }, 200));
      }
      if (url === '/api/workspaces/ws-1/members' && method === 'GET') {
        return Promise.resolve(jsonResponse({ members }, 200));
      }
      if (url === '/api/workspaces/ws-1/invitations' && method === 'GET') {
        return Promise.resolve(jsonResponse({ invitations: [] }, 200));
      }
      if (
        url.startsWith('/api/workspaces/ws-1/members/') &&
        method === 'PATCH'
      ) {
        const parsed = JSON.parse(bodyText ?? '{}') as {
          roleDescription?: string;
        };
        patchBodies.push({ url, body: parsed });
        if (options?.failPatch) {
          return Promise.resolve(
            jsonResponse(
              {
                error: {
                  code: 'auth.forbidden',
                  message: 'cannot modify role description',
                },
              },
              403,
            ),
          );
        }
        const targetId = url.split('/').pop();
        members = members.map((m) =>
          m.userId === targetId
            ? { ...m, roleDescription: parsed.roleDescription ?? '' }
            : m,
        );
        const updated = members.find((m) => m.userId === targetId);
        return Promise.resolve(jsonResponse(updated, 200));
      }
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  return { patchBodies };
}

beforeEach(() => {
  setAccessToken(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe('human member roles settings', () => {
  it.each<{
    actorId: string;
    actorRole: WorkspaceRole;
    editableNames: string[];
    blockedNames: string[];
  }>([
    {
      actorId: 'owner-1',
      actorRole: 'owner',
      editableNames: [
        'Jan Weber',
        'Lukas Meyer',
        'Nora Becker',
        'Maya Lin',
        'Sara Klein',
      ],
      blockedNames: [],
    },
    {
      actorId: 'admin-1',
      actorRole: 'admin',
      editableNames: ['Lukas Meyer', 'Maya Lin', 'Sara Klein'],
      blockedNames: ['Jan Weber', 'Nora Becker'],
    },
    {
      actorId: 'member-1',
      actorRole: 'member',
      editableNames: ['Maya Lin'],
      blockedNames: ['Jan Weber', 'Lukas Meyer', 'Nora Becker', 'Sara Klein'],
    },
    {
      actorId: 'viewer-1',
      actorRole: 'viewer',
      editableNames: [],
      blockedNames: [
        'Jan Weber',
        'Lukas Meyer',
        'Nora Becker',
        'Maya Lin',
        'Sara Klein',
      ],
    },
  ])(
    'enforces role description button visibility for $actorRole',
    async ({ actorId, actorRole, editableNames, blockedNames }) => {
      installMembersFetch(actorId, actorRole);
      renderWithProviders(<MembersSettingsPage />);

      const table = await screen.findByRole('table');
      for (const name of editableNames) {
        const row = within(table).getByText(name).closest('tr');
        expect(row).not.toBeNull();
        expect(
          within(row as HTMLElement).getByRole('button', {
            name: new RegExp(
              `(Edit|Set) role and responsibilities for ${name}`,
            ),
          }),
        ).toBeInTheDocument();
      }
      for (const name of blockedNames) {
        const row = within(table).getByText(name).closest('tr');
        expect(row).not.toBeNull();
        expect(
          within(row as HTMLElement).queryByRole('button', {
            name: new RegExp(
              `(Edit|Set) role and responsibilities for ${name}`,
            ),
          }),
        ).not.toBeInTheDocument();
      }
    },
  );

  it('sets, edits, and removes a human member role description and surfaces errors', async () => {
    const { patchBodies } = installMembersFetch('member-1', 'member');
    renderWithProviders(<MembersSettingsPage />);

    const table = await screen.findByRole('table');
    expect(within(table).getByText('Release Reviewer')).toBeInTheDocument();

    // Open modal for Maya Lin (self) and remove role.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'Edit role and responsibilities for Maya Lin',
      }),
    );

    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByText('Role & responsibilities for Maya Lin'),
    ).toBeInTheDocument();

    fireEvent.click(
      within(dialog).getByRole('button', {
        name: enMessages.members.roleModalRemove,
      }),
    );

    await waitFor(() => {
      expect(patchBodies).toHaveLength(1);
    });
    expect(patchBodies[0]).toEqual({
      url: '/api/workspaces/ws-1/members/member-1',
      body: { roleDescription: '' },
    });

    // Now set a new role on Maya Lin.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'Set role and responsibilities for Maya Lin',
      }),
    );

    const reopened = screen.getByRole('dialog');
    expect(
      within(reopened).queryByRole('button', {
        name: enMessages.members.roleModalRemove,
      }),
    ).not.toBeInTheDocument();

    const textarea = within(reopened).getByLabelText(
      enMessages.members.roleModalLabel,
    );
    fireEvent.change(textarea, {
      target: { value: '  Release Lead — Signs off on tags.  ' },
    });
    fireEvent.click(
      within(reopened).getByRole('button', {
        name: enMessages.members.roleModalSave,
      }),
    );

    await waitFor(() => {
      expect(patchBodies).toHaveLength(2);
    });
    expect(patchBodies[1]).toEqual({
      url: '/api/workspaces/ws-1/members/member-1',
      body: { roleDescription: 'Release Lead — Signs off on tags.' },
    });
    expect(
      within(table).getByText('Release Lead — Signs off on tags.'),
    ).toBeInTheDocument();

    // Verify error handling keeps dialog open with amber alert.
    cleanup();
    installMembersFetch('member-1', 'member', { failPatch: true });
    renderWithProviders(<MembersSettingsPage />);

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Edit role and responsibilities for Maya Lin',
      }),
    );
    const errorDialog = screen.getByRole('dialog');
    fireEvent.click(
      within(errorDialog).getByRole('button', {
        name: enMessages.members.roleModalSave,
      }),
    );
    const alert = await within(errorDialog).findByRole('alert');
    expect(alert).toHaveTextContent(enMessages.errors.authForbidden);
  });
});

describe('human roles in group details and @mention composer', () => {
  it('renders human member role descriptions in GroupDetails and omits empty ones', () => {
    const group: Group = {
      id: 'grp-1',
      name: 'Release',
      goal: 'Ship v1.4.0',
      leadBotId: 'bot-1',
      botTalk: true,
      turnLimit: 20,
      answerPolicy: 'mentioned_else_lead',
      askBefore: [],
      paused: false,
      pauseReason: '',
      turnsSinceHuman: 0,
      unread: 0,
      members: [
        {
          type: 'bot',
          id: 'bot-1',
          roleText: '',
          name: 'Atlas',
          roleDescription: 'Release Engineer',
        },
        {
          type: 'user',
          id: 'user-1',
          roleText: '',
          name: 'Maya Lin',
          roleDescription: 'Release Reviewer — Checks release notes.',
        },
        {
          type: 'user',
          id: 'user-2',
          roleText: '',
          name: 'Liam',
          roleDescription: '',
        },
      ],
    };

    render(
      <NextIntlClientProvider locale="en" messages={enMessages}>
        <GroupDetails
          group={group}
          bots={[]}
          canEdit={false}
          error={null}
          onPatch={vi.fn()}
        />
      </NextIntlClientProvider>,
    );

    expect(
      screen.getByText('Release Reviewer — Checks release notes.'),
    ).toBeInTheDocument();
    expect(
      screen.getAllByRole('region', {
        name: enMessages.team.humanRoleLabel,
      }),
    ).toHaveLength(1);
  });

  it('shows human member role descriptions in the @mention autocomplete list', () => {
    render(
      <NextIntlClientProvider locale="en" messages={enMessages}>
        <GroupMessageComposer
          canPost
          targets={[
            {
              id: 'user-1',
              name: 'Maya Lin',
              kind: 'user',
              roleDescription: 'Release Reviewer',
            },
            {
              id: 'bot-1',
              name: 'Atlas',
              kind: 'bot',
              roleDescription: 'Release Engineer',
            },
            { id: 'all', name: 'all', kind: 'all' },
          ]}
          postMessage={vi.fn()}
          onSent={vi.fn()}
        />
      </NextIntlClientProvider>,
    );

    const composer = screen.getByLabelText(enMessages.team.composer);
    fireEvent.change(composer, {
      target: { value: '@', selectionStart: 1 },
    });

    const listbox = screen.getByRole('listbox', {
      name: enMessages.team.mentionList,
    });
    expect(within(listbox).getByText('@Maya Lin')).toBeInTheDocument();
    expect(within(listbox).getByText('Release Reviewer')).toBeInTheDocument();
  });
});
