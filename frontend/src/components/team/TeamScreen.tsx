// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { ArrowLeftRight, Info, Monitor, Terminal, X } from 'lucide-react';
import Link from 'next/link';
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { ToolCallsSidebar } from '@/components/chat/ToolCallsSidebar';
import type { ToolTraceSummary } from '@/components/chat/toolTrace';
import { useTeamShell } from '@/components/shell/AppShell';
import { GroupConversation } from '@/components/team/GroupConversation';
import { useApprovalList } from '@/components/team/useApprovals';
import { DirectDetails, GroupDetails } from '@/components/team/GroupDetails';
import { NewGroupDialog } from '@/components/team/NewGroupDialog';
import {
  TeamSidebar,
  type TeamSidebarSelection,
} from '@/components/team/TeamSidebar';
import { groupSettingsBody, initials } from '@/components/team/text';
import { Alert, BotAvatar, Button, ModalDialog } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import { resolveErrorKey, type ErrorTranslationKey } from '@/lib/errors';
import { canManageBot } from '@/lib/rbac';
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
type Selection = Exclude<TeamSidebarSelection, { type: 'approvals' }>;

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
):
  | 'pauseTurnLimit'
  | 'pauseLoop'
  | 'pauseApproval'
  | 'pauseBudget'
  | 'pauseSecretRequired'
  | 'paused' {
  if (reason === 'turn_limit') return 'pauseTurnLimit';
  if (reason === 'loop') return 'pauseLoop';
  if (reason === 'approval') return 'pauseApproval';
  if (reason === 'budget') return 'pauseBudget';
  if (reason === 'secret_required') return 'pauseSecretRequired';
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
 * Full-bleed Team chat: dark-2 sidebar, Slack-style group/DM transcript,
 * activity stream, and collapsible paper-2 details pane.
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
  const tChat = useTranslations('chat');
  const actions = useTranslations('actions');
  const errors = useTranslations('errors');
  const client = useQueryClient();
  const shell = useTeamShell();

  const canChange = role !== 'viewer';
  const [selection, setSelection] = useState<Selection>({ type: 'none' });
  const [localQuery, setLocalQuery] = useState('');
  const [localDrawerOpen, setLocalDrawerOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(true);
  const [asideTab, setAsideTab] = useState<'details' | 'tools'>('details');
  const [mobileToolOpen, setMobileToolOpen] = useState(false);
  const [toolTrace, setToolTrace] = useState<ToolTraceSummary | null>(null);
  const [selectedToolTurnKey, setSelectedToolTurnKey] = useState<string | null>(
    null,
  );
  const [scrollRequest, setScrollRequest] = useState<{
    messageId: string;
    nonce: number;
  } | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [presetBotId, setPresetBotId] = useState<string | undefined>(undefined);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [saveError, setSaveError] = useState<ErrorTranslationKey | null>(null);
  const [botSaveError, setBotSaveError] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [deleteTarget, setDeleteTarget] = useState<Group | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<ErrorTranslationKey | null>(
    null,
  );

  const query = shell.hasShellHeader ? shell.searchQuery : localQuery;
  const setQuery = shell.hasShellHeader ? shell.setSearchQuery : setLocalQuery;
  const drawerOpen = shell.hasShellHeader
    ? shell.mobileDrawerOpen
    : localDrawerOpen;
  const setDrawerOpen = shell.hasShellHeader
    ? shell.setMobileDrawerOpen
    : setLocalDrawerOpen;

  const base = `/api/workspaces/${encodeURIComponent(workspaceId)}`;
  const groups = useQuery({
    queryKey: ['groups', workspaceId],
    queryFn: () => requestJson<Group[]>(`${base}/groups`),
    refetchInterval: 10000,
  });
  const bots = useQuery({
    queryKey: ['bots', workspaceId],
    queryFn: () => requestJson<BotListResponse>(`${base}/bots`),
    refetchInterval: 3000,
    refetchOnMount: 'always',
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
  const groupList = useMemo(() => groups.data ?? [], [groups.data]);
  const botList = useMemo(() => bots.data?.bots ?? [], [bots.data?.bots]);
  const humanList = members.data?.members ?? [];

  useEffect(() => {
    if (
      selection.type === 'group' &&
      groups.data &&
      !groupList.some((group) => group.id === selection.id)
    ) {
      setSelection({ type: 'none' });
      setToolTrace(null);
      setSelectedToolTurnKey(null);
      setMobileToolOpen(false);
      return;
    }
    if (selection.type !== 'none') return;
    if (typeof window !== 'undefined') {
      const params = new URLSearchParams(window.location.search);
      const dm = params.get('dm');
      if (dm && botList.some((b) => b.id === dm)) {
        setSelection({ type: 'dm', botId: dm });
        return;
      }
      const groupParam = params.get('group');
      if (groupParam && groupList.some((g) => g.id === groupParam)) {
        setSelection({ type: 'group', id: groupParam });
        return;
      }
    }
    const first = groups.data?.[0];
    if (!first) return;
    setSelection({ type: 'group', id: first.id });
  }, [groups.data, groupList, botList, selection]);

  useEffect(() => {
    if (!drawerOpen && !mobileToolOpen) return;
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setDrawerOpen(false);
        setMobileToolOpen(false);
      }
    }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [drawerOpen, mobileToolOpen, setDrawerOpen]);

  const handleSelectGroup = useCallback((id: string) => {
    setSelection({ type: 'group', id });
    setToolTrace(null);
    setSelectedToolTurnKey(null);
    setMobileToolOpen(false);
  }, []);

  const handleSelectDm = useCallback((botId: string) => {
    setSelection({ type: 'dm', botId });
    setToolTrace(null);
    setSelectedToolTurnKey(null);
    setMobileToolOpen(false);
  }, []);

  const handleInspectToolTurn = useCallback((turnKey: string) => {
    setSelectedToolTurnKey(turnKey);
    setAsideTab('tools');
    setDetailsOpen(true);
    if (
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      !window.matchMedia('(min-width: 1280px)').matches
    ) {
      setMobileToolOpen(true);
    }
  }, []);

  function toggleToolCallsPanel() {
    if (
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      !window.matchMedia('(min-width: 1280px)').matches
    ) {
      setMobileToolOpen((prev) => !prev);
      return;
    }
    if (detailsOpen && asideTab === 'tools') {
      setDetailsOpen(false);
      return;
    }
    setAsideTab('tools');
    setDetailsOpen(true);
  }

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

  async function patchBotAskBefore(
    bot: Bot,
    askBefore: components['schemas']['AskBeforeRule'][],
  ) {
    setBotSaveError(null);
    try {
      const updated = await requestJson<Bot>(
        `${base}/bots/${encodeURIComponent(bot.id)}`,
        { method: 'PATCH', body: { askBefore } },
      );
      client.setQueryData<BotListResponse>(['bots', workspaceId], (current) =>
        current
          ? {
              ...current,
              bots: current.bots.map((row) =>
                row.id === updated.id ? updated : row,
              ),
            }
          : current,
      );
      client.setQueryData<Bot>(['bot', workspaceId, bot.id], updated);
    } catch (err) {
      setBotSaveError(resolveErrorKey(err));
    }
  }

  async function deleteGroup() {
    if (!deleteTarget || deleting) return;
    const id = deleteTarget.id;
    setDeleting(true);
    setDeleteError(null);
    try {
      await requestJson<void>(`${base}/groups/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      });
      // Stop an older list response from putting the deleted group back.
      await client.cancelQueries({ queryKey: ['groups', workspaceId] });
      client.setQueryData<Group[]>(['groups', workspaceId], (current) =>
        (current ?? []).filter((group) => group.id !== id),
      );
      client.removeQueries({ queryKey: ['group-messages', workspaceId, id] });
      setSelection((current) =>
        current.type === 'group' && current.id === id
          ? { type: 'none' }
          : current,
      );
      setToolTrace(null);
      setSelectedToolTurnKey(null);
      setMobileToolOpen(false);
      setDeleteTarget(null);
      void client.invalidateQueries({ queryKey: ['approvals', workspaceId] });
      void client.invalidateQueries({
        queryKey: ['notifications', workspaceId],
      });
    } catch (err) {
      setDeleteError(resolveErrorKey(err));
    } finally {
      setDeleting(false);
    }
  }

  let main: ReactNode;
  if (groups.isPending) {
    main = <p className="p-6 text-[15px] text-ink-2">{actions('loading')}</p>;
  } else if (groups.isError && !groups.data) {
    // Later refetch errors keep the cached list, so an open draft stays put.
    main = (
      <div className="p-6">
        <Alert tone="warning">{t('loadFailed')}</Alert>
      </div>
    );
  } else if (selection.type === 'activity') {
    const notes = notifications.data?.notifications ?? [];
    main = (
      <div className="flex flex-1 flex-col gap-4 overflow-y-auto p-6">
        <h1 className="text-[22px] font-black text-ink">
          {t('activityTitle')}
        </h1>
        {notes.length === 0 ? (
          <p className="text-[15px] text-ink-2">{t('activityEmpty')}</p>
        ) : (
          <ul className="flex flex-col gap-2.5">
            {notes.map((note) => {
              const group = groupList.find(
                (row) => row.id === note.conversationId,
              );
              return (
                <li
                  key={note.id}
                  className="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-line bg-paper-2 px-4 py-2.5"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="rounded-pill border border-blue-line bg-blue-tint px-2.5 py-0.5 text-[12px] font-bold text-blue">
                      {t(kindKey(note.kind))}
                    </span>
                    {group ? (
                      <span className="font-bold text-ink">{`#${group.name}`}</span>
                    ) : null}
                  </div>
                  {group ? (
                    <button
                      type="button"
                      onClick={() =>
                        setSelection({ type: 'group', id: group.id })
                      }
                      className="inline-flex min-h-[44px] items-center rounded-sm px-2.5 font-bold text-green-600 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
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
      <div className="flex min-h-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-line bg-paper px-5 py-3">
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-[19px] font-black leading-[1.2] text-ink">
              <span aria-hidden="true" className="font-bold text-muted">
                #{' '}
              </span>
              {selectedGroup.name}
            </h1>
            <p className="truncate text-[13px] text-muted">
              {selectedGroup.goal.trim() || t('noGoal')}
              {' · '}
              {t('memberSummary', { count: selectedGroup.members.length })}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {canChange ? (
              <Button
                variant="secondary"
                onClick={() => {
                  setDeleteError(null);
                  setDeleteTarget(selectedGroup);
                }}
              >
                {t('deleteGroup')}
              </Button>
            ) : null}
            <div className="hidden items-center -space-x-1.5 sm:flex">
              {selectedGroup.members.slice(0, 4).map((member) => {
                if (member.type === 'bot') {
                  const matchedBot = botList.find((b) => b.id === member.id);
                  return (
                    <span
                      key={member.id}
                      className="rounded-xs ring-2 ring-paper"
                    >
                      <BotAvatar
                        seed={matchedBot?.avatarSeed ?? member.id}
                        size={26}
                        decorative
                      />
                    </span>
                  );
                }
                return (
                  <span
                    key={member.id}
                    className="grid h-[26px] w-[26px] place-items-center rounded-xs bg-dark text-[10px] font-black text-paper ring-2 ring-paper"
                  >
                    {initials(member.name || t('human'))}
                  </span>
                );
              })}
            </div>
            <span className="inline-flex items-center gap-1.5 rounded-pill border border-mint-line bg-mint-soft px-3 py-1 text-[12px] font-bold text-green-600">
              <ArrowLeftRight className="h-3.5 w-3.5" aria-hidden="true" />
              <span>
                {selectedGroup.botTalk ? t('botTalkOn') : t('botTalkOff')}
              </span>
            </span>
            <button
              type="button"
              aria-pressed={
                (detailsOpen && asideTab === 'tools') || mobileToolOpen
              }
              onClick={toggleToolCallsPanel}
              className={`inline-flex min-h-[40px] items-center gap-1.5 rounded-sm border px-3 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                (detailsOpen && asideTab === 'tools') || mobileToolOpen
                  ? 'border-dark bg-dark text-paper'
                  : 'border-line bg-paper text-ink-2 hover:bg-mint-soft'
              }`}
            >
              <Terminal className="h-4 w-4" aria-hidden="true" />
              <span>
                {toolTrace && toolTrace.totalSteps > 0
                  ? tChat('toolCallsTabCount', { count: toolTrace.totalSteps })
                  : tChat('toolCallsTab')}
              </span>
            </button>
            <button
              type="button"
              aria-label={t('toggleDetails')}
              aria-pressed={detailsOpen}
              onClick={() => {
                if (detailsOpen && asideTab === 'tools') {
                  setAsideTab('details');
                  return;
                }
                setAsideTab('details');
                setDetailsOpen((prev) => !prev);
              }}
              className={`grid h-10 w-10 place-items-center rounded-sm border transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                detailsOpen && asideTab === 'details'
                  ? 'border-dark bg-dark text-paper'
                  : 'border-line bg-paper text-ink-2 hover:bg-mint-soft'
              }`}
            >
              <Info className="h-4 w-4" aria-hidden="true" />
            </button>
          </div>
        </header>
        {selectedGroup.paused ? (
          <div className="mx-5 mt-3 flex items-center gap-2 rounded-sm border border-mint-line bg-mint-soft px-3.5 py-2 text-[13px] font-bold text-dark">
            <ArrowLeftRight
              className="h-4 w-4 shrink-0 text-green-600"
              aria-hidden="true"
            />
            <span>{t(pauseKey(selectedGroup.pauseReason))}</span>
          </div>
        ) : null}
        <GroupConversation
          workspaceId={workspaceId}
          userId={userId}
          group={selectedGroup}
          role={role}
          canPost={canChange}
          query={query}
          selectedToolTurnKey={selectedToolTurnKey}
          onInspectToolTurn={handleInspectToolTurn}
          onToolTraceChange={setToolTrace}
          scrollRequest={scrollRequest}
        />
      </div>
    );
  } else if (selectedBot) {
    main = (
      <div className="flex min-h-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-line bg-paper px-5 py-3">
          <div className="flex min-w-0 items-center gap-3">
            <BotAvatar
              seed={selectedBot.avatarSeed}
              mood={moodFor(selectedBot.status)}
              size={36}
              showStatusDot
              decorative
            />
            <div className="min-w-0">
              <h1 className="truncate text-[19px] font-black leading-[1.2] text-ink">
                {selectedBot.name}
              </h1>
              <p className="truncate text-[13px] text-muted">
                {t(statusKey(selectedBot.status))}
              </p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Link
              href={`/bots/${selectedBot.id}`}
              className="inline-flex min-h-[40px] items-center gap-2 rounded-sm border-[1.5px] border-line bg-paper px-3.5 text-[13px] font-bold text-ink transition duration-150 hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <Monitor className="h-4 w-4 text-green-600" aria-hidden="true" />
              <span>{t('openComputer')}</span>
            </Link>
            <button
              type="button"
              aria-pressed={
                (detailsOpen && asideTab === 'tools') || mobileToolOpen
              }
              onClick={toggleToolCallsPanel}
              className={`inline-flex min-h-[40px] items-center gap-1.5 rounded-sm border px-3 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                (detailsOpen && asideTab === 'tools') || mobileToolOpen
                  ? 'border-dark bg-dark text-paper'
                  : 'border-line bg-paper text-ink-2 hover:bg-mint-soft'
              }`}
            >
              <Terminal className="h-4 w-4" aria-hidden="true" />
              <span>
                {toolTrace && toolTrace.totalSteps > 0
                  ? tChat('toolCallsTabCount', { count: toolTrace.totalSteps })
                  : tChat('toolCallsTab')}
              </span>
            </button>
            <button
              type="button"
              aria-label={t('toggleDetails')}
              aria-pressed={detailsOpen}
              onClick={() => {
                if (detailsOpen && asideTab === 'tools') {
                  setAsideTab('details');
                  return;
                }
                setAsideTab('details');
                setDetailsOpen((prev) => !prev);
              }}
              className={`grid h-10 w-10 place-items-center rounded-sm border transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                detailsOpen && asideTab === 'details'
                  ? 'border-dark bg-dark text-paper'
                  : 'border-line bg-paper text-ink-2 hover:bg-mint-soft'
              }`}
            >
              <Info className="h-4 w-4" aria-hidden="true" />
            </button>
          </div>
        </header>
        <div className="flex min-h-0 flex-1 flex-col p-5">
          <ChatPanel
            key={`${workspaceId}/${userId}/${selectedBot.id}`}
            workspaceId={workspaceId}
            userId={userId}
            botId={selectedBot.id}
            name={selectedBot.name}
            avatarSeed={selectedBot.avatarSeed}
            canChat={canChange}
            role={role}
            transcriptQuery={query}
            variant="slack"
            hideHeader
            selectedToolTurnKey={selectedToolTurnKey}
            onInspectToolTurn={handleInspectToolTurn}
            onToolTraceChange={setToolTrace}
            scrollRequest={scrollRequest}
          />
        </div>
      </div>
    );
  } else {
    main = (
      <div className="flex flex-1 flex-col gap-3 p-6">
        <h1 className="text-[20px] font-black text-ink">{t('groups')}</h1>
        <p className="text-[15px] text-ink-2">{t('emptyGroups')}</p>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {deleteTarget ? (
        <ModalDialog
          open
          title={t('deleteGroupTitle', { name: deleteTarget.name })}
          closeLabel={actions('close')}
          onClose={() => {
            if (!deleting) setDeleteTarget(null);
          }}
        >
          <p>{t('deleteGroupWarning')}</p>
          {deleteError ? (
            <Alert tone="warning">{errors(deleteError)}</Alert>
          ) : null}
          <div className="flex flex-wrap justify-end gap-3">
            <Button
              variant="secondary"
              disabled={deleting}
              autoFocus
              onClick={() => setDeleteTarget(null)}
            >
              {actions('cancel')}
            </Button>
            <Button disabled={deleting} onClick={() => void deleteGroup()}>
              {deleting ? t('deletingGroup') : t('deleteGroup')}
            </Button>
          </div>
        </ModalDialog>
      ) : null}
      {!shell.hasShellHeader ? (
        <div className="bg-terminal px-3 py-2">
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
      ) : null}

      <div className="flex min-h-0 flex-1 overflow-hidden">
        <TeamSidebar
          workspaceName={workspaceName}
          role={role}
          groups={visibleGroups}
          bots={visibleBots}
          humans={visibleHumans}
          totalBots={botList.length}
          totalHumans={humanList.length}
          pendingApprovalsCount={
            approvals.data
              ? approvals.data.approvals.filter(
                  (row) => row.status === 'pending',
                ).length
              : undefined
          }
          selection={selection}
          canChange={canChange}
          sidebarEmpty={sidebarEmpty}
          mobileOpen={drawerOpen}
          onCloseMobile={() => setDrawerOpen(false)}
          onSelectGroup={handleSelectGroup}
          onSelectDm={handleSelectDm}
          onSelectActivity={() => {
            setSelection({ type: 'activity' });
            setToolTrace(null);
            setSelectedToolTurnKey(null);
            setMobileToolOpen(false);
          }}
          onNewGroup={() => openDialog()}
        />

        <section className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-paper">
          {main}
        </section>

        {detailsOpen && (selectedGroup || selectedBot) ? (
          <aside
            aria-label={
              asideTab === 'tools' ? tChat('toolSidebarLabel') : t('details')
            }
            className="hidden w-[320px] shrink-0 overflow-y-auto border-l border-line bg-paper-2 p-4 xl:flex xl:flex-col xl:gap-4"
          >
            <div
              role="group"
              aria-label={t('details')}
              className="grid grid-cols-2 gap-1 rounded-md border border-line bg-paper p-1"
            >
              <button
                type="button"
                aria-pressed={asideTab === 'details'}
                onClick={() => setAsideTab('details')}
                className={`min-h-[36px] rounded-sm px-2.5 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                  asideTab === 'details'
                    ? 'bg-dark text-paper'
                    : 'text-ink-2 hover:bg-paper-2 hover:text-ink'
                }`}
              >
                {tChat('detailsTab')}
              </button>
              <button
                type="button"
                aria-pressed={asideTab === 'tools'}
                onClick={() => setAsideTab('tools')}
                className={`min-h-[36px] rounded-sm px-2.5 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                  asideTab === 'tools'
                    ? 'bg-dark text-paper'
                    : 'text-ink-2 hover:bg-paper-2 hover:text-ink'
                }`}
              >
                {toolTrace && toolTrace.totalSteps > 0
                  ? tChat('toolCallsTabCount', { count: toolTrace.totalSteps })
                  : tChat('toolCallsTab')}
              </button>
            </div>

            {asideTab === 'tools' ? (
              <ToolCallsSidebar
                trace={
                  toolTrace ?? {
                    chatMessages: [],
                    turns: [],
                    totalSteps: 0,
                    runningSteps: 0,
                    errorSteps: 0,
                    turnsByReplyId: new Map(),
                    unattachedTurnsByAnchor: new Map(),
                    leadingUnattachedTurns: [],
                  }
                }
                selectedTurnKey={selectedToolTurnKey}
                resolveBot={(botId) => {
                  if (!botId) return null;
                  const bot = botList.find((b) => b.id === botId);
                  if (bot)
                    return { name: bot.name, avatarSeed: bot.avatarSeed };
                  const member = selectedGroup?.members.find(
                    (m) => m.id === botId,
                  );
                  if (member?.name) {
                    return { name: member.name, avatarSeed: member.id };
                  }
                  return null;
                }}
                onJumpToMessage={(messageId) =>
                  setScrollRequest((prev) => ({
                    messageId,
                    nonce: (prev?.nonce ?? 0) + 1,
                  }))
                }
              />
            ) : (
              <>
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
                    canEditAskBefore={canManageBot(role, userId, selectedBot)}
                    askBeforeError={botSaveError ? errors(botSaveError) : null}
                    onUpdateAskBefore={(next) =>
                      void patchBotAskBefore(selectedBot, next)
                    }
                    onOpenGroup={handleSelectGroup}
                    onNewGroup={() => openDialog(selectedBot.id)}
                  />
                ) : null}
              </>
            )}
          </aside>
        ) : null}
      </div>

      {mobileToolOpen && (selectedGroup || selectedBot) ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label={tChat('toolSidebarLabel')}
          className="fixed inset-0 z-50 flex justify-end bg-overlay xl:hidden"
          onClick={() => setMobileToolOpen(false)}
        >
          <div
            className="flex h-full w-full max-w-[360px] flex-col border-l border-line bg-paper-2 p-4 shadow-card-hover"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="mb-3 flex items-center justify-between gap-2 border-b border-line pb-2">
              <h2 className="text-[16px] font-black text-ink">
                {tChat('toolCallsTab')}
              </h2>
              <button
                type="button"
                aria-label={tChat('toolCloseDrawer')}
                onClick={() => setMobileToolOpen(false)}
                className="grid h-10 w-10 place-items-center rounded-sm border border-line bg-paper text-ink-2 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              >
                <X className="h-4 w-4" aria-hidden="true" />
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto">
              <ToolCallsSidebar
                trace={
                  toolTrace ?? {
                    chatMessages: [],
                    turns: [],
                    totalSteps: 0,
                    runningSteps: 0,
                    errorSteps: 0,
                    turnsByReplyId: new Map(),
                    unattachedTurnsByAnchor: new Map(),
                    leadingUnattachedTurns: [],
                  }
                }
                selectedTurnKey={selectedToolTurnKey}
                resolveBot={(botId) => {
                  if (!botId) return null;
                  const bot = botList.find((b) => b.id === botId);
                  if (bot)
                    return { name: bot.name, avatarSeed: bot.avatarSeed };
                  return null;
                }}
                onJumpToMessage={(messageId) => {
                  setMobileToolOpen(false);
                  setScrollRequest((prev) => ({
                    messageId,
                    nonce: (prev?.nonce ?? 0) + 1,
                  }));
                }}
              />
            </div>
          </div>
        </div>
      ) : null}

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
