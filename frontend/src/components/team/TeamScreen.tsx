'use client';

import { useEffect, useState, type ReactNode } from 'react';
import Link from 'next/link';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { GroupConversation } from '@/components/team/GroupConversation';
import { useApprovalList } from '@/components/team/useApprovals';
import { DirectDetails, GroupDetails } from '@/components/team/GroupDetails';
import { NewGroupDialog } from '@/components/team/NewGroupDialog';
import { groupSettingsBody } from '@/components/team/text';
import { Alert, BotAvatar } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import { resolveErrorKey, type ErrorTranslationKey } from '@/lib/errors';
import type {
  Bot,
  BotListResponse,
  BotStatus,
  WorkspaceMemberListResponse,
  WorkspaceRole,
} from '@/types/api';
import type { components } from '@/types/openapi.gen';

type Group = components['schemas']['Group'];
type Notification = components['schemas']['Notification'];
type Selection =
  | { type: 'group'; id: string }
  | { type: 'dm'; botId: string }
  | { type: 'activity' }
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

function pauseKey(
  reason: string,
): 'pauseTurnLimit' | 'pauseLoop' | 'pauseApproval' | 'pauseBudget' | 'paused' {
  if (reason === 'turn_limit') return 'pauseTurnLimit';
  if (reason === 'loop') return 'pauseLoop';
  if (reason === 'approval') return 'pauseApproval';
  if (reason === 'budget') return 'pauseBudget';
  return 'paused';
}

function kindKey(
  kind: Notification['kind'],
): 'kindMention' | 'kindApproval' | 'kindBudget' {
  if (kind === 'approval') return 'kindApproval';
  if (kind === 'budget') return 'kindBudget';
  return 'kindMention';
}

function matches(name: string, needle: string): boolean {
  return needle === '' || name.toLowerCase().includes(needle);
}

/**
 * Team chat: groups, colleague DMs, activity, and group settings.
 * Search filters loaded names and the open transcript. It is not a server index.
 */
