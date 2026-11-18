// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { GitLabInstancesPanel } from '@/components/settings/GitLabInstancesPanel';
import { MCPServersPanel } from '@/components/settings/MCPServersPanel';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { useAuth } from '@/context/AuthContext';

function IntegrationsContent() {
  const { activeWorkspace } = useAuth();
  if (!activeWorkspace) {
    return null;
  }

  return (
    <div className="flex flex-col gap-6">
      <MCPServersPanel
        key={`mcp-${activeWorkspace.id}`}
        workspaceId={activeWorkspace.id}
        role={activeWorkspace.role}
        ignoreMissing
      />
      <GitLabInstancesPanel
        key={activeWorkspace.id}
        workspaceId={activeWorkspace.id}
        role={activeWorkspace.role}
      />
    </div>
  );
}

/**
 * Workspace MCP server registry and self-hosted GitLab origins settings page.
 */
export default function IntegrationsSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <IntegrationsContent />
    </WorkspaceSettingsShell>
  );
}
