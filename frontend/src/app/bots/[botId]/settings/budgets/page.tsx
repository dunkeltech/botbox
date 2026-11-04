'use client';

import { BotSettingsRoute } from '@/components/bots/BotSettingsShell';
import { BudgetPanel } from '@/components/chat/BudgetPanel';

/**
 * Per-bot Budgets & usage settings section showing daily/monthly caps,
 * 429 BudgetExceeded enforcement, and subscription token usage.
 */
export default function BotBudgetsSettingsPage() {
  return (
    <BotSettingsRoute activeSection="budgets">
      {({ bot, workspace }) => (
        <BudgetPanel
          workspaceId={workspace.id}
          botId={bot.id}
          canManage={workspace.role === 'owner' || workspace.role === 'admin'}
        />
      )}
    </BotSettingsRoute>
  );
}
