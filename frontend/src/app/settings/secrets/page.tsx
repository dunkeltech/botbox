// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { WorkspaceSecretsPanel } from '@/components/settings/WorkspaceSecretsPanel';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { useAuth } from '@/context/AuthContext';

function WorkspaceSecretsContent() {
  const { activeWorkspace } = useAuth();
  if (!activeWorkspace) {
    return null;
  }

  return (
    <WorkspaceSecretsPanel
      key={activeWorkspace.id}
      workspaceId={activeWorkspace.id}
      role={activeWorkspace.role}
    />
  );
}

/**
 * Workspace-wide write-only secrets management page.
 */
export default function WorkspaceSecretsSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <WorkspaceSecretsContent />
    </WorkspaceSettingsShell>
  );
}
