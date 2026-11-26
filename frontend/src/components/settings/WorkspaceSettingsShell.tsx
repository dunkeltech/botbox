// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import {
  ClipboardList,
  Coins,
  KeyRound,
  Plug,
  Shield,
  SlidersHorizontal,
  Users,
} from 'lucide-react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import type { ReactNode } from 'react';
import { useTranslations } from 'next-intl';

import { AppShell } from '@/components/shell/AppShell';
import { Card, Eyebrow, RoleBadge } from '@/components/ui';
import { useAuth } from '@/context/AuthContext';

export interface WorkspaceSettingsShellProps {
  children: ReactNode;
  headerAction?: ReactNode;
}

const SETTINGS_NAV_GROUPS = [
  {
    id: 'workspace',
    labelKey: 'groups.workspace',
    items: [
      {
        href: '/settings/general',
        labelKey: 'nav.general',
        icon: SlidersHorizontal,
      },
      {
        href: '/settings/members',
        labelKey: 'nav.members',
        icon: Users,
      },
    ],
  },
  {
    id: 'spend',
    labelKey: 'groups.spend',
    items: [
      {
        href: '/settings/budgets',
        labelKey: 'nav.budgets',
        icon: Coins,
      },
    ],
  },
  {
    id: 'security',
    labelKey: 'groups.security',
    items: [
      {
        href: '/settings/secrets',
        labelKey: 'nav.secrets',
        icon: KeyRound,
      },
      {
        href: '/settings/integrations',
        labelKey: 'nav.integrations',
        icon: Plug,
      },
      {
        href: '/settings/guardrails',
        labelKey: 'nav.guardrails',
        icon: Shield,
      },
      {
        href: '/settings/audit',
        labelKey: 'nav.audit',
        icon: ClipboardList,
      },
    ],
  },
] as const;

function SettingsShellInner({
  children,
  headerAction,
}: WorkspaceSettingsShellProps) {
  const t = useTranslations('workspaceSettings');
  const pathname = usePathname();
  const { activeWorkspace } = useAuth();

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1.5">
          <Eyebrow>
            {t('eyebrow', { workspaceName: activeWorkspace?.name ?? '' })}
          </Eyebrow>
          <h1 className="text-[34px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[44px]">
            {t('title')}
          </h1>
          <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
            {t('lead')}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          {activeWorkspace ? <RoleBadge role={activeWorkspace.role} /> : null}
          {headerAction}
        </div>
      </div>

      <div className="grid gap-6 lg:grid-cols-[256px_minmax(0,1fr)] lg:items-start">
        <aside className="lg:sticky lg:top-6">
          <Card className="p-4">
            <nav aria-label={t('navAria')} className="flex flex-col gap-4">
              {SETTINGS_NAV_GROUPS.map((group) => (
                <div key={group.id} className="flex flex-col gap-1">
                  <p className="px-2.5 pb-1 text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
                    {t(group.labelKey)}
                  </p>
                  <div className="flex flex-row flex-wrap gap-1 lg:flex-col lg:flex-nowrap">
                    {group.items.map((item) => {
                      const Icon = item.icon;
                      const active =
                        pathname === item.href ||
                        pathname.startsWith(`${item.href}/`);
                      return (
                        <Link
                          key={item.href}
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
                          <span>{t(item.labelKey)}</span>
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

/**
 * Shared layout shell for Workspace Settings routes with a page header and grouped navigation.
 */
export function WorkspaceSettingsShell({
  children,
  headerAction,
}: WorkspaceSettingsShellProps) {
  return (
    <AppShell>
      <SettingsShellInner headerAction={headerAction}>
        {children}
      </SettingsShellInner>
    </AppShell>
  );
}