export function TeamScreen({
  workspaceId,
  userId,
  displayName,
  role,
  workspaceName,
}: {
  workspaceId: string;
  userId: string;
  displayName: string;
  role: WorkspaceRole;
  workspaceName: string;
}) {
  const t = useTranslations('team');
  const roles = useTranslations('roles');
  const actions = useTranslations('actions');
  const errors = useTranslations('errors');
  const client = useQueryClient();
  const canChange = role !== 'viewer';
  const [selection, setSelection] = useState<Selection>({ type: 'none' });
  const [query, setQuery] = useState('');
  const [dialogOpen, setDialogOpen] = useState(false);
  const [presetBotId, setPresetBotId] = useState<string | undefined>(undefined);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [saveError, setSaveError] = useState<ErrorTranslationKey | null>(null);
  const base = `/api/workspaces/${encodeURIComponent(workspaceId)}`;
  const groups = useQuery({
    queryKey: ['groups', workspaceId],
    queryFn: () => requestJson<Group[]>(`${base}/groups`),
    refetchInterval: 10000,
  });
  const bots = useQuery({
    queryKey: ['bots', workspaceId],
    queryFn: () => requestJson<BotListResponse>(`${base}/bots`),
  });
  const members = useQuery({
    queryKey: ['members', workspaceId],
    queryFn: () => requestJson<WorkspaceMemberListResponse>(`${base}/members`),
  });
  // The badge is the pending slice of the shared list, so a decision here
  // or in the inbox updates it without a reload.
  const approvals = useApprovalList(workspaceId);
  const notifications = useQuery({
    queryKey: ['notifications', workspaceId],
    queryFn: () =>
      requestJson<components['schemas']['NotificationList']>(
        `${base}/notifications`,
      ),
    refetchInterval: 10000,
  });
  const groupList = groups.data ?? [];
  const botList = bots.data?.bots ?? [];
  const humanList = members.data?.members ?? [];

  useEffect(() => {
    if (selection.type !== 'none') return;
    const first = groups.data?.[0];
    if (!first) return;
    setSelection({ type: 'group', id: first.id });
  }, [groups.data, selection.type]);

  const openGroupId = selection.type === 'group' ? selection.id : '';
  useEffect(() => {
    if (openGroupId === '') return;
    let ignore = false;
    // The group object changes on every refetch. Depending on it would post
    // read, invalidate the list, and loop. The id is the open conversation.
    void requestJson<void>(
      `${base}/groups/${encodeURIComponent(openGroupId)}/read`,
      {
        method: 'POST',
      },
    )
      .then(() => {
        if (!ignore) {
          void client.invalidateQueries({ queryKey: ['groups', workspaceId] });
        }
      })
      .catch(() => {
        // A failed read leaves the badge. Opening the group again tries once more.
      });
    return () => {
      ignore = true;
    };
  }, [base, client, openGroupId, workspaceId]);

  const selectedGroup =
    selection.type === 'group'
      ? groupList.find((group) => group.id === selection.id)
      : undefined;
  const selectedBot =
    selection.type === 'dm'
      ? botList.find((bot) => bot.id === selection.botId)
      : undefined;
  const needle = query.trim().toLowerCase();
  const visibleGroups = groupList.filter(
    (group) => matches(group.name, needle) || group.id === openGroupId,
  );
  const visibleBots = botList.filter(
    (bot) =>
      matches(bot.name, needle) ||
      (selection.type === 'dm' && bot.id === selection.botId),
  );
  const visibleHumans = humanList.filter((human) =>
    matches(human.displayName, needle),
  );
  const sidebarEmpty =
    needle !== '' &&
    visibleGroups.length === 0 &&
    visibleBots.length === 0 &&
    visibleHumans.length === 0;

  function openDialog(botId?: string) {
    setPresetBotId(botId);
    setCreateError(null);
    setDialogOpen(true);
  }

  async function createGroup(
    body: components['schemas']['CreateGroupRequest'],
  ) {
    setCreating(true);
    setCreateError(null);
    try {
      const created = await requestJson<Group>(`${base}/groups`, {
        method: 'POST',
        body,
      });
      client.setQueryData<Group[]>(['groups', workspaceId], (current) => [
        created,
        ...(current ?? []).filter((group) => group.id !== created.id),
      ]);
      setSelection({ type: 'group', id: created.id });
      setDialogOpen(false);
    } catch (err) {
      setCreateError(resolveErrorKey(err));
    } finally {
      setCreating(false);
    }
  }

  async function patchGroup(
    group: Group,
    patch: Parameters<typeof groupSettingsBody>[1],
  ) {
    setSaveError(null);
    try {
      const updated = await requestJson<Group>(
        `${base}/groups/${encodeURIComponent(group.id)}`,
        { method: 'PATCH', body: groupSettingsBody(group, patch) },
      );
      client.setQueryData<Group[]>(['groups', workspaceId], (current) =>
        (current ?? []).map((row) => (row.id === updated.id ? updated : row)),
      );
    } catch (err) {
      setSaveError(resolveErrorKey(err));
    }
  }

  const sideButton = (active: boolean) =>
    `flex min-h-11 w-full items-center gap-2 rounded-sm px-2.5 text-left text-[15px] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light ${active ? 'bg-terminal font-black text-paper' : 'font-bold text-on-dark hover:bg-dark'}`;

  let main: ReactNode;
  if (groups.isPending) {
    main = <p className="p-5 text-[15px]">{actions('loading')}</p>;
  } else if (groups.isError && !groups.data) {
    // Later refetch errors keep the cached list, so an open draft stays put.
    main = (
      <div className="p-5">
        <Alert tone="warning">{t('loadFailed')}</Alert>
      </div>
    );
  } else if (selection.type === 'activity') {
    const notes = notifications.data?.notifications ?? [];
    main = (
      <div className="flex flex-col gap-3 p-5">
        <h1 className="text-[20px] font-black">{t('activityTitle')}</h1>
        {notes.length === 0 ? (
          <p className="text-[15px] text-ink-2">{t('activityEmpty')}</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {notes.map((note) => {
              const group = groupList.find(
                (row) => row.id === note.conversationId,
              );
              return (
                <li
                  key={note.id}
                  className="flex flex-wrap items-center gap-2 rounded-sm border border-line px-3 py-2"
                >
                  <span className="font-black">{t(kindKey(note.kind))}</span>
                  {group ? <span>{group.name}</span> : null}
                  {group ? (
                    <button
                      type="button"
                      onClick={() =>
                        setSelection({ type: 'group', id: group.id })
                      }
                      className="inline-flex min-h-11 items-center rounded-sm px-2 font-bold text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                    >
                      {t('openGroup', { name: group.name })}
                    </button>
                  ) : null}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    );
  } else if (selectedGroup) {
    main = (
      <div className="flex min-h-[520px] flex-col">
        <header className="flex flex-col gap-2 border-b border-line px-5 py-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <h1 className="text-[20px] font-black">
                <span aria-hidden="true" className="font-bold text-muted">
                  #{' '}
                </span>
                {selectedGroup.name}
              </h1>
              <p className="truncate text-[14px] text-muted">
                {selectedGroup.goal.trim() || t('noGoal')}
              </p>
            </div>
            <p className="rounded-pill border border-mint-line bg-mint-soft px-2.5 py-1 text-[12px] font-bold text-green-600">
              {selectedGroup.botTalk ? t('botTalkOn') : t('botTalkOff')}
            </p>
          </div>
          <p className="text-[13px] text-muted">
            {t('memberSummary', { count: selectedGroup.members.length })}
          </p>
          {selectedGroup.paused ? (
            <p className="rounded-sm border border-mint-line bg-mint-soft px-3 py-2 text-[13px]">
              {t(pauseKey(selectedGroup.pauseReason))}
            </p>
          ) : null}
        </header>
        <GroupConversation
          workspaceId={workspaceId}
          userId={userId}
          group={selectedGroup}
          role={role}
          canPost={canChange}
          query={query}
        />
      </div>
    );
  } else if (selectedBot) {
    main = (
      <div className="p-5">
        <ChatPanel
          key={`${workspaceId}/${userId}/${selectedBot.id}`}
          workspaceId={workspaceId}
          userId={userId}
          botId={selectedBot.id}
          name={selectedBot.name}
          avatarSeed={selectedBot.avatarSeed}
          canChat={canChange}
          transcriptQuery={query}
        />
      </div>
    );
  } else {
    main = (
      <div className="flex flex-col gap-3 p-5">
        <h1 className="text-[20px] font-black">{t('groups')}</h1>
        <p className="text-[15px] text-ink-2">{t('emptyGroups')}</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-sm bg-terminal px-3 py-2">
        <label htmlFor="team-search" className="sr-only">
          {t('searchLabel')}
        </label>
        <input
          id="team-search"
          type="search"
          value={query}
          placeholder={t('searchPlaceholder')}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
          className="min-h-11 w-full rounded-sm bg-dark-2 px-3 text-[14px] text-paper placeholder:text-on-dark-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
        />
      </div>
      <div
        className={`grid items-start gap-3 ${selectedGroup || selectedBot ? 'lg:grid-cols-[220px_minmax(0,1fr)_250px]' : 'lg:grid-cols-[220px_minmax(0,1fr)]'}`}
      >
        <nav
          aria-label={t('conversations')}
          className="flex flex-col gap-3 rounded-md bg-dark-2 p-2.5 text-on-dark"
        >
          <div className="px-2.5">
            <p className="truncate text-[17px] font-black text-paper">
              {workspaceName}
            </p>
            <p className="text-[13px] text-on-dark-muted">
              {t('counts', { bots: botList.length, humans: humanList.length })}
            </p>
            <p className="mt-1 text-[12px] font-bold text-green-light">
              {roles(role)}
            </p>
          </div>
          <div className="flex flex-col gap-1">
            <Link
              href="/team/approvals"
              className="flex min-h-11 items-center gap-2 rounded-sm px-2.5 font-black text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
            >
              <span className="flex-1">{t('approvals')}</span>
              {approvals.data ? (
                <span className="rounded-pill bg-amber px-2 py-0.5 text-[12px] font-black text-terminal">
                  {t('approvalsCount', {
                    count: approvals.data.approvals.filter(
                      (row) => row.status === 'pending',
                    ).length,
                  })}
                </span>
              ) : null}
            </Link>
            <button
              type="button"
              aria-current={selection.type === 'activity' ? 'true' : undefined}
              onClick={() => setSelection({ type: 'activity' })}
              className={sideButton(selection.type === 'activity')}
            >
              {t('activity')}
            </button>
          </div>
          <section className="flex flex-col gap-1">
            <h2 className="px-2.5 text-[12px] font-bold uppercase tracking-[0.08em] text-on-dark-muted">
              {t('groups')}
            </h2>
            {visibleGroups.map((group) => {
              const active = group.id === openGroupId;
              return (
                <button
                  key={group.id}
                  type="button"
                  aria-current={active ? 'true' : undefined}
                  onClick={() => setSelection({ type: 'group', id: group.id })}
                  className={sideButton(active)}
                >
                  <span aria-hidden="true" className="w-4 text-center">
                    #
                  </span>
                  <span className="min-w-0 flex-1 truncate">{group.name}</span>
                  {group.unread > 0 ? (
                    <span className="rounded-pill bg-green px-2 py-0.5 text-[12px] font-black text-terminal">
                      {t('unread', { count: Number(group.unread) })}
                    </span>
                  ) : null}
                </button>
              );
            })}
            {canChange ? (
              <button
                type="button"
                onClick={() => openDialog()}
                className="flex min-h-11 w-full items-center rounded-sm px-2.5 text-left text-[15px] font-bold text-on-dark-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
              >
                {t('newGroup')}
              </button>
            ) : null}
          </section>
          <section className="flex flex-col gap-1">
            <h2 className="px-2.5 text-[12px] font-bold uppercase tracking-[0.08em] text-on-dark-muted">
              {t('colleagues')}
            </h2>
            {visibleBots.map((bot) => (
              <ColleagueButton
                key={bot.id}
                bot={bot}
                active={selection.type === 'dm' && selection.botId === bot.id}
                className={sideButton(
                  selection.type === 'dm' && selection.botId === bot.id,
                )}
                status={t(statusKey(bot.status))}
                onPick={() => setSelection({ type: 'dm', botId: bot.id })}
              />
            ))}
            {canChange ? (
              <Link
                href="/bots/new"
                className="flex min-h-11 items-center rounded-sm px-2.5 text-[15px] font-bold text-on-dark-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
              >
                {t('hire')}
              </Link>
            ) : null}
          </section>
          <section className="flex flex-col gap-1">
            <div className="flex items-center justify-between gap-2 px-2.5">
              <h2 className="text-[12px] font-bold uppercase tracking-[0.08em] text-on-dark-muted">
                {t('humans')}
              </h2>
            </div>
            {visibleHumans.map((human) => (
              <div
                key={human.userId}
                className="flex min-h-11 items-center gap-2 px-2.5"
              >
                <span className="grid h-7 w-7 shrink-0 place-items-center rounded-sm bg-hex-2 text-[11px] font-black text-paper">
                  {human.displayName.slice(0, 1).toUpperCase()}
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
                className="flex min-h-11 items-center rounded-sm px-2.5 text-[15px] font-bold text-on-dark-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
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
        <section className="min-w-0 overflow-hidden rounded-md border border-line bg-paper">
          {main}
        </section>
        {selectedGroup || selectedBot ? (
          <aside
            aria-label={t('details')}
            className="rounded-md bg-paper-2 p-4"
          >
            {selectedGroup ? (
              <GroupDetails
                group={selectedGroup}
                bots={botList}
                canEdit={canChange}
                error={saveError ? errors(saveError) : null}
                onPatch={(patch) => void patchGroup(selectedGroup, patch)}
              />
            ) : null}
            {selectedBot ? (
              <DirectDetails
                bot={selectedBot}
                groups={groupList}
                canCreate={canChange}
                onOpenGroup={(id) => setSelection({ type: 'group', id })}
                onNewGroup={() => openDialog(selectedBot.id)}
              />
            ) : null}
          </aside>
        ) : null}
      </div>
      {dialogOpen ? (
        <NewGroupDialog
          userId={userId}
          displayName={displayName}
          bots={botList}
          humans={humanList}
          initialBotId={presetBotId}
          creating={creating}
          error={createError ? errors(createError) : null}
          onClose={() => setDialogOpen(false)}
          onCreate={createGroup}
        />
      ) : null}
    </div>
  );
}

function ColleagueButton({
  bot,
  active,
  className,
  status,
  onPick,
}: {
  bot: Bot;
  active: boolean;
  className: string;
  status: string;
  onPick: () => void;
}) {
  return (
    <button
      type="button"
      aria-current={active ? 'true' : undefined}
      onClick={onPick}
      className={className}
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
        <span className="block truncate text-[12px] text-on-dark-muted">
          {status}
        </span>
      </span>
    </button>
  );
}
