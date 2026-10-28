'use client';

import Link from 'next/link';
import { useParams } from 'next/navigation';
import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import { BotGitLabInstancesPanel } from '@/components/settings/BotGitLabInstancesPanel';
import { canManageBot } from '@/lib/rbac';
import { BudgetPanel } from '@/components/chat/BudgetPanel';
import { BotBuilderForm } from '@/components/bots/BotBuilderForm';
import { AppShell } from '@/components/shell/AppShell';
import { Alert, Card } from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { getBot } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { Bot } from '@/types/api';

function BotSettingsContent() {
  const tActions = useTranslations('actions');
  const tBuilder = useTranslations('builder');
  const tErrors = useTranslations('errors');
  const params = useParams<{ botId: string }>();
  const botId = typeof params?.botId === 'string' ? params.botId : '';
  const { activeWorkspace, user } = useAuth();

  const [bot, setBot] = useState<Bot | null>(null);
  const [loading, setLoading] = useState(true);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  useEffect(() => {
    if (!activeWorkspace || !botId) {
      setBot(null);
      setLoading(false);
      return;
    }

    let cancelled = false;
    setLoading(true);
    setErrorKey(null);

    void (async () => {
      try {
        const fetched = await getBot(activeWorkspace.id, botId);
        if (!cancelled) {
          setBot(fetched);
        }
      } catch (err) {
        if (!cancelled) {
          setBot(null);
          setErrorKey(resolveErrorKey(err));
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [activeWorkspace, botId]);

  if (!activeWorkspace) {
    return null;
  }

  if (loading) {
    return (
      <Card className="p-8 text-center">
        <p role="status" className="text-[15px] font-bold text-ink-2">
          {tActions('loading')}
        </p>
      </Card>
    );
  }

  if (errorKey || !bot) {
    return (
      <div className="flex flex-col gap-4">
        <Alert tone="warning">{tErrors(errorKey ?? 'botNotFound')}</Alert>
        <Link
          href="/"
          className="inline-flex min-h-[44px] w-fit items-center rounded-sm border-[1.5px] border-line bg-paper px-4 text-[14px] font-bold text-green-600 hover:bg-mint-soft"
        >
          {tBuilder('backToFleet')}
        </Link>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <BotBuilderForm mode="edit" workspace={activeWorkspace} bot={bot} />
      <BotGitLabInstancesPanel
        key={`${activeWorkspace.id}:${bot.id}`}
        workspaceId={activeWorkspace.id}
        botId={bot.id}
        canManage={canManageBot(activeWorkspace.role, user?.id, bot)}
      />
      <BudgetPanel
        workspaceId={activeWorkspace.id}
        botId={bot.id}
        canManage={
          activeWorkspace.role === 'owner' || activeWorkspace.role === 'admin'
        }
      />
    </div>
  );
}

/**
 * Bot configuration / settings page scoped to the active workspace.
 */
export default function BotSettingsPage() {
  return (
    <AppShell>
      <BotSettingsContent />
    </AppShell>
  );
}
