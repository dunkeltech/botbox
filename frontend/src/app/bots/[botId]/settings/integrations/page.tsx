// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { BotMCPProxyPanel } from '@/components/bots/BotMCPProxyPanel';
import { BotSettingsRoute } from '@/components/bots/BotSettingsShell';
import { BotGitLabInstancesPanel } from '@/components/settings/BotGitLabInstancesPanel';

/**
 * Per-bot MCP & GitLab integrations section governing assigned GitLab
 * login origins and fail-closed MCP tool command policies.
 */
export default function BotIntegrationsSettingsPage() {
  return (
    <BotSettingsRoute activeSection="integrations">
      {({ bot, workspace, canManage }) => (
        <>
          <BotGitLabInstancesPanel
            key={`${workspace.id}:${bot.id}`}
            workspaceId={workspace.id}
            botId={bot.id}
            canManage={canManage}
          />
          <BotMCPProxyPanel
            key={`mcp-${workspace.id}:${bot.id}`}
            workspaceId={workspace.id}
            botId={bot.id}
            botName={bot.name}
            canManage={canManage}
            ignoreMissing
          />
        </>
      )}
    </BotSettingsRoute>
  );
}
