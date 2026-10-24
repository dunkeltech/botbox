'use client';

import { useQuery } from '@tanstack/react-query';
import { AppShell, useTeamShell } from '@/components/shell/AppShell';
import { ApprovalsInbox } from '@/components/team/ApprovalsInbox';
import { TeamSidebar } from '@/components/team/TeamSidebar';
import { useApprovalList } from '@/components/team/useApprovals';
import { useAuth } from '@/context/AuthContext';
import { requestJson } from '@/lib/api-client';
import type { BotListResponse, WorkspaceMemberListResponse } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type Group = components['schemas']['Group'];

function ApprovalsContent() {
  const { activeWorkspace, user } = useAuth();
  const shell = useTeamShell();
  const workspaceId = activeWorkspace?.id ?? '';
  const base = `/api/workspaces/${encodeURIComponent(workspaceId)}`;

  const groups = useQuery({
    queryKey: ['groups', workspaceId],
    enabled: Boolean(workspaceId),
    queryFn: () => requestJson<Group[]>(`${base}/groups`),
  });
  const bots = useQuery({
    queryKey: ['bots', workspaceId],
    enabled: Boolean(workspaceId),
    queryFn: () => requestJson<BotListResponse>(`${base}/bots`),
  });
  const members = useQuery({
    queryKey: ['members', workspaceId],
    enabled: Boolean(workspaceId),
    queryFn: () => requestJson<WorkspaceMemberListResponse>(`${base}/members`),
  });
  const approvals = useApprovalList(workspaceId);

  if (!activeWorkspace || !user) return null;

  const groupList = groups.data ?? [];
  const botList = bots.data?.bots ?? [];
  const humanList = members.data?.members ?? [];
  const needle = shell.searchQuery.trim().toLowerCase();
  const visibleGroups = needle
    ? groupList.filter((g) => g.name.toLowerCase().includes(needle))
    : groupList;
  const visibleBots = needle
    ? botList.filter((b) => b.name.toLowerCase().includes(needle))
    : botList;
  const visibleHumans = needle
    ? humanList.filter((h) => h.displayName.toLowerCase().includes(needle))
    : humanList;

  return (
    <div className="flex min-h-0 flex-1 overflow-hidden">
      <TeamSidebar
        workspaceName={activeWorkspace.name}
        role={activeWorkspace.role}
        groups={visibleGroups}
        bots={visibleBots}
        humans={visibleHumans}
        totalBots={botList.length}
        totalHumans={humanList.length}
        pendingApprovalsCount={
          approvals.data
            ? approvals.data.approvals.filter((row) => row.status === 'pending')
                .length
            : undefined
        }
        selection={{ type: 'approvals' }}
        canChange={activeWorkspace.role !== 'viewer'}
        mobileOpen={shell.mobileDrawerOpen}
        onCloseMobile={() => shell.setMobileDrawerOpen(false)}
      />
      <div className="min-h-0 flex-1 overflow-y-auto bg-paper-2 p-5 sm:p-7">
        <ApprovalsInbox
          workspaceId={activeWorkspace.id}
          role={activeWorkspace.role}
        />
      </div>
    </div>
  );
}

/** Approvals inbox for the active workspace inside the full-bleed Team shell. */
export default function ApprovalsPage() {
  return (
    <AppShell variant="team">
      <ApprovalsContent />
    </AppShell>
  );
}
