'use client';

import { AppShell } from '@/components/shell/AppShell';
import { ApprovalsInbox } from '@/components/team/ApprovalsInbox';
import { useAuth } from '@/context/AuthContext';

function ApprovalsContent() {
  const { activeWorkspace, user } = useAuth();
  if (!activeWorkspace || !user) return null;
  return (
    <ApprovalsInbox
      workspaceId={activeWorkspace.id}
      role={activeWorkspace.role}
    />
  );
}

/** Approvals inbox for the active workspace. */
export default function ApprovalsPage() {
  return (
    <AppShell>
      <ApprovalsContent />
    </AppShell>
  );
}
