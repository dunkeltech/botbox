'use client';

import { BotSecretsView } from '@/components/bots/BotSecretsView';
import { BotSettingsRoute } from '@/components/bots/BotSettingsShell';

/**
 * Per-bot Secrets & Egress Network section wrapped in the shared Bot Settings shell.
 */
export default function BotSecretsPage() {
  return (
    <BotSettingsRoute activeSection="secrets">
      {({ bot, workspace }) => (
        <BotSecretsView workspace={workspace} bot={bot} embedded />
      )}
    </BotSettingsRoute>
  );
}
