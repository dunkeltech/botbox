'use client';

import { Bell, PenSquare, Plus, ShieldCheck, X } from 'lucide-react';
import Link from 'next/link';
import { useTranslations } from 'next-intl';

import { initials } from '@/components/team/text';
import { BotAvatar } from '@/components/ui';
import type {
  Bot,
  BotStatus,
  WorkspaceMember,
  WorkspaceRole,
} from '@/types/api';
import type { components } from '@/types/openapi.gen';

type Group = components['schemas']['Group'];

export type TeamSidebarSelection =
  | { type: 'group'; id: string }
  | { type: 'dm'; botId: string }
  | { type: 'activity' }
  | { type: 'approvals' }
  | { type: 'none' };

function statusKey(
  status: BotStatus,
): 'statusWorking' | 'statusThinking' | 'statusIdle' | 'statusStopped' {
  if (status === 'action') return 'statusWorking';
  if (status === 'thinking') return 'statusThinking';
  if (status === 'stopped') return 'statusStopped';
  return 'statusIdle';
}

function moodFor(
  status: BotStatus,
): 'action' | 'thinking' | 'idle' | 'offline' {
  if (status === 'action') return 'action';
  if (status === 'thinking') return 'thinking';
  if (status === 'stopped') return 'offline';
  return 'idle';
}

export interface TeamSidebarProps {
  workspaceName: string;
  role: WorkspaceRole;
  groups: Group[];
  bots: Bot[];
  humans: WorkspaceMember[];
  totalBots: number;
  totalHumans: number;
  pendingApprovalsCount?: number;
  selection: TeamSidebarSelection;
  canChange: boolean;
  sidebarEmpty?: boolean;
  mobileOpen?: boolean;
  onCloseMobile?: () => void;
  onSelectGroup?: (groupId: string) => void;
  onSelectDm?: (botId: string) => void;
  onSelectActivity?: () => void;
  onNewGroup?: () => void;
}

/**
 * Slack-style dark-2 workspace sidebar shared by Team Chat and Approvals Inbox,
 * rendered statically on desktop (264 px) and as a slide-over drawer on phones.
 */
