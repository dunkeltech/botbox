// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { AuditLogView } from '@/components/settings/AuditLogView';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { useAuth } from '@/context/AuthContext';

function AuditLogContent() {
  const { activeWorkspace } = useAuth();
  if (!activeWorkspace) {
    return null;
  }

  return (
    <AuditLogView
      key={activeWorkspace.id}
      workspaceId={activeWorkspace.id}
      role={activeWorkspace.role}
    />
  );
}

/**
 * Workspace audit log settings page for owners and admins.
 */
export default function WorkspaceAuditSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <AuditLogContent />
    </WorkspaceSettingsShell>
  );
}
