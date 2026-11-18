// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import Link from 'next/link';
import { useTranslations } from 'next-intl';

import { BotBuilderForm } from '@/components/bots/BotBuilderForm';
import { AppShell } from '@/components/shell/AppShell';
import { Alert } from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { canCreateBot } from '@/lib/rbac';

function NewBotContent() {
  const tBuilder = useTranslations('builder');
  const tErrors = useTranslations('errors');
  const { activeWorkspace } = useAuth();

  if (!activeWorkspace) {
    return null;
  }

  if (!canCreateBot(activeWorkspace.role)) {
    return (
      <div className="flex flex-col gap-4">
        <Alert tone="warning">{tErrors('authForbidden')}</Alert>
        <Link
          href="/"
          className="inline-flex min-h-[44px] w-fit items-center rounded-sm border-[1.5px] border-line bg-paper px-4 text-[14px] font-bold text-green-600 hover:bg-mint-soft"
        >
          {tBuilder('backToFleet')}
        </Link>
      </div>
    );
  }

  return <BotBuilderForm mode="create" workspace={activeWorkspace} />;
}

/**
 * Create-bot route scoped to the active workspace.
 */
export default function NewBotPage() {
  return (
    <AppShell>
      <NewBotContent />
    </AppShell>
  );
}
