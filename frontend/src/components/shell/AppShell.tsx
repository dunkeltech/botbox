// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import {
  Bell,
  Bot as BotIcon,
  Check,
  ChevronDown,
  Menu,
  Plus,
  Settings,
  ShieldCheck,
  User as UserIcon,
  Users,
  X,
} from 'lucide-react';
import Link from 'next/link';
import { usePathname, useRouter } from 'next/navigation';
import {
  createContext,
  type FormEvent,
  type ReactNode,
  useContext,
  useEffect,
  useRef,
  useState,
} from 'react';
import { useTranslations } from 'next-intl';

import { initials as formatInitials } from '@/components/team/text';
import {
  Alert,
  BrandLogo,
  Button,
  Input,
  ModalDialog,
  RadioCardGroup,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { createWorkspace, requestJson } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { slugifyName } from '@/lib/slug';
import type { DriverType } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type ApprovalList = components['schemas']['ApprovalList'];

interface TeamShellContextValue {
  hasShellHeader: boolean;
  searchQuery: string;
  setSearchQuery: (value: string) => void;
  mobileDrawerOpen: boolean;
  setMobileDrawerOpen: (open: boolean) => void;
}

const TeamShellContext = createContext<TeamShellContextValue>({
  hasShellHeader: false,
  searchQuery: '',
  setSearchQuery: () => {},
  mobileDrawerOpen: false,
  setMobileDrawerOpen: () => {},
});

/**
 * Provides team header search state and mobile drawer controls when mounted inside AppShell variant="team".
 */
export function useTeamShell(): TeamShellContextValue {
  return useContext(TeamShellContext);
}

function extractBotId(pathname: string): string | null {
  const match = /^\/bots\/([^/]+)(?:\/|$)/.exec(pathname);
  const id = match?.[1];
  if (!id || id === 'new') return null;
  return id;
}

export interface AppShellProps {
  children: ReactNode;
  /** Standard light header + 1360px container, or full-bleed dark Team chat bar. */
  variant?: 'default' | 'team';
}

/**
 * Authenticated workspace chrome with horizontal top navigation, workspace switcher pill,
 * approvals indicator, account menu, and native <dialog> workspace creation modal.
 */
export function AppShell({ children, variant = 'default' }: AppShellProps) {
  const t = useTranslations('shell');
  const tTeam = useTranslations('team');
  const tActions = useTranslations('actions');
  const tCommon = useTranslations('common');
  const tRoles = useTranslations('roles');
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

  const [wsMenuOpen, setWsMenuOpen] = useState(false);
  const [accountMenuOpen, setAccountMenuOpen] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [pendingApprovals, setPendingApprovals] = useState(0);

  const [teamSearch, setTeamSearch] = useState('');
  const [teamDrawerOpen, setTeamDrawerOpen] = useState(false);

  const wsMenuRef = useRef<HTMLDivElement | null>(null);
  const accountMenuRef = useRef<HTMLDivElement | null>(null);

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

  useEffect(() => {
    if (!activeWorkspace || !user) {
      setPendingApprovals(0);
      return;
    }
    let cancelled = false;
    void requestJson<ApprovalList>(
      `/api/workspaces/${encodeURIComponent(activeWorkspace.id)}/approvals`,
    )
      .then((res) => {
        if (!cancelled && Array.isArray(res?.approvals)) {
          setPendingApprovals(
            res.approvals.filter((row) => row.status === 'pending').length,
          );
        }
      })
      .catch(() => {
        if (!cancelled) {
          setPendingApprovals(0);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [activeWorkspace, user, pathname]);

  useEffect(() => {
    setWsMenuOpen(false);
    setAccountMenuOpen(false);
    setMobileNavOpen(false);
  }, [pathname]);

  useEffect(() => {
    function handleOutside(event: MouseEvent) {
      const target = event.target as Node;
      if (wsMenuRef.current && !wsMenuRef.current.contains(target)) {
        setWsMenuOpen(false);
      }
      if (accountMenuRef.current && !accountMenuRef.current.contains(target)) {
        setAccountMenuOpen(false);
      }
    }
    function handleKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setWsMenuOpen(false);
        setAccountMenuOpen(false);
        setMobileNavOpen(false);
      }
    }
    document.addEventListener('mousedown', handleOutside);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('mousedown', handleOutside);
      document.removeEventListener('keydown', handleKey);
    };
  }, []);

  function openCreateModal() {
    setWsMenuOpen(false);
    setMobileNavOpen(false);
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
    setAccountMenuOpen(false);
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

  const activeBotId = extractBotId(pathname);
  const isTeam = variant === 'team';

  const navItems = [
    {
      href: '/',
      label: t('navFleet'),
      icon: BotIcon,
      active:
        pathname === '/' ||
        (pathname.startsWith('/bots/') && !pathname.endsWith('/secrets')),
      visible: true,
    },
    {
      href: '/team',
      label: t('navTeam'),
      icon: Users,
      active: pathname.startsWith('/team'),
      visible: true,
    },
    {
      href: activeBotId ? `/bots/${activeBotId}/secrets` : '/',
      label: t('navSecrets'),
      icon: ShieldCheck,
      active: pathname.endsWith('/secrets'),
      visible: activeBotId !== null,
    },
    {
      href: '/settings/general',
      label: t('navMembers'),
      icon: Settings,
      active: pathname.startsWith('/settings'),
      visible: true,
    },
  ].filter((item) => item.visible);

  const initials = formatInitials(user.displayName);

  const workspaceMenu = wsMenuOpen ? (
    <div
      role="menu"
      aria-label={t('workspaceMenuTitle')}
      className="absolute right-0 top-[calc(100%+8px)] z-50 w-64 rounded-md border border-line bg-paper p-2 text-ink shadow-card-hover"
    >
      <p className="px-2.5 py-1 text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
        {t('workspaceMenuTitle')}
      </p>
      <div className="mt-1 flex flex-col gap-1">
        {workspaces.map((ws) => {
          const isCurrent = ws.id === activeWorkspace?.id;
          return (
            <button
              key={ws.id}
              type="button"
              role="menuitemradio"
              aria-checked={isCurrent}
              onClick={() => {
                selectWorkspace(ws.id);
                setWsMenuOpen(false);
              }}
              className={`flex min-h-[44px] w-full items-center justify-between gap-2 rounded-sm px-2.5 text-left text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                isCurrent
                  ? 'bg-mint-soft text-green-600'
                  : 'text-ink hover:bg-paper-2'
              }`}
            >
              <span className="flex min-w-0 flex-col">
                <span className="truncate">{ws.name}</span>
                <span className="truncate font-mono text-[11px] font-normal text-muted">
                  {`${ws.slug} · ${tRoles(ws.role)}`}
                </span>
              </span>
              {isCurrent ? (
                <Check className="h-4 w-4 shrink-0" aria-hidden="true" />
              ) : null}
            </button>
          );
        })}
      </div>
      <div className="mt-2 border-t border-line pt-2">
        <button
          type="button"
          role="menuitem"
          onClick={openCreateModal}
          className="flex min-h-[44px] w-full items-center gap-2 rounded-sm px-2.5 text-left text-[14px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <Plus className="h-4 w-4 shrink-0" aria-hidden="true" />
          <span>{t('newWorkspace')}</span>
        </button>
      </div>
    </div>
  ) : null;

  const accountMenu = accountMenuOpen ? (
    <div
      role="menu"
      aria-label={t('accountAria')}
      className="absolute right-0 top-[calc(100%+8px)] z-50 w-64 rounded-md border border-line bg-paper p-3 text-ink shadow-card-hover"
    >
      <div className="flex flex-col gap-0.5 border-b border-line pb-3">
        <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
          {t('signedInLabel')}
        </span>
        <span className="truncate text-[15px] font-black text-ink">
          {user.displayName}
        </span>
        <span className="truncate font-mono text-[12px] text-muted">
          {user.email}
        </span>
      </div>
      <div className="pt-2">
        <button
          type="button"
          role="menuitem"
          onClick={() => void handleSignOut()}
          className="flex min-h-[44px] w-full items-center rounded-sm px-2.5 text-left text-[14px] font-bold text-ink transition duration-150 hover:bg-mint-soft hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          {t('signOut')}
        </button>
      </div>
    </div>
  ) : null;

  return (
    <TeamShellContext.Provider
      value={{
        hasShellHeader: isTeam,
        searchQuery: teamSearch,
        setSearchQuery: setTeamSearch,
        mobileDrawerOpen: teamDrawerOpen,
        setMobileDrawerOpen: setTeamDrawerOpen,
      }}
    >
      <div
        className={
          isTeam
            ? 'flex h-screen min-h-[600px] flex-col overflow-hidden bg-paper text-ink'
            : 'flex min-h-screen flex-col bg-paper-2 text-ink'
        }
      >
        <header className="relative z-30 shrink-0 bg-terminal text-on-dark">
          <div className="flex min-h-[54px] flex-wrap items-center gap-x-5 gap-y-2 px-4 py-1.5 sm:px-6">
            <div className="flex items-center gap-2">
              {isTeam ? (
                <button
                  type="button"
                  aria-label={t('teamMenuToggleAria')}
                  aria-expanded={teamDrawerOpen}
                  onClick={() => setTeamDrawerOpen((prev) => !prev)}
                  className="relative grid h-11 w-11 place-items-center rounded-sm bg-dark-2 text-paper transition duration-150 hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light lg:hidden"
                >
                  {teamDrawerOpen ? (
                    <X className="h-5 w-5" aria-hidden="true" />
                  ) : (
                    <Menu className="h-5 w-5" aria-hidden="true" />
                  )}
                  {pendingApprovals > 0 && !teamDrawerOpen ? (
                    <span className="absolute right-1 top-1 flex h-4 min-w-[16px] items-center justify-center rounded-pill bg-amber-on-dark px-1 text-[10px] font-black text-terminal">
                      {pendingApprovals}
                    </span>
                  ) : null}
                </button>
              ) : (
                <button
                  type="button"
                  aria-label={t('menuToggleAria')}
                  aria-expanded={mobileNavOpen}
                  onClick={() => setMobileNavOpen((prev) => !prev)}
                  className="grid h-11 w-11 place-items-center rounded-sm bg-dark-2 text-paper transition duration-150 hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light md:hidden"
                >
                  {mobileNavOpen ? (
                    <X className="h-5 w-5" aria-hidden="true" />
                  ) : (
                    <Menu className="h-5 w-5" aria-hidden="true" />
                  )}
                </button>
              )}

              <Link
                href="/"
                aria-label={t('homeAria')}
                className="inline-flex min-h-[44px] items-center rounded-sm px-1 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
              >
                <BrandLogo dark size={30} />
              </Link>
            </div>

            <nav
              aria-label={t('navAria')}
              className="hidden items-center gap-1 md:flex"
            >
              {navItems.map((item) => (
                <Link
                  key={item.href}
                  href={item.href}
                  aria-current={item.active ? 'page' : undefined}
                  className={`inline-flex min-h-[44px] items-center px-3 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light ${
                    item.active
                      ? 'text-paper shadow-[inset_0_-2px_0_#5eab60]'
                      : 'text-on-dark hover:text-paper'
                  }`}
                >
                  {item.label}
                </Link>
              ))}
            </nav>

            {isTeam ? (
              <div className="order-last w-full sm:order-none sm:mx-auto sm:max-w-[460px] sm:flex-1">
                <label htmlFor="team-search" className="sr-only">
                  {tTeam('searchLabel')}
                </label>
                <input
                  id="team-search"
                  type="search"
                  value={teamSearch}
                  placeholder={tTeam('searchPlaceholder')}
                  autoComplete="off"
                  onChange={(event) => setTeamSearch(event.target.value)}
                  className="min-h-[38px] w-full rounded-sm border border-dark bg-dark-2 px-3 text-[14px] text-paper placeholder:text-on-dark-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                />
              </div>
            ) : (
              <div className="hidden flex-1 md:block" />
            )}

            <div className="ml-auto flex items-center gap-2 sm:ml-0">
              {activeWorkspace ? (
                <div ref={wsMenuRef} className="relative">
                  <button
                    type="button"
                    aria-label={t('switchWorkspaceAria', {
                      name: activeWorkspace.name,
                      role: tRoles(activeWorkspace.role),
                    })}
                    aria-haspopup="menu"
                    aria-expanded={wsMenuOpen}
                    onClick={() => setWsMenuOpen((prev) => !prev)}
                    className="inline-flex min-h-[40px] items-center gap-2 rounded-sm bg-dark px-3 text-[13px] font-bold text-paper transition duration-150 hover:bg-dark-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                  >
                    <span
                      aria-hidden="true"
                      className="h-2 w-2 rounded-pill bg-green-light"
                    />
                    <span className="max-w-[120px] truncate sm:max-w-[180px]">
                      {activeWorkspace.name}
                    </span>
                    <span className="hidden rounded-pill bg-dark-2 px-1.5 py-0.5 text-[11px] font-bold text-mint sm:inline">
                      {tRoles(activeWorkspace.role)}
                    </span>
                    <ChevronDown
                      className="h-3.5 w-3.5 text-on-dark-muted"
                      aria-hidden="true"
                    />
                  </button>
                  {workspaceMenu}
                </div>
              ) : null}

              <Link
                href="/team/approvals"
                aria-label={t('notificationsAria')}
                className="relative grid h-10 w-10 place-items-center rounded-sm bg-dark text-on-dark transition duration-150 hover:bg-dark-2 hover:text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
              >
                <Bell className="h-[18px] w-[18px]" aria-hidden="true" />
                {pendingApprovals > 0 ? (
                  <span className="absolute -right-1 -top-1 flex h-5 min-w-[20px] items-center justify-center rounded-pill bg-amber-on-dark px-1 text-[11px] font-black text-terminal">
                    {pendingApprovals}
                  </span>
                ) : null}
              </Link>

              <div ref={accountMenuRef} className="relative">
                <button
                  type="button"
                  aria-label={t('accountAria')}
                  aria-haspopup="menu"
                  aria-expanded={accountMenuOpen}
                  onClick={() => setAccountMenuOpen((prev) => !prev)}
                  className="grid h-10 w-10 place-items-center rounded-sm bg-dark text-[13px] font-black text-mint transition duration-150 hover:bg-dark-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                >
                  {initials || (
                    <UserIcon
                      className="h-[18px] w-[18px]"
                      aria-hidden="true"
                    />
                  )}
                </button>
                {accountMenu}
              </div>
            </div>
          </div>

          {!isTeam && mobileNavOpen ? (
            <nav
              aria-label={t('navAria')}
              className="flex flex-col gap-1 border-t border-dark bg-dark-2 px-4 py-3 md:hidden"
            >
              {navItems.map((item) => {
                const Icon = item.icon;
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    aria-current={item.active ? 'page' : undefined}
                    className={`flex min-h-[44px] items-center gap-3 rounded-sm px-3 text-[15px] font-bold ${
                      item.active
                        ? 'bg-green-600 text-paper'
                        : 'text-on-dark hover:bg-dark hover:text-paper'
                    }`}
                  >
                    <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
                    <span>{item.label}</span>
                  </Link>
                );
              })}
            </nav>
          ) : null}
        </header>

        {isTeam ? (
          <main className="flex min-h-0 flex-1 flex-col overflow-hidden">
            {children}
          </main>
        ) : (
          <>
            <main className="mx-auto w-full max-w-[1360px] flex-1 px-4 py-7 sm:px-6 sm:py-9">
              {children}
            </main>
            <footer className="mt-auto bg-dark text-on-dark">
              <div className="mx-auto flex max-w-[1360px] flex-wrap items-center justify-between gap-3 px-4 py-4 text-[13px] sm:px-6">
                <p className="flex items-center gap-2">
                  <span className="font-black text-paper">
                    {tCommon('brandLeft')}
                    <span className="text-green-light">
                      {tCommon('brandDot')}
                    </span>
                    {tCommon('brandRight')}
                  </span>
                  <span className="text-on-dark-muted">
                    {`· ${tCommon('endorsement')}`}
                  </span>
                </p>
                <div className="flex flex-wrap items-center gap-4 font-mono text-[12px] text-on-dark-muted">
                  <span>{t('footerDrivers')}</span>
                  <Link
                    href="/dev/styleguide"
                    className="inline-flex min-h-[36px] items-center font-sans text-[13px] font-bold text-on-dark underline-offset-4 hover:text-paper hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                  >
                    {t('navStyleguide')}
                  </Link>
                </div>
              </div>
            </footer>
          </>
        )}

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
    </TeamShellContext.Provider>
  );
}
