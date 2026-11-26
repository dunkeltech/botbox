// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const mockedAuth = vi.hoisted(() => ({
  activeWorkspace: {
    id: 'ws-1',
    name: 'dunkel.Tech',
    slug: 'dunkel-tech',
    role: 'owner' as const,
    defaultDriver: 'docker' as const,
    defaultLocale: 'en',
    screenshotRetentionDays: 7,
  },
}));

vi.mock('next/navigation', () => ({
  usePathname: () => '/settings/audit',
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    prefetch: vi.fn(),
    refresh: vi.fn(),
  }),
}));

vi.mock('@/context/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'user-1', email: 'owner@example.com', displayName: 'Owner' },
    workspaces: [mockedAuth.activeWorkspace],
    activeWorkspace: mockedAuth.activeWorkspace,
    setupRequired: false,
    isLoading: false,
    applySession: vi.fn(),
    selectWorkspace: vi.fn(),
    reloadWorkspaces: vi.fn(),
    signOut: vi.fn(),
    refreshSetupStatus: vi.fn(),
  }),
}));

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return {
    ...actual,
    requestJson: vi.fn(),
  };
});

import { AuditLogView } from '@/components/settings/AuditLogView';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { ApiClientError, requestJson } from '@/lib/api-client';
import messages from '../../../../messages/en.json';

function renderWithIntl(ui: ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      {ui}
    </NextIntlClientProvider>,
  );
}

describe('AuditLogView', () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('loads entries for workspace owner, supports filtering and pagination', async () => {
    const mockReq = vi.mocked(requestJson);
    mockReq
      .mockResolvedValueOnce({
        entries: [
          {
            id: 'entry-1',
            workspaceId: 'ws-1',
            actorUserId: 'user-1',
            actorEmail: 'owner@example.com',
            actorDisplayName: 'Owner',
            action: 'secret.created',
            targetType: 'workspace_secret',
            targetId: '11111111-1111-1111-1111-111111111111',
            metadata: { token: '[REDACTED]', name: 'GH_TOKEN' },
            createdAt: '2026-11-26T10:00:00Z',
          },
        ],
        total: 30,
        limit: 25,
        offset: 0,
      })
      .mockResolvedValueOnce({
        entries: [
          {
            id: 'entry-26',
            workspaceId: 'ws-1',
            action: 'environment.auto_stopped_idle',
            targetType: 'environment',
            metadata: {},
            createdAt: '2026-11-26T09:00:00Z',
          },
        ],
        total: 30,
        limit: 25,
        offset: 25,
      })
      .mockResolvedValueOnce({
        entries: [],
        total: 0,
        limit: 25,
        offset: 0,
      });

    renderWithIntl(<AuditLogView workspaceId="ws-1" role="owner" />);

    await waitFor(() => {
      expect(screen.getByText('secret.created')).toBeInTheDocument();
    });
    expect(screen.getByText('Owner (owner@example.com)')).toBeInTheDocument();
    expect(screen.getByText(/GH_TOKEN/)).toBeInTheDocument();
    expect(mockReq).toHaveBeenCalledWith(
      '/api/workspaces/ws-1/audit-logs?limit=25&offset=0',
    );

    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => {
      expect(mockReq).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/audit-logs?limit=25&offset=25',
      );
    });
    expect(
      screen.getByText('environment.auto_stopped_idle'),
    ).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Filter by action'), {
      target: { value: 'secret.created' },
    });
    fireEvent.change(screen.getByLabelText('Target type'), {
      target: { value: 'workspace_secret' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Apply filters' }));

    await waitFor(() => {
      expect(mockReq).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/audit-logs?limit=25&offset=0&action=secret.created&targetType=workspace_secret',
      );
    });
  });

  it('blocks member and viewer roles with translated admin-only gate without API fetch', () => {
    const mockReq = vi.mocked(requestJson);
    for (const role of ['member', 'viewer'] as const) {
      renderWithIntl(<AuditLogView workspaceId="ws-1" role={role} />);
      expect(
        screen.getByText('Administrator access required'),
      ).toBeInTheDocument();
      cleanup();
    }
    expect(mockReq).not.toHaveBeenCalled();
  });

  it('shows error banner when request fails', async () => {
    const mockReq = vi.mocked(requestJson);
    mockReq.mockRejectedValueOnce(
      new ApiClientError(500, 'internal_error', 'failed'),
    );

    renderWithIntl(<AuditLogView workspaceId="ws-1" role="admin" />);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(
        'Could not load workspace audit log.',
      );
    });
  });

  it('includes audit log navigation link in WorkspaceSettingsShell', async () => {
    vi.mocked(requestJson).mockResolvedValue({ approvals: [] });
    renderWithIntl(
      <WorkspaceSettingsShell>
        <div>body</div>
      </WorkspaceSettingsShell>,
    );
    const link = await screen.findByRole('link', { name: 'Audit log' });
    expect(link).toHaveAttribute('href', '/settings/audit');
  });
});
