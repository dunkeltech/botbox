'use client';

import {
  ArrowLeft,
  Coins,
  KeyRound,
  Plug,
  SlidersHorizontal,
} from 'lucide-react';
import Link from 'next/link';
import { useParams } from 'next/navigation';
import { type ReactNode, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import { AppShell } from '@/components/shell/AppShell';
import {
  Alert,
  BotAvatar,
  Card,
  Eyebrow,
  RoleBadge,
  StatusChip,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { getBot } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canManageBot } from '@/lib/rbac';
import type { Bot, WorkspaceSummary } from '@/types/api';

export type BotSettingsSection =
  'general' | 'budgets' | 'integrations' | 'secrets';

export interface BotSettingsShellProps {
  bot: Bot;
  workspace: WorkspaceSummary;
  canManage: boolean;
  activeSection: BotSettingsSection;
  children: ReactNode;
}

export interface BotSettingsRouteContext {
  bot: Bot;
  workspace: WorkspaceSummary;
  userId: string;
  canManage: boolean;
}

/**
 * Shared 2-column Bot Settings shell providing the breadcrumb header, bot status pill,
 * and grouped 256 px left sub-navigation.
 */
export function BotSettingsShell({
  bot,
  workspace,
  canManage,
  activeSection,
  children,
}: BotSettingsShellProps) {
  const t = useTranslations('botSettings');
  const tBuilder = useTranslations('builder');
  const tFleet = useTranslations('fleet');

  const botName = bot.name || tBuilder('namePlaceholder');
  const botStatus = bot.status ?? 'idle';
  const botSeed = bot.avatarSeed || bot.id;

  const navGroups = [
    {
      id: 'bot',
      label: t('groups.bot'),
      items: [
        {
          id: 'general' as const,
          href: `/bots/${bot.id}/settings`,
          label: t('nav.general'),
          icon: SlidersHorizontal,
        },
      ],
    },
    {
      id: 'spend',
      label: t('groups.spend'),
      items: [
        {
          id: 'budgets' as const,
          href: `/bots/${bot.id}/settings/budgets`,
          label: t('nav.budgets'),
          icon: Coins,
        },
      ],
    },
    {
      id: 'security',
      label: t('groups.security'),
      items: [
        {
          id: 'integrations' as const,
          href: `/bots/${bot.id}/settings/integrations`,
          label: t('nav.integrations'),
          icon: Plug,
        },
        {
          id: 'secrets' as const,
          href: `/bots/${bot.id}/secrets`,
          label: t('nav.secrets'),
          icon: KeyRound,
        },
      ],
    },
  ] as const;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-3">
            <Link
              href={`/bots/${bot.id}`}
              className="inline-flex min-h-[44px] w-fit items-center gap-2 rounded-sm px-1 text-[14px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <ArrowLeft className="h-4 w-4" aria-hidden="true" />
              <span>{tBuilder('backToWorkspace')}</span>
            </Link>
            <span aria-hidden="true" className="text-line-strong">
              /
            </span>
            <Eyebrow>{t('eyebrow', { name: botName })}</Eyebrow>
          </div>

          <h1 className="text-[34px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[42px]">
            {tBuilder('editTitle', { name: botName })}
          </h1>
          <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
            {t('lead', { name: botName })}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2.5 rounded-lg border border-line bg-paper px-3.5 py-2 shadow-card">
          <BotAvatar
            seed={botSeed}
            mood={bot.mood ?? 'idle'}
            size={32}
            tile
            decorative
          />
          <span className="text-[14px] font-black text-ink">{botName}</span>
          <StatusChip
            status={botStatus === 'action' ? 'working' : botStatus}
            label={tFleet(`status.${botStatus}`)}
          />
          <RoleBadge role={workspace.role} />
          <span className="rounded-pill border border-line bg-paper-2 px-2.5 py-0.5 text-[12px] font-bold text-ink-2">
            {canManage ? t('canManage') : t('readOnly')}
          </span>
        </div>
      </div>

      <div className="grid gap-6 lg:grid-cols-[256px_minmax(0,1fr)] lg:items-start">
        <aside className="lg:sticky lg:top-6">
          <Card className="p-4">
            <nav
              aria-label={tBuilder('tabs.ariaLabel')}
              className="flex flex-col gap-4"
            >
              {navGroups.map((group) => (
                <div key={group.id} className="flex flex-col gap-1">
                  <p className="px-2.5 pb-1 text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
                    {group.label}
                  </p>
                  <div className="flex flex-row flex-wrap gap-1 lg:flex-col lg:flex-nowrap">
                    {group.items.map((item) => {
                      const Icon = item.icon;
                      const active = item.id === activeSection;
                      return (
                        <Link
                          key={item.id}
                          href={item.href}
                          aria-current={active ? 'page' : undefined}
                          className={`inline-flex min-h-[44px] items-center gap-2.5 rounded-sm px-3 text-[14px] transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                            active
                              ? 'border-l-[3px] border-green bg-mint-soft font-bold text-green-600'
                              : 'font-bold text-ink-2 hover:bg-paper-2 hover:text-ink'
                          }`}
                        >
                          <Icon
                            className="h-4 w-4 shrink-0"
                            aria-hidden="true"
                          />
                          <span>{item.label}</span>
                        </Link>
                      );
                    })}
                  </div>
                </div>
              ))}
            </nav>
          </Card>
        </aside>

        <div className="flex min-w-0 flex-col gap-6">{children}</div>
      </div>
    </div>
  );
}

function BotSettingsRouteInner({
  activeSection,
  children,
}: {
  activeSection: BotSettingsSection;
  children: (ctx: BotSettingsRouteContext) => ReactNode;
}) {
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

  const canManage = canManageBot(activeWorkspace.role, user?.id, bot);

  return (
    <BotSettingsShell
      bot={bot}
      workspace={activeWorkspace}
      canManage={canManage}
      activeSection={activeSection}
    >
      {children({
        bot,
        workspace: activeWorkspace,
        userId: user?.id ?? '',
        canManage,
      })}
    </BotSettingsShell>
  );
}

/**
 * Route-level AppShell + workspace-scoped bot loader shared by all four
 * Bot Settings sub-pages (`/settings`, `/settings/budgets`, `/settings/integrations`, `/secrets`).
 */
export function BotSettingsRoute({
  activeSection,
  children,
}: {
  activeSection: BotSettingsSection;
  children: (ctx: BotSettingsRouteContext) => ReactNode;
}) {
  return (
    <AppShell>
      <BotSettingsRouteInner activeSection={activeSection}>
        {children}
      </BotSettingsRouteInner>
    </AppShell>
  );
}
