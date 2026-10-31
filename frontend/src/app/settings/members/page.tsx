'use client';

import { Copy, Plus, Trash2 } from 'lucide-react';
import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from 'react';
import { useTranslations } from 'next-intl';

import { GitLabInstancesPanel } from '@/components/settings/GitLabInstancesPanel';
import { MCPServersPanel } from '@/components/settings/MCPServersPanel';
import { BudgetPanel } from '@/components/chat/BudgetPanel';
import { AppShell } from '@/components/shell/AppShell';
import { initials } from '@/components/team/text';
import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  RoleBadge,
  Select,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import {
  createInvitation,
  listInvitations,
  listMembers,
  removeMember,
  requestJson,
  revokeInvitation,
  updateMemberRole,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canManageMembers } from '@/lib/rbac';
import type {
  CreatedInvitationResponse,
  InvitationSummary,
  WorkspaceMember,
  WorkspaceRole,
} from '@/types/api';

const ALL_ROLES: WorkspaceRole[] = ['owner', 'admin', 'member', 'viewer'];

const RBAC_MATRIX_ROWS = [
  { key: 'wsDelete', own: 'yes', adm: 'no', mem: 'no', vwr: 'no' },
  { key: 'invite', own: 'yes', adm: 'yes', mem: 'no', vwr: 'no' },
  { key: 'budgets', own: 'yes', adm: 'yes', mem: 'view', vwr: 'no' },
  { key: 'wsSecrets', own: 'yes', adm: 'yes', mem: 'own', vwr: 'no' },
  { key: 'bots', own: 'yes', adm: 'yes', mem: 'yes', vwr: 'no' },
  { key: 'chat', own: 'yes', adm: 'yes', mem: 'yes', vwr: 'no' },
  { key: 'read', own: 'yes', adm: 'yes', mem: 'yes', vwr: 'yes' },
] as const;

