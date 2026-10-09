'use client';

import { AppShell } from '@/components/shell/AppShell';
import { BotWorkspaceView } from '@/components/workspace/BotWorkspaceView';

/**
 * Single bot workspace route hosting desktop noVNC viewport, terminal runner,
 * file browser, and sandbox controls.
 */
export default function BotWorkspacePage() {
  return (
    <AppShell>
      <BotWorkspaceView />
    </AppShell>
  );
}
