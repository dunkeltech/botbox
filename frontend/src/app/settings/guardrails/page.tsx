// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { LLMGuardrailsPanel } from '@/components/settings/LLMGuardrailsPanel';
import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { useAuth } from '@/context/AuthContext';

function GuardrailsSettingsContent() {
  const { activeWorkspace } = useAuth();
  if (!activeWorkspace) {
    return null;
  }

  return (
    <LLMGuardrailsPanel
      key={activeWorkspace.id}
      workspaceId={activeWorkspace.id}
      role={activeWorkspace.role}
    />
  );
}

/**
 * Workspace settings route for configuring LLM proxy guardrails, built-in
 * detectors, custom RE2/keyword rules, and viewing the proxy decision stream.
 */
export default function GuardrailsSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <GuardrailsSettingsContent />
    </WorkspaceSettingsShell>
  );
}