export function TeamSidebar({
  workspaceName,
  role,
  groups,
  bots,
  humans,
  totalBots,
  totalHumans,
  pendingApprovalsCount,
  selection,
  canChange,
  sidebarEmpty = false,
  mobileOpen = false,
  onCloseMobile,
  onSelectGroup,
  onSelectDm,
  onSelectActivity,
  onNewGroup,
}: TeamSidebarProps) {
  const t = useTranslations('team');
  const roles = useTranslations('roles');

  const sideButton = (active: boolean) =>
    `flex min-h-[44px] w-full items-center gap-2.5 rounded-sm px-2.5 text-left text-[15px] transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light ${
      active
        ? 'bg-green-600 font-black text-paper'
        : 'font-bold text-on-dark hover:bg-dark hover:text-paper'
    }`;

  const content = (
    <nav
      aria-label={t('conversations')}
      className="flex h-full flex-col gap-3 overflow-y-auto bg-dark-2 p-3 text-on-dark"
    >
      <div className="flex items-start justify-between gap-2 px-1.5">
        <div className="min-w-0">
          <p className="truncate text-[17px] font-black text-paper">
            {workspaceName}
          </p>
          <p className="text-[13px] text-on-dark-muted">
            {t('counts', { bots: totalBots, humans: totalHumans })}
          </p>
          <p className="mt-0.5 text-[12px] font-bold text-green-light">
            {roles(role)}
          </p>
        </div>
        <div className="flex items-center gap-1">
          {canChange && onNewGroup ? (
            <button
              type="button"
              aria-label={t('composeGroup')}
              onClick={() => {
                onNewGroup();
                onCloseMobile?.();
              }}
              className="grid h-10 w-10 place-items-center rounded-sm bg-paper text-dark-2 transition duration-150 hover:bg-mint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
            >
              <PenSquare className="h-4 w-4" aria-hidden="true" />
            </button>
          ) : null}
          {onCloseMobile ? (
            <button
              type="button"
              aria-label={t('closeSidebar')}
              onClick={onCloseMobile}
              className="grid h-10 w-10 place-items-center rounded-sm text-on-dark hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light lg:hidden"
            >
              <X className="h-4 w-4" aria-hidden="true" />
            </button>
          ) : null}
        </div>
      </div>

      <div className="flex flex-col gap-1 border-b border-dark pb-2.5">
        <Link
          href="/team/approvals"
          aria-current={selection.type === 'approvals' ? 'page' : undefined}
          onClick={() => onCloseMobile?.()}
          className={sideButton(selection.type === 'approvals')}
        >
          <ShieldCheck
            className="h-4 w-4 shrink-0 text-green-light"
            aria-hidden="true"
          />
          <span className="flex-1">{t('approvals')}</span>
          {pendingApprovalsCount !== undefined ? (
            <span className="rounded-pill bg-amber-on-dark px-2 py-0.5 text-[12px] font-black text-terminal">
              {t('approvalsCount', { count: pendingApprovalsCount })}
            </span>
          ) : null}
        </Link>
        {onSelectActivity ? (
          <button
            type="button"
            aria-current={selection.type === 'activity' ? 'true' : undefined}
            onClick={() => {
              onSelectActivity();
              onCloseMobile?.();
            }}
            className={sideButton(selection.type === 'activity')}
          >
            <Bell className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>{t('activity')}</span>
          </button>
        ) : (
          <Link
            href="/team"
            onClick={() => onCloseMobile?.()}
            className={sideButton(false)}
          >
            <Bell className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>{t('activity')}</span>
          </Link>
        )}
      </div>

      <section className="flex flex-col gap-1">
        <h2 className="px-2.5 text-[12px] font-bold uppercase tracking-[0.08em] text-on-dark-muted">
          {t('groups')}
        </h2>
        {groups.map((group) => {
          const active =
            selection.type === 'group' && selection.id === group.id;
          if (!onSelectGroup) {
            return (
              <Link
                key={group.id}
                href={`/team?group=${encodeURIComponent(group.id)}`}
                onClick={() => onCloseMobile?.()}
                className={sideButton(active)}
              >
                <span aria-hidden="true" className="w-4 text-center font-black">
                  #
                </span>
                <span className="min-w-0 flex-1 truncate">{group.name}</span>
                {group.unread > 0 ? (
                  <span className="rounded-pill bg-green-light px-2 py-0.5 text-[12px] font-black text-terminal">
                    {t('unread', { count: Number(group.unread) })}
                  </span>
                ) : null}
              </Link>
            );
          }
          return (
            <button
              key={group.id}
              type="button"
              aria-current={active ? 'true' : undefined}
              onClick={() => {
                onSelectGroup(group.id);
                onCloseMobile?.();
              }}
              className={sideButton(active)}
            >
              <span aria-hidden="true" className="w-4 text-center font-black">
                #
              </span>
              <span className="min-w-0 flex-1 truncate">{group.name}</span>
              {group.unread > 0 ? (
                <span className="rounded-pill bg-green-light px-2 py-0.5 text-[12px] font-black text-terminal">
                  {t('unread', { count: Number(group.unread) })}
                </span>
              ) : null}
            </button>
          );
        })}
        {canChange && onNewGroup ? (
          <button
            type="button"
            onClick={() => {
              onNewGroup();
              onCloseMobile?.();
            }}
            className="flex min-h-[44px] w-full items-center gap-2.5 rounded-sm px-2.5 text-left text-[15px] font-bold text-on-dark-muted transition duration-150 hover:bg-dark hover:text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
          >
            <Plus className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>{t('newGroup')}</span>
          </button>
        ) : null}
      </section>

      <section className="flex flex-col gap-1">
        <h2 className="px-2.5 text-[12px] font-bold uppercase tracking-[0.08em] text-on-dark-muted">
          {t('colleagues')}
        </h2>
        {bots.map((bot) => {
          const active = selection.type === 'dm' && selection.botId === bot.id;
          if (!onSelectDm) {
            return (
              <Link
                key={bot.id}
                href={`/team?dm=${encodeURIComponent(bot.id)}`}
                onClick={() => onCloseMobile?.()}
                className={sideButton(active)}
              >
                <BotAvatar
                  seed={bot.avatarSeed}
                  mood={moodFor(bot.status)}
                  size={28}
                  showStatusDot
                  decorative
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[14px]">{bot.name}</span>
                  <span
                    className={`block truncate text-[12px] ${
                      active ? 'text-mint' : 'text-on-dark-muted'
                    }`}
                  >
                    {t(statusKey(bot.status))}
                  </span>
                </span>
              </Link>
            );
          }
          return (
            <button
              key={bot.id}
              type="button"
              aria-current={active ? 'true' : undefined}
              onClick={() => {
                onSelectDm(bot.id);
                onCloseMobile?.();
              }}
              className={sideButton(active)}
            >
              <BotAvatar
                seed={bot.avatarSeed}
                mood={moodFor(bot.status)}
                size={28}
                showStatusDot
                decorative
              />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[14px]">{bot.name}</span>
                <span
                  className={`block truncate text-[12px] ${
                    active ? 'text-mint' : 'text-on-dark-muted'
                  }`}
                >
                  {t(statusKey(bot.status))}
                </span>
              </span>
            </button>
          );
        })}
        {canChange ? (
          <Link
            href="/bots/new"
            onClick={() => onCloseMobile?.()}
            className="flex min-h-[44px] items-center gap-2.5 rounded-sm px-2.5 text-[15px] font-bold text-on-dark-muted transition duration-150 hover:bg-dark hover:text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
          >
            <Plus className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>{t('hire')}</span>
          </Link>
        ) : null}
      </section>

      <section className="flex flex-col gap-1">
        <h2 className="px-2.5 text-[12px] font-bold uppercase tracking-[0.08em] text-on-dark-muted">
          {t('humans')}
        </h2>
        {humans.map((human) => (
          <div
            key={human.userId}
            className="flex min-h-[44px] items-center gap-2.5 px-2.5"
          >
            <span className="grid h-7 w-7 shrink-0 place-items-center rounded-sm bg-hex-2 text-[11px] font-black text-paper">
              {initials(human.displayName)}
            </span>
            <span className="min-w-0">
              <span className="block truncate text-[14px] font-bold text-paper">
                {human.displayName}
              </span>
              <span className="block text-[12px] text-on-dark-muted">
                {roles(human.role)}
              </span>
            </span>
          </div>
        ))}
        {canChange ? (
          <Link
            href="/settings/members"
            onClick={() => onCloseMobile?.()}
            className="flex min-h-[44px] items-center rounded-sm px-2.5 text-[15px] font-bold text-on-dark-muted transition duration-150 hover:bg-dark hover:text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
          >
            {t('manageMembers')}
          </Link>
        ) : null}
      </section>

      {sidebarEmpty ? (
        <p className="px-2.5 text-[13px] text-on-dark-muted">
          {t('emptyMatch')}
        </p>
      ) : null}
    </nav>
  );

  return (
    <>
      {mobileOpen ? (
        <div
          className="fixed inset-0 z-30 bg-ink/45 lg:hidden"
          aria-hidden="true"
          onClick={onCloseMobile}
        />
      ) : null}
      <div
        className={
          mobileOpen
            ? 'fixed inset-y-0 left-0 z-40 w-[280px] max-w-[85vw] shadow-card-hover lg:static lg:z-auto lg:h-full lg:w-[264px] lg:shrink-0 lg:shadow-none'
            : 'hidden h-full w-[264px] shrink-0 lg:block'
        }
      >
        {content}
      </div>
    </>
  );
}