function MembersContent() {
  const t = useTranslations('members');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');
  const tRoles = useTranslations('roles');
  const { user, activeWorkspace } = useAuth();

  const canManage = canManageMembers(activeWorkspace?.role);

  const [members, setMembers] = useState<WorkspaceMember[]>([]);
  const [invitations, setInvitations] = useState<InvitationSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  const [busyUserId, setBusyUserId] = useState<string | null>(null);
  const [busyInviteId, setBusyInviteId] = useState<string | null>(null);

  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState<WorkspaceRole>('member');
  const [inviting, setInviting] = useState(false);
  const [createdInvite, setCreatedInvite] =
    useState<CreatedInvitationResponse | null>(null);
  const [copied, setCopied] = useState(false);

  const [priceProvider, setPriceProvider] = useState('anthropic');
  const [priceModel, setPriceModel] = useState('');
  const [priceInputUsd, setPriceInputUsd] = useState('');
  const [priceOutputUsd, setPriceOutputUsd] = useState('');
  const [priceCacheUsd, setPriceCacheUsd] = useState('0');
  const [savingPrice, setSavingPrice] = useState(false);
  const [priceSaved, setPriceSaved] = useState(false);
  const [priceError, setPriceError] = useState(false);

  const loadData = useCallback(async () => {
    if (!activeWorkspace) {
      setMembers([]);
      setInvitations([]);
      setLoading(false);
      return;
    }

    setLoading(true);
    setErrorKey(null);
    try {
      const membersRes = await listMembers(activeWorkspace.id);
      setMembers(membersRes.members);

      if (canManage) {
        const invRes = await listInvitations(activeWorkspace.id);
        setInvitations(invRes.invitations);
      } else {
        setInvitations([]);
      }
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setLoading(false);
    }
  }, [activeWorkspace, canManage]);

  useEffect(() => {
    setCreatedInvite(null);
    setCopied(false);
    void loadData();
  }, [loadData]);

  const shareUrl = useMemo(() => {
    if (!createdInvite) {
      return '';
    }
    if (createdInvite.inviteUrl.startsWith('http')) {
      return createdInvite.inviteUrl;
    }
    if (typeof window !== 'undefined') {
      const prefix = createdInvite.inviteUrl.startsWith('/') ? '' : '/';
      return `${window.location.origin}${prefix}${createdInvite.inviteUrl}`;
    }
    return createdInvite.inviteUrl;
  }, [createdInvite]);

  async function handleRoleChange(userId: string, nextRole: WorkspaceRole) {
    if (!activeWorkspace || !canManage) {
      return;
    }
    setErrorKey(null);
    setBusyUserId(userId);
    try {
      const updated = await updateMemberRole(activeWorkspace.id, userId, {
        role: nextRole,
      });
      setMembers((prev) =>
        prev.map((member) => (member.userId === userId ? updated : member)),
      );
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusyUserId(null);
    }
  }

  async function handleRemoveMember(userId: string) {
    if (!activeWorkspace || !canManage) {
      return;
    }
    setErrorKey(null);
    setBusyUserId(userId);
    try {
      await removeMember(activeWorkspace.id, userId);
      setMembers((prev) => prev.filter((member) => member.userId !== userId));
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusyUserId(null);
    }
  }

  async function handleCreateInvitation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeWorkspace || !canManage) {
      return;
    }
    setErrorKey(null);
    setCopied(false);
    setInviting(true);
    try {
      const trimmedEmail = inviteEmail.trim();
      const created = await createInvitation(activeWorkspace.id, {
        email: trimmedEmail.length > 0 ? trimmedEmail : null,
        role: inviteRole,
      });
      setCreatedInvite(created);
      setInvitations((prev) => [created.invitation, ...prev]);
      setInviteEmail('');
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setInviting(false);
    }
  }

  async function handleRevokeInvitation(invitationId: string) {
    if (!activeWorkspace || !canManage) {
      return;
    }
    setErrorKey(null);
    setBusyInviteId(invitationId);
    try {
      await revokeInvitation(activeWorkspace.id, invitationId);
      setInvitations((prev) => prev.filter((inv) => inv.id !== invitationId));
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusyInviteId(null);
    }
  }

  async function handleCopyUrl() {
    if (!shareUrl) {
      return;
    }
    try {
      if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(shareUrl);
        setCopied(true);
      }
    } catch {
      // Clipboard write unavailable in restricted browser context; URL remains selectable in read-only input.
    }
  }

  async function handleSaveModelPrice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeWorkspace || !canManage || savingPrice) {
      return;
    }
    setSavingPrice(true);
    setPriceSaved(false);
    setPriceError(false);
    try {
      await requestJson(
        `/api/workspaces/${encodeURIComponent(activeWorkspace.id)}/model-prices`,
        {
          method: 'PUT',
          body: {
            provider: priceProvider.trim(),
            model: priceModel.trim(),
            inputMicrosPerMillion: Math.round(Number(priceInputUsd) * 1000000),
            outputMicrosPerMillion: Math.round(
              Number(priceOutputUsd) * 1000000,
            ),
            cacheMicrosPerMillion: Math.round(
              Number(priceCacheUsd || '0') * 1000000,
            ),
          },
        },
      );
      setPriceSaved(true);
    } catch {
      setPriceError(true);
    } finally {
      setSavingPrice(false);
    }
  }

  const pendingInvitations = invitations.filter((inv) => !inv.acceptedAt);

  function renderMatrixCell(cell: 'yes' | 'no' | 'view' | 'own') {
    if (cell === 'yes') {
      return <span className="font-bold text-green-600">{t('rbacYes')}</span>;
    }
    if (cell === 'view') {
      return <span className="font-bold text-blue">{t('rbacView')}</span>;
    }
    if (cell === 'own') {
      return <span className="font-bold text-blue">{t('rbacOwnOnly')}</span>;
    }
    return <span className="text-muted">{t('rbacNo')}</span>;
  }

  return (
    <div className="flex flex-col gap-6">
      {activeWorkspace && (
        <>
          <GitLabInstancesPanel
            key={activeWorkspace.id}
            workspaceId={activeWorkspace.id}
            role={activeWorkspace.role}
          />
          <MCPServersPanel
            key={`mcp-${activeWorkspace.id}`}
            workspaceId={activeWorkspace.id}
            role={activeWorkspace.role}
            ignoreMissing
          />
        </>
      )}
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

        {canManage ? (
          <a
            href="#invite-section"
            className="inline-flex min-h-[44px] items-center gap-2 rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper shadow-card transition duration-150 hover:-translate-y-px hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            <span>{t('inviteCta')}</span>
          </a>
        ) : null}
      </div>

      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      {!canManage ? <Alert tone="info">{t('inviteSubtitle')}</Alert> : null}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px] lg:items-start">
        <div className="flex min-w-0 flex-col gap-6">
          <Card className="flex flex-col gap-5 p-6">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-[20px] font-black text-ink">
                {t('rosterTitle')}
              </h2>
              <span className="rounded-pill border border-line bg-paper-2 px-3 py-0.5 text-[13px] font-bold text-muted">
                {t('rosterCount', { count: members.length })}
              </span>
            </div>

            {loading ? (
              <p
                role="status"
                className="py-4 text-[15px] font-bold text-ink-2"
              >
                {tActions('loading')}
              </p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-left">
                  <thead>
                    <tr className="border-b border-line bg-paper-2 text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                      <th className="px-3 py-3">{t('colMember')}</th>
                      <th className="px-3 py-3">{t('colEmail')}</th>
                      <th className="px-3 py-3">{t('colRole')}</th>
                      {canManage ? (
                        <th className="px-3 py-3 text-right">
                          {t('colActions')}
                        </th>
                      ) : null}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {members.map((member) => {
                      const isSelf = user?.id === member.userId;
                      const rowBusy = busyUserId === member.userId;
                      return (
                        <tr key={member.userId}>
                          <td className="px-3 py-3 text-[15px] font-bold text-ink">
                            <div className="flex items-center gap-2.5">
                              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-dark text-[12px] font-black text-paper">
                                {initials(member.displayName)}
                              </span>
                              <span>{member.displayName}</span>
                              {isSelf ? (
                                <span className="rounded-pill border border-mint-line bg-mint-soft px-2 py-0.5 text-[11px] font-bold text-green-600">
                                  {t('youBadge')}
                                </span>
                              ) : null}
                            </div>
                          </td>
                          <td className="px-3 py-3 font-mono text-[13px] text-ink-2">
                            {member.email}
                          </td>
                          <td className="px-3 py-3">
                            {canManage ? (
                              <Select
                                hideLabel
                                label={t('roleSelectAria', {
                                  name: member.displayName,
                                })}
                                value={member.role}
                                disabled={rowBusy}
                                onChange={(event) =>
                                  void handleRoleChange(
                                    member.userId,
                                    event.target.value as WorkspaceRole,
                                  )
                                }
                                className="max-w-[160px]"
                              >
                                {ALL_ROLES.map((role) => (
                                  <option key={role} value={role}>
                                    {tRoles(role)}
                                  </option>
                                ))}
                              </Select>
                            ) : (
                              <RoleBadge role={member.role} />
                            )}
                          </td>
                          {canManage ? (
                            <td className="px-3 py-3 text-right">
                              <Button
                                variant="secondary"
                                type="button"
                                disabled={rowBusy}
                                onClick={() =>
                                  void handleRemoveMember(member.userId)
                                }
                              >
                                {rowBusy
                                  ? t('removingMember')
                                  : t('removeMember')}
                              </Button>
                            </td>
                          ) : null}
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}

            {canManage ? (
              <div className="flex flex-col gap-3 border-t border-line pt-4">
                <h3 className="text-[14px] font-bold text-ink">
                  {t('pendingTitle', { count: pendingInvitations.length })}
                </h3>
                {pendingInvitations.length === 0 ? (
                  <p className="text-[14px] text-muted">{t('noPending')}</p>
                ) : (
                  <ul className="flex flex-col gap-2.5">
                    {pendingInvitations.map((inv) => {
                      const isBusy = busyInviteId === inv.id;
                      const expiryText = `${t('expiresPrefix')} ${new Date(inv.expiresAt).toLocaleDateString()}`;
                      return (
                        <li
                          key={inv.id}
                          className="flex flex-wrap items-center justify-between gap-3 rounded-sm border border-dashed border-amber-line bg-amber-tint px-4 py-3"
                        >
                          <div className="flex min-w-0 flex-wrap items-center gap-2.5">
                            <span className="truncate font-mono text-[13px] font-bold text-ink">
                              {inv.email ?? t('openInviteLabel')}
                            </span>
                            <RoleBadge role={inv.role} />
                            <span className="text-[12px] text-amber">
                              {expiryText}
                            </span>
                          </div>

                          <Button
                            variant="secondary"
                            type="button"
                            disabled={isBusy}
                            onClick={() => void handleRevokeInvitation(inv.id)}
                            className="min-h-[40px] border-amber-line text-amber"
                          >
                            <Trash2 className="h-4 w-4" aria-hidden="true" />
                            <span>{isBusy ? t('revoking') : t('revoke')}</span>
                          </Button>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            ) : null}
          </Card>

          {canManage ? (
            <Card id="invite-section" className="flex flex-col gap-4 p-6">
              <h2 className="text-[20px] font-black text-ink">
                {t('inviteTitle')}
              </h2>

              <form
                onSubmit={(event) => void handleCreateInvitation(event)}
                className="grid items-end gap-4 sm:grid-cols-[1fr_180px_auto]"
              >
                <Input
                  label={t('inviteEmailLabel')}
                  placeholder={t('inviteEmailPlaceholder')}
                  helperText={t('inviteEmailHelper')}
                  type="email"
                  value={inviteEmail}
                  onChange={(event) => setInviteEmail(event.target.value)}
                />

                <div className="sm:pb-6">
                  <Select
                    label={t('inviteRoleLabel')}
                    value={inviteRole}
                    onChange={(event) =>
                      setInviteRole(event.target.value as WorkspaceRole)
                    }
                  >
                    {ALL_ROLES.map((role) => (
                      <option key={role} value={role}>
                        {tRoles(role)}
                      </option>
                    ))}
                  </Select>
                </div>

                <div className="sm:pb-6">
                  <Button type="submit" arrow disabled={inviting}>
                    {inviting ? t('inviting') : t('inviteSubmit')}
                  </Button>
                </div>
              </form>

              {createdInvite ? (
                <Alert tone="success">
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                    <div className="flex-1">
                      <Input
                        label={t('generatedTitle')}
                        helperText={t('generatedHint')}
                        value={shareUrl}
                        readOnly
                        mono
                      />
                    </div>
                    <Button
                      variant="secondary"
                      type="button"
                      onClick={() => void handleCopyUrl()}
                    >
                      <Copy className="h-4 w-4" aria-hidden="true" />
                      <span>{copied ? t('copiedUrl') : t('copyUrl')}</span>
                    </Button>
                  </div>
                </Alert>
              ) : null}
            </Card>
          ) : null}

          {activeWorkspace ? (
            <section id="budgets" className="flex flex-col gap-4">
              <BudgetPanel
                workspaceId={activeWorkspace.id}
                canManage={canManage}
                ignoreMissing
              />
            </section>
          ) : null}

          {canManage ? (
            <Card className="flex flex-col gap-4 p-6">
              <div>
                <h2 className="text-[20px] font-black text-ink">
                  {t('modelPricesTitle')}
                </h2>
                <p className="mt-1 text-[14px] text-ink-2">
                  {t('modelPricesLead')}
                </p>
              </div>
              {priceSaved ? (
                <Alert tone="success">{t('modelPriceSaved')}</Alert>
              ) : null}
              {priceError ? (
                <Alert tone="warning">{t('modelPriceError')}</Alert>
              ) : null}
              <form
                onSubmit={(event) => void handleSaveModelPrice(event)}
                className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5 lg:items-end"
              >
                <Input
                  label={t('modelPriceProvider')}
                  mono
                  value={priceProvider}
                  required
                  onChange={(event) => setPriceProvider(event.target.value)}
                />
                <Input
                  label={t('modelPriceModel')}
                  mono
                  placeholder="claude-sonnet-4"
                  value={priceModel}
                  required
                  onChange={(event) => setPriceModel(event.target.value)}
                />
                <Input
                  label={t('modelPriceInput')}
                  type="number"
                  min="0"
                  step="0.01"
                  mono
                  value={priceInputUsd}
                  required
                  onChange={(event) => setPriceInputUsd(event.target.value)}
                />
                <Input
                  label={t('modelPriceOutput')}
                  type="number"
                  min="0"
                  step="0.01"
                  mono
                  value={priceOutputUsd}
                  required
                  onChange={(event) => setPriceOutputUsd(event.target.value)}
                />
                <Input
                  label={t('modelPriceCache')}
                  type="number"
                  min="0"
                  step="0.01"
                  mono
                  value={priceCacheUsd}
                  required
                  onChange={(event) => setPriceCacheUsd(event.target.value)}
                />
                <div className="sm:col-span-2 lg:col-span-5 lg:flex lg:justify-end">
                  <Button
                    type="submit"
                    variant="secondary"
                    disabled={savingPrice}
                  >
                    {savingPrice ? t('modelPriceSaving') : t('modelPriceSave')}
                  </Button>
                </div>
              </form>
            </Card>
          ) : null}
        </div>

        <aside className="flex flex-col gap-4">
          <Card className="flex flex-col gap-3 p-5">
            <Eyebrow>{t('rbacEyebrow')}</Eyebrow>
            <h2 className="text-[20px] font-black text-ink">
              {t('rbacTitle')}
            </h2>
            <p className="text-[13px] leading-[1.5] text-muted">
              {t('rbacLead')}
            </p>
            <div className="mt-1 overflow-x-auto">
              <div className="min-w-[260px] text-[12px]">
                <div className="grid grid-cols-[minmax(0,1fr)_34px_34px_34px_34px] gap-1 border-b border-line bg-paper-2 px-2 py-2 font-bold uppercase tracking-[0.04em] text-muted">
                  <span>{t('rbacCapCol')}</span>
                  <span className="text-center">{t('rbacOwnCol')}</span>
                  <span className="text-center">{t('rbacAdmCol')}</span>
                  <span className="text-center">{t('rbacMemCol')}</span>
                  <span className="text-center">{t('rbacVwrCol')}</span>
                </div>
                <div className="divide-y divide-line">
                  {RBAC_MATRIX_ROWS.map((row) => (
                    <div
                      key={row.key}
                      className="grid grid-cols-[minmax(0,1fr)_34px_34px_34px_34px] items-center gap-1 px-2 py-2"
                    >
                      <span className="font-bold text-ink">
                        {t(`rbacRows.${row.key}`)}
                      </span>
                      <span className="text-center">
                        {renderMatrixCell(row.own)}
                      </span>
                      <span className="text-center">
                        {renderMatrixCell(row.adm)}
                      </span>
                      <span className="text-center">
                        {renderMatrixCell(row.mem)}
                      </span>
                      <span className="text-center">
                        {renderMatrixCell(row.vwr)}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          </Card>

          <div className="rounded-lg bg-terminal p-5 text-on-dark shadow-card">
            <h3 className="text-[15px] font-black text-paper">
              {t('isolationTitle')}
            </h3>
            <p className="mt-1.5 text-[13px] leading-[1.55] text-on-dark-muted">
              {t('isolationBody')}
            </p>
          </div>
        </aside>
      </div>
    </div>
  );
}

/**
 * Workspace members, invitations RBAC management, and workspace budgets page.
 */
export default function MembersSettingsPage() {
  return (
    <AppShell>
      <MembersContent />
    </AppShell>
  );
}
