// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useTranslations } from 'next-intl';

import { BotBuilderForm } from '@/components/bots/BotBuilderForm';
import { BotSettingsRoute } from '@/components/bots/BotSettingsShell';
import { RunnerControl } from '@/components/chat/RunnerControl';
import { Card } from '@/components/ui';

/**
 * General & runtime bot settings section: pause/resume control, identity,
 * global role, model brain, sandbox driver, and danger zone.
 */
export default function BotSettingsPage() {
  const tRunner = useTranslations('runner');

  return (
    <BotSettingsRoute activeSection="general">
      {({ bot, workspace, userId, canManage }) => (
        <>
          <Card className="flex flex-wrap items-start justify-between gap-4 p-6">
            <div>
              <h2 className="text-[18px] font-black text-ink">
                {tRunner('settingsTitle')}
              </h2>
              <p className="mt-1 text-[13px] text-ink-2">
                {tRunner('settingsHint')}
              </p>
            </div>
            <RunnerControl
              workspaceId={workspace.id}
              botId={bot.id}
              userId={userId}
              canManage={canManage}
            />
          </Card>

          <BotBuilderForm
            mode="edit"
            workspace={workspace}
            bot={bot}
            embedded
          />
        </>
      )}
    </BotSettingsRoute>
  );
}
