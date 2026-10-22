'use client';

import { AppShell } from '@/components/shell/AppShell';
import { TeamScreen } from '@/components/team/TeamScreen';
import { useAuth } from '@/context/AuthContext';

function TeamContent() {
  const { activeWorkspace, user } = useAuth();
  if (!activeWorkspace || !user) return null;
  return (
    <TeamScreen
      workspaceId={activeWorkspace.id}
      userId={user.id}
      displayName={user.displayName}
      role={activeWorkspace.role}
      workspaceName={activeWorkspace.name}
    />
  );
}

/** Team chat for the active workspace. */
export default function TeamPage() {
  return (
    <AppShell>
      <TeamContent />
    </AppShell>
  );
}
