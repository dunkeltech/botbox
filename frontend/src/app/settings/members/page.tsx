// SPDX-License-Identifier: AGPL-3.0-only

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

import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import { initials } from '@/components/team/text';
import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  ModalDialog,
  RoleBadge,
  Select,
  Textarea,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import {
  createInvitation,
  listInvitations,
  listMembers,
  removeMember,
  revokeInvitation,
  updateMemberRole,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canEditMemberRoleDescription, canManageMembers } from '@/lib/rbac';
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

  const [editingMember, setEditingMember] = useState<WorkspaceMember | null>(
    null,
  );
  const [roleDraft, setRoleDraft] = useState('');
  const [savingRoleDesc, setSavingRoleDesc] = useState(false);
  const [modalErrorKey, setModalErrorKey] =
    useState<ErrorTranslationKey | null>(null);
  const [modalValidationError, setModalValidationError] = useState<
    string | null
  >(null);

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

  function openRoleModal(member: WorkspaceMember) {
    setEditingMember(member);
    setRoleDraft(member.roleDescription ?? '');
    setModalErrorKey(null);
    setModalValidationError(null);
  }

  function closeRoleModal() {
    if (savingRoleDesc) {
      return;
    }
    setEditingMember(null);
    setModalErrorKey(null);
    setModalValidationError(null);
  }

  async function saveRoleDescription(nextDescription: string) {
    if (!activeWorkspace || !editingMember || savingRoleDesc) {
      return;
    }
    const trimmed = nextDescription.trim();
    if ([...trimmed].length > 4000) {
      setModalValidationError(t('roleModalTooLong'));
      return;
    }
    setModalErrorKey(null);
    setModalValidationError(null);
    setSavingRoleDesc(true);
    try {
      const updated = await updateMemberRole(
        activeWorkspace.id,
        editingMember.userId,
        {
          roleDescription: trimmed,
        },
      );
      setMembers((prev) =>
        prev.map((member) =>
          member.userId === editingMember.userId ? updated : member,
        ),
      );
      setEditingMember(null);
    } catch (err) {
      setModalErrorKey(resolveErrorKey(err));
    } finally {
      setSavingRoleDesc(false);
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
      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      {!canManage ? <Alert tone="info">{t('inviteSubtitle')}</Alert> : null}

      <Card className="flex flex-col gap-5 p-6">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-[20px] font-black text-ink">
            {t('rosterTitle')}
          </h2>
          <div className="flex flex-wrap items-center gap-2">
            <span className="rounded-pill border border-line bg-paper-2 px-3 py-0.5 text-[13px] font-bold text-muted">
              {t('rosterCount', { count: members.length })}
            </span>
            {canManage ? (
              <a
                href="#invite-section"
                className="inline-flex min-h-[44px] items-center gap-2 rounded-sm bg-green-600 px-4 text-[14px] font-bold text-paper shadow-card transition duration-150 hover:-translate-y-px hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              >
                <Plus className="h-4 w-4" aria-hidden="true" />
                <span>{t('inviteCta')}</span>
              </a>
            ) : null}
          </div>
        </div>

        {loading ? (
          <p role="status" className="py-4 text-[15px] font-bold text-ink-2">
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
                  <th className="px-3 py-3">{t('colRoleDescription')}</th>
                  {canManage ? (
                    <th className="px-3 py-3 text-right">{t('colActions')}</th>
                  ) : null}
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {members.map((member) => {
                  const isSelf = user?.id === member.userId;
                  const rowBusy = busyUserId === member.userId;
                  const roleDesc = member.roleDescription?.trim() ?? '';
                  const canEditRoleDesc = canEditMemberRoleDescription(
                    activeWorkspace?.role,
                    member.role,
                    isSelf,
                  );
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
                      <td className="px-3 py-3">
                        <div className="flex items-center justify-between gap-3">
                          <span
                            className={
                              roleDesc
                                ? 'line-clamp-2 max-w-[320px] text-[13px] leading-[1.45] text-ink-2'
                                : 'text-[13px] italic text-muted'
                            }
                          >
                            {roleDesc || t('noRoleSet')}
                          </span>
                          {canEditRoleDesc ? (
                            <Button
                              variant="secondary"
                              type="button"
                              aria-label={t(
                                roleDesc
                                  ? 'editRoleDescriptionAria'
                                  : 'setRoleDescriptionAria',
                                { name: member.displayName },
                              )}
                              onClick={() => openRoleModal(member)}
                              className="shrink-0"
                            >
                              {t(
                                roleDesc
                                  ? 'editRoleDescription'
                                  : 'setRoleDescription',
                              )}
                            </Button>
                          ) : null}
                        </div>
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
                            {rowBusy ? t('removingMember') : t('removeMember')}
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

        <Alert tone="info">{t('rolePermissionsHint')}</Alert>

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

      <Card className="flex flex-col gap-3 p-6">
        <Eyebrow>{t('rbacEyebrow')}</Eyebrow>
        <h2 className="text-[20px] font-black text-ink">{t('rbacTitle')}</h2>
        <p className="text-[13px] leading-[1.5] text-muted">{t('rbacLead')}</p>
        <div className="mt-1 overflow-x-auto">
          <div className="min-w-[340px] text-[13px]">
            <div className="grid grid-cols-[minmax(0,1fr)_52px_52px_52px_52px] gap-2 border-b border-line bg-paper-2 px-3 py-2.5 text-[12px] font-bold uppercase tracking-[0.04em] text-muted">
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
                  className="grid grid-cols-[minmax(0,1fr)_52px_52px_52px_52px] items-center gap-2 px-3 py-2.5"
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

      <ModalDialog
        open={editingMember !== null}
        onClose={closeRoleModal}
        title={
          editingMember
            ? t('roleModalTitle', { name: editingMember.displayName })
            : ''
        }
        closeLabel={tActions('close')}
      >
        {editingMember ? (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void saveRoleDescription(roleDraft);
            }}
            className="flex flex-col gap-4"
          >
            <Eyebrow>{t('roleModalEyebrow')}</Eyebrow>

            <div className="flex flex-wrap items-center justify-between gap-3 rounded-sm border border-line bg-paper-2 px-3.5 py-2.5">
              <div className="flex min-w-0 items-center gap-2.5">
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-sm bg-hex-2 text-[12px] font-black text-paper">
                  {initials(editingMember.displayName)}
                </span>
                <div className="min-w-0">
                  <p className="truncate text-[14px] font-bold text-ink">
                    {editingMember.displayName}
                  </p>
                  <p className="truncate font-mono text-[12px] text-muted">
                    {editingMember.email}
                  </p>
                </div>
              </div>
              <RoleBadge role={editingMember.role} />
            </div>

            <Textarea
              label={t('roleModalLabel')}
              placeholder={t('roleModalPlaceholder')}
              helperText={t('roleModalHelper', {
                name: editingMember.displayName,
              })}
              value={roleDraft}
              disabled={savingRoleDesc}
              warning={modalValidationError ?? undefined}
              onChange={(event) => {
                setRoleDraft(event.target.value);
                setModalValidationError(null);
              }}
            />

            {modalErrorKey ? (
              <Alert tone="warning">{tErrors(modalErrorKey)}</Alert>
            ) : null}

            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <div>
                {(editingMember.roleDescription ?? '').trim() !== '' ? (
                  <Button
                    variant="secondary"
                    type="button"
                    disabled={savingRoleDesc}
                    onClick={() => void saveRoleDescription('')}
                    className="border-dashed border-amber-line text-amber hover:border-amber-line hover:bg-amber-tint"
                  >
                    {savingRoleDesc
                      ? t('roleModalRemoving')
                      : t('roleModalRemove')}
                  </Button>
                ) : null}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="secondary"
                  type="button"
                  disabled={savingRoleDesc}
                  onClick={closeRoleModal}
                >
                  {tActions('cancel')}
                </Button>
                <Button type="submit" disabled={savingRoleDesc}>
                  {savingRoleDesc ? t('roleModalSaving') : t('roleModalSave')}
                </Button>
              </div>
            </div>
          </form>
        ) : null}
      </ModalDialog>
    </div>
  );
}

/**
 * Workspace members, invitations, and §4.1 RBAC permission matrix page.
 */
export default function MembersSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <MembersContent />
    </WorkspaceSettingsShell>
  );
}
