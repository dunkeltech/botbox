// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { BudgetPanel } from '@/components/chat/BudgetPanel';
import { ModelPricesPanel } from '@/components/settings/ModelPricesPanel';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { useAuth } from '@/context/AuthContext';
import { canManageMembers } from '@/lib/rbac';

function BudgetsContent() {
  const { activeWorkspace } = useAuth();
  if (!activeWorkspace) {
    return null;
  }
  const canManage = canManageMembers(activeWorkspace.role);

  return (
    <div className="flex flex-col gap-6">
      <BudgetPanel
        workspaceId={activeWorkspace.id}
        canManage={canManage}
        ignoreMissing
      />
      <ModelPricesPanel
        key={activeWorkspace.id}
        workspaceId={activeWorkspace.id}
        canManage={canManage}
        ignoreMissing
      />
    </div>
  );
}

/**
 * Workspace spend budgets and model_prices rate table settings page.
 */
export default function BudgetsSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <BudgetsContent />
    </WorkspaceSettingsShell>
  );
}
