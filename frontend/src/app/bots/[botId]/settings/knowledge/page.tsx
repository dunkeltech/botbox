// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { BotKnowledgeBasePanel } from '@/components/bots/BotKnowledgeBasePanel';
import { BotSettingsRoute } from '@/components/bots/BotSettingsShell';

/**
 * Per-bot Knowledge Base settings page governing persistent facts, context budget,
 * and chat auto-save policy.
 */
export default function BotKnowledgeSettingsPage() {
  return (
    <BotSettingsRoute activeSection="knowledge">
      {({ bot, workspace, canManage }) => (
        <BotKnowledgeBasePanel
          key={`${workspace.id}:${bot.id}`}
          workspaceId={workspace.id}
          botId={bot.id}
          botName={bot.name}
          canManage={canManage}
        />
      )}
    </BotSettingsRoute>
  );
}
