'use client';

import { Bot, Palette, Plus, PlusSquare, Users } from 'lucide-react';
import Link from 'next/link';
import { usePathname, useRouter } from 'next/navigation';
import { type FormEvent, type ReactNode, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  BrandLogo,
  Button,
  Input,
  ModalDialog,
  RadioCardGroup,
  RoleBadge,
  Select,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { createWorkspace } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canCreateBot as canRoleCreateBot } from '@/lib/rbac';
import { slugifyName } from '@/lib/slug';
import type { DriverType } from '@/types/api';

/**
 * Authenticated workspace chrome with navigation, workspace switcher,
 * role badge, and native <dialog> workspace creation modal.
 */
export function AppShell({ children }: { children: ReactNode }) {
  const t = useTranslations('shell');
  const tActions = useTranslations('actions');
  const tCommon = useTranslations('common');
  const tErrors = useTranslations('errors');

  const router = useRouter();
  const pathname = usePathname();
  const {
    user,
    workspaces,
    activeWorkspace,
    setupRequired,
    isLoading,
    selectWorkspace,
    reloadWorkspaces,
    signOut,
  } = useAuth();

  const [createOpen, setCreateOpen] = useState(false);
  const [wsName, setWsName] = useState('');
  const [wsSlug, setWsSlug] = useState('');
  const [slugTouched, setSlugTouched] = useState(false);
  const [wsDriver, setWsDriver] = useState<DriverType>('docker');
  const [creating, setCreating] = useState(false);
  const [modalErrorKey, setModalErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  useEffect(() => {
    if (isLoading) {
      return;
    }
    if (setupRequired) {
      router.replace('/setup');
      return;
    }
    if (!user) {
      router.replace('/login');
    }
  }, [isLoading, setupRequired, user, router]);

  function openCreateModal() {
    setWsName('');
    setWsSlug('');
    setSlugTouched(false);
    setWsDriver('docker');
    setModalErrorKey(null);
    setCreateOpen(true);
  }

  function closeCreateModal() {
    setCreateOpen(false);
    setModalErrorKey(null);
  }

  async function handleCreateWorkspace(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setModalErrorKey(null);
    setCreating(true);
    try {
      const created = await createWorkspace({
        name: wsName.trim(),
        slug: wsSlug.trim(),
        defaultDriver: wsDriver,
      });
      await reloadWorkspaces(created.id);
      setCreateOpen(false);
    } catch (err) {
      setModalErrorKey(resolveErrorKey(err));
    } finally {
      setCreating(false);
    }
  }

  async function handleSignOut() {
    await signOut();
    router.replace('/login');
  }

  if (isLoading || setupRequired || !user) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-paper-2 p-6">
        <p role="status" className="text-[15px] font-bold text-ink-2">
          {tActions('loading')}
        </p>
      </div>
    );
  }

  const canCreateBot = canRoleCreateBot(activeWorkspace?.role);

  const navItems = [
    {
      href: '/',
      label: t('navFleet'),
      icon: Bot,
      active:
        pathname === '/' ||
        (pathname.startsWith('/bots/') && pathname !== '/bots/new'),
      visible: true,
    },
    {
      href: '/bots/new',
      label: t('navBuilder'),
      icon: PlusSquare,
      active: pathname === '/bots/new',
      visible: canCreateBot,
    },
    {
      href: '/settings/members',
      label: t('navMembers'),
      icon: Users,
      active: pathname.startsWith('/settings/members'),
      visible: true,
    },
    {
      href: '/dev/styleguide',
      label: t('navStyleguide'),
      icon: Palette,
      active: pathname.startsWith('/dev/styleguide'),
      visible: true,
    },
  ].filter((item) => item.visible);

  return (
    <div className="flex min-h-screen flex-col bg-paper-2 lg:flex-row">
      <aside className="flex w-full shrink-0 flex-col justify-between gap-6 border-b border-line bg-paper p-5 lg:w-72 lg:border-b-0 lg:border-r">
        <div className="flex flex-col gap-6">
          <div className="flex flex-col gap-1">
            <Link
              href="/"
              className="inline-flex min-h-[44px] items-center rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <BrandLogo />
            </Link>
            <p className="text-[12px] font-bold text-muted">
              {tCommon('endorsement')}
            </p>
          </div>

          <div className="flex flex-col gap-3 rounded-sm border border-line bg-paper-2 p-3.5">
            <Select
              label={t('workspaceLabel')}
              value={activeWorkspace?.id ?? ''}
              disabled={workspaces.length === 0}
              onChange={(event) => selectWorkspace(event.target.value)}
            >
              {workspaces.map((ws) => (
                <option key={ws.id} value={ws.id}>
                  {ws.name}
                </option>
              ))}
            </Select>

            {activeWorkspace ? (
              <div className="flex items-center justify-between gap-2">
                <RoleBadge role={activeWorkspace.role} />
                <span className="truncate font-mono text-[12px] text-muted">
                  {activeWorkspace.slug}
                </span>
              </div>
            ) : null}

            <Button
              variant="secondary"
              type="button"
              onClick={openCreateModal}
              className="w-full"
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              {t('newWorkspace')}
            </Button>
          </div>

          <nav aria-label={t('navAria')} className="flex flex-col gap-1.5">
            {navItems.map((item) => {
              const Icon = item.icon;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  aria-current={item.active ? 'page' : undefined}
                  className={`flex min-h-[44px] items-center gap-3 rounded-sm border px-3.5 text-[15px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                    item.active
                      ? 'border-mint-line bg-mint-soft text-green-600'
                      : 'border-transparent text-ink-2 hover:border-line hover:bg-paper-2 hover:text-ink'
                  }`}
                >
                  <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
                  <span>{item.label}</span>
                </Link>
              );
            })}
          </nav>
        </div>

        <div className="flex flex-col gap-3 border-t border-line pt-4">
          <div className="flex flex-col gap-0.5">
            <span className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('signedInLabel')}
            </span>
            <span className="truncate text-[14px] font-bold text-ink">
              {user.displayName}
            </span>
            <span className="truncate font-mono text-[12px] text-muted">
              {user.email}
            </span>
          </div>
          <Button
            variant="secondary"
            type="button"
            onClick={() => void handleSignOut()}
            className="w-full"
          >
            {t('signOut')}
          </Button>
        </div>
      </aside>

      <main className="min-w-0 flex-1 px-4 py-8 sm:px-8 lg:px-10">
        <div className="mx-auto max-w-[1160px]">{children}</div>
      </main>

      <ModalDialog
        open={createOpen}
        onClose={closeCreateModal}
        title={t('createModalTitle')}
        subtitle={t('createModalSubtitle')}
        closeLabel={tActions('close')}
      >
        <form
          onSubmit={(event) => void handleCreateWorkspace(event)}
          className="flex flex-col gap-4"
        >
          {modalErrorKey ? (
            <Alert tone="warning">{tErrors(modalErrorKey)}</Alert>
          ) : null}

          <Input
            label={t('workspaceNameLabel')}
            placeholder={t('workspaceNamePlaceholder')}
            value={wsName}
            autoFocus
            required
            onChange={(event) => {
              const nextName = event.target.value;
              setWsName(nextName);
              if (!slugTouched) {
                setWsSlug(slugifyName(nextName));
              }
            }}
          />

          <Input
            label={t('workspaceSlugLabel')}
            helperText={t('workspaceSlugHelper')}
            placeholder={t('workspaceSlugPlaceholder')}
            mono
            autoCorrect="off"
            autoCapitalize="none"
            spellCheck={false}
            value={wsSlug}
            required
            onChange={(event) => {
              setSlugTouched(true);
              setWsSlug(event.target.value);
            }}
          />

          <RadioCardGroup<DriverType>
            legend={t('driverLegend')}
            name="new-workspace-driver"
            value={wsDriver}
            onChange={setWsDriver}
            options={[
              { value: 'docker', title: t('driverDocker') },
              { value: 'kubernetes', title: t('driverK8s') },
            ]}
          />

          <div className="flex flex-wrap justify-end gap-3 pt-2">
            <Button
              variant="secondary"
              type="button"
              onClick={closeCreateModal}
              disabled={creating}
            >
              {tActions('cancel')}
            </Button>
            <Button type="submit" arrow disabled={creating}>
              {creating ? t('creatingWorkspace') : t('createWorkspaceSubmit')}
            </Button>
          </div>
        </form>
      </ModalDialog>
    </div>
  );
}
