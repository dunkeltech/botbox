'use client';

import { Copy, Trash2 } from 'lucide-react';
import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from 'react';
import { useTranslations } from 'next-intl';

import { AppShell } from '@/components/shell/AppShell';
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

  const pendingInvitations = invitations.filter((inv) => !inv.acceptedAt);

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Eyebrow>{t('eyebrow')}</Eyebrow>
        <h1 className="text-[34px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[44px]">
          {t('title')}
        </h1>
        <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
          {t('lead')}
        </p>
      </div>

      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      {!canManage ? <Alert tone="info">{t('inviteSubtitle')}</Alert> : null}

      <Card className="flex flex-col gap-4 p-6">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-[20px] font-black text-ink">
            {t('rosterTitle')}
          </h2>
          <span className="text-[14px] font-bold text-muted">
            {t('rosterCount', { count: members.length })}
          </span>
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
                  {canManage ? (
                    <th className="px-3 py-3 text-right">{t('colActions')}</th>
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
                        <div className="flex items-center gap-2">
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
      </Card>

      {canManage ? (
        <div className="grid gap-6 lg:grid-cols-2 lg:items-start">
          <Card className="flex flex-col gap-4 p-6">
            <h2 className="text-[20px] font-black text-ink">
              {t('inviteTitle')}
            </h2>

            <form
              onSubmit={(event) => void handleCreateInvitation(event)}
              className="flex flex-col gap-4"
            >
              <Input
                label={t('inviteEmailLabel')}
                placeholder={t('inviteEmailPlaceholder')}
                helperText={t('inviteEmailHelper')}
                type="email"
                value={inviteEmail}
                onChange={(event) => setInviteEmail(event.target.value)}
              />

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

              <Button type="submit" arrow disabled={inviting}>
                {inviting ? t('inviting') : t('inviteSubmit')}
              </Button>
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

          <Card className="flex flex-col gap-4 p-6">
            <h2 className="text-[20px] font-black text-ink">
              {t('pendingTitle')}
            </h2>

            {pendingInvitations.length === 0 ? (
              <p className="text-[14px] text-muted">{t('noPending')}</p>
            ) : (
              <ul className="flex flex-col gap-3">
                {pendingInvitations.map((inv) => {
                  const isBusy = busyInviteId === inv.id;
                  const expiryText = `${t('expiresPrefix')} ${new Date(inv.expiresAt).toLocaleDateString()}`;
                  return (
                    <li
                      key={inv.id}
                      className="flex flex-wrap items-center justify-between gap-3 rounded-sm border border-line bg-paper-2 p-3.5"
                    >
                      <div className="flex min-w-0 flex-col gap-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="truncate font-mono text-[13px] font-bold text-ink">
                            {inv.email ?? t('openInviteLabel')}
                          </span>
                          <RoleBadge role={inv.role} />
                        </div>
                        <span className="text-[12px] text-muted">
                          {expiryText}
                        </span>
                      </div>

                      <Button
                        variant="secondary"
                        type="button"
                        disabled={isBusy}
                        onClick={() => void handleRevokeInvitation(inv.id)}
                      >
                        <Trash2 className="h-4 w-4" aria-hidden="true" />
                        <span>{isBusy ? t('revoking') : t('revoke')}</span>
                      </Button>
                    </li>
                  );
                })}
              </ul>
            )}
          </Card>
        </div>
      ) : null}
    </div>
  );
}

/**
 * Workspace members and invitations RBAC management page.
 */
export default function MembersSettingsPage() {
  return (
    <AppShell>
      <MembersContent />
    </AppShell>
  );
}
