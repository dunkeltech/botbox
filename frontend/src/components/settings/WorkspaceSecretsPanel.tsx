// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { EyeOff, Lock, Pencil, ShieldCheck, Trash2 } from 'lucide-react';
import { type FormEvent, useCallback, useEffect, useId, useState } from 'react';
import { useTranslations } from 'next-intl';

import { Alert, Button, Card, Eyebrow, Input, Select } from '@/components/ui';
import {
  createWorkspaceSecret,
  deleteWorkspaceSecret,
  listWorkspaceSecrets,
  updateWorkspaceSecret,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canManageMembers } from '@/lib/rbac';
import type {
  SecretAuthScheme,
  SecretMetadata,
  WorkspaceRole,
} from '@/types/api';

export interface WorkspaceSecretsPanelProps {
  workspaceId: string;
  role: WorkspaceRole;
}

/**
 * Manages workspace-wide write-only encrypted secrets (/api/workspaces/{id}/secrets)
 * inherited by bots in the workspace, paired with security invariants.
 */
export function WorkspaceSecretsPanel({
  workspaceId,
  role,
}: WorkspaceSecretsPanelProps) {
  const t = useTranslations('workspaceSettings.secrets');
  const tSecrets = useTranslations('secrets');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const canManage = canManageMembers(role);
  const subdomainsId = useId();

  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [loading, setLoading] = useState(true);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  const [editingSecret, setEditingSecret] = useState<SecretMetadata | null>(
    null,
  );
  const [secretName, setSecretName] = useState('');
  const [domainMatch, setDomainMatch] = useState('');
  const [matchSubdomains, setMatchSubdomains] = useState(false);
  const [pathPrefix, setPathPrefix] = useState('');
  const [authScheme, setAuthScheme] = useState<SecretAuthScheme>('bearer');
  const [headerName, setHeaderName] = useState('');
  const [username, setUsername] = useState('');
  const [secretValue, setSecretValue] = useState('');
  const [saving, setSaving] = useState(false);
  const [bannerKey, setBannerKey] = useState<
    'savedBanner' | 'deletedBanner' | null
  >(null);
  const [formErrorKey, setFormErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const resetForm = useCallback(() => {
    setEditingSecret(null);
    setSecretName('');
    setDomainMatch('');
    setMatchSubdomains(false);
    setPathPrefix('');
    setAuthScheme('bearer');
    setHeaderName('');
    setUsername('');
    setSecretValue('');
    setFormErrorKey(null);
  }, []);

  const loadSecrets = useCallback(async () => {
    setLoading(true);
    setErrorKey(null);
    try {
      const res = await listWorkspaceSecrets(workspaceId);
      setSecrets(res.secrets ?? []);
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setLoading(false);
    }
  }, [workspaceId]);

  useEffect(() => {
    resetForm();
    setConfirmDeleteId(null);
    setBannerKey(null);
    void loadSecrets();
  }, [loadSecrets, resetForm]);

  function startEdit(secret: SecretMetadata) {
    setEditingSecret(secret);
    setSecretName(secret.name);
    setDomainMatch(secret.domainMatch);
    setMatchSubdomains(secret.matchSubdomains);
    setPathPrefix(secret.pathPrefix);
    setAuthScheme(secret.authScheme);
    setHeaderName(secret.headerName);
    setUsername(secret.username);
    setSecretValue('');
    setFormErrorKey(null);
    setBannerKey(null);
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canManage || saving) {
      return;
    }
    setSaving(true);
    setFormErrorKey(null);
    setBannerKey(null);

    try {
      if (editingSecret) {
        const updated = await updateWorkspaceSecret(
          workspaceId,
          editingSecret.id,
          {
            name: secretName.trim(),
            domainMatch: domainMatch.trim(),
            matchSubdomains,
            pathPrefix: pathPrefix.trim(),
            authScheme,
            headerName: authScheme === 'header' ? headerName.trim() : undefined,
            username: authScheme === 'basic' ? username.trim() : undefined,
            value: secretValue.length > 0 ? secretValue : undefined,
          },
        );
        setSecrets((prev) =>
          prev.map((item) => (item.id === updated.id ? updated : item)),
        );
      } else {
        const created = await createWorkspaceSecret(workspaceId, {
          name: secretName.trim(),
          domainMatch: domainMatch.trim(),
          matchSubdomains,
          pathPrefix: pathPrefix.trim() || undefined,
          authScheme,
          headerName: authScheme === 'header' ? headerName.trim() : undefined,
          username: authScheme === 'basic' ? username.trim() : undefined,
          value: secretValue,
        });
        setSecrets((prev) => [...prev, created]);
      }
      resetForm();
      setBannerKey('savedBanner');
    } catch (err) {
      setFormErrorKey(resolveErrorKey(err));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(secretId: string) {
    if (!canManage || deletingId) {
      return;
    }
    setDeletingId(secretId);
    setErrorKey(null);
    setBannerKey(null);
    try {
      await deleteWorkspaceSecret(workspaceId, secretId);
      setSecrets((prev) => prev.filter((item) => item.id !== secretId));
      if (editingSecret?.id === secretId) {
        resetForm();
      }
      setConfirmDeleteId(null);
      setBannerKey('deletedBanner');
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setDeletingId(null);
    }
  }

  function renderInjectionLabel(secret: SecretMetadata): string {
    if (secret.authScheme === 'bearer') {
      return tSecrets('injectionBearer');
    }
    if (secret.authScheme === 'header') {
      return tSecrets('injectionHeader', { headerName: secret.headerName });
    }
    return secret.username
      ? tSecrets('injectionBasic', { username: secret.username })
      : tSecrets('injectionBasicAnon');
  }

  return (
    <div className="flex flex-col gap-6">
      <Card className="flex flex-col gap-5 p-6">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('eyebrow')}</Eyebrow>
            <h2 className="text-[20px] font-black text-ink">{t('title')}</h2>
            <p className="max-w-2xl text-[14px] leading-[1.6] text-ink-2">
              {t('lead')}
            </p>
          </div>
          <span className="rounded-pill border border-mint-line bg-mint-soft px-3 py-1 font-mono text-[12px] font-bold text-green-600">
            {t('countBadge', { count: secrets.length })}
          </span>
        </div>

        {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}
        {bannerKey ? <Alert tone="success">{tSecrets(bannerKey)}</Alert> : null}

        {loading ? (
          <p role="status" className="text-[14px] text-muted">
            {tActions('loading')}
          </p>
        ) : secrets.length === 0 ? (
          <p className="rounded-sm border border-line bg-paper-2 px-4 py-3 text-[14px] text-muted">
            {t('empty')}
          </p>
        ) : (
          <div className="overflow-x-auto rounded-md border border-line">
            <table
              aria-label={tSecrets('tableAria')}
              className="w-full border-collapse text-left"
            >
              <thead>
                <tr className="border-b border-line bg-paper-2 text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  <th className="px-3 py-2.5">{tSecrets('colName')}</th>
                  <th className="px-3 py-2.5">{tSecrets('colDestination')}</th>
                  <th className="px-3 py-2.5">{tSecrets('colInjection')}</th>
                  <th className="px-3 py-2.5">{tSecrets('colPreview')}</th>
                  <th className="px-3 py-2.5">{tSecrets('colUsage')}</th>
                  {canManage ? (
                    <th className="px-3 py-2.5 text-right">
                      {tSecrets('colActions')}
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody className="divide-y divide-line text-[13px]">
                {secrets.map((secret) => {
                  const isConfirming = confirmDeleteId === secret.id;
                  const isDeleting = deletingId === secret.id;
                  return (
                    <tr key={secret.id}>
                      <td className="px-3 py-3">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-mono font-bold text-ink">
                            {secret.name}
                          </span>
                          <span className="rounded-pill border border-mint-line bg-mint-soft px-2 py-0.5 text-[11px] font-bold text-green-600">
                            {tSecrets('scopeWorkspaceBadge')}
                          </span>
                        </div>
                      </td>
                      <td className="px-3 py-3 font-mono text-ink-2">
                        <div>
                          {secret.domainMatch}
                          {secret.pathPrefix || ''}
                        </div>
                        {secret.matchSubdomains ? (
                          <div className="text-[11px] text-muted">
                            {tSecrets('subdomainsTag')}
                          </div>
                        ) : null}
                      </td>
                      <td className="px-3 py-3 font-mono text-ink-2">
                        {renderInjectionLabel(secret)}
                      </td>
                      <td className="px-3 py-3">
                        <span className="inline-flex items-center gap-1.5 rounded-xs border border-line bg-paper-2 px-2 py-0.5 font-mono text-[12px] text-ink">
                          <Lock
                            className="h-3 w-3 text-muted"
                            aria-hidden="true"
                          />
                          <span>{secret.maskedPreview}</span>
                        </span>
                      </td>
                      <td className="px-3 py-3 font-mono text-muted">
                        {secret.usedCount > 0
                          ? tSecrets('usageCountLabel', {
                              count: secret.usedCount,
                            })
                          : tSecrets('neverUsed')}
                      </td>
                      {canManage ? (
                        <td className="px-3 py-3 text-right">
                          <div className="inline-flex flex-wrap items-center justify-end gap-1.5">
                            <Button
                              type="button"
                              variant="secondary"
                              onClick={() => startEdit(secret)}
                            >
                              <Pencil
                                className="h-3.5 w-3.5"
                                aria-hidden="true"
                              />
                              <span>{tSecrets('editAction')}</span>
                            </Button>
                            {isConfirming ? (
                              <>
                                <Button
                                  type="button"
                                  variant="secondary"
                                  disabled={isDeleting}
                                  onClick={() => void handleDelete(secret.id)}
                                  className="border-amber-line bg-amber-tint text-amber"
                                >
                                  {tSecrets('deleteConfirmAction')}
                                </Button>
                                <Button
                                  type="button"
                                  variant="secondary"
                                  disabled={isDeleting}
                                  onClick={() => setConfirmDeleteId(null)}
                                >
                                  {tActions('cancel')}
                                </Button>
                              </>
                            ) : (
                              <Button
                                type="button"
                                variant="secondary"
                                onClick={() => setConfirmDeleteId(secret.id)}
                              >
                                <Trash2
                                  className="h-3.5 w-3.5"
                                  aria-hidden="true"
                                />
                                <span>{tSecrets('deleteAction')}</span>
                              </Button>
                            )}
                          </div>
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

      {!canManage ? (
        <Alert tone="info">{t('readOnlyNotice')}</Alert>
      ) : (
        <Card className="flex flex-col gap-5 p-6">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="flex flex-col gap-1">
              <Eyebrow>{t('addEyebrow')}</Eyebrow>
              <h2 className="text-[20px] font-black text-ink">
                {editingSecret
                  ? t('editTitle', { name: editingSecret.name })
                  : t('addTitle')}
              </h2>
              <p className="text-[14px] text-ink-2">{t('addLead')}</p>
            </div>
            {editingSecret ? (
              <Button type="button" variant="secondary" onClick={resetForm}>
                {tSecrets('cancelEdit')}
              </Button>
            ) : null}
          </div>

          {formErrorKey ? (
            <Alert tone="warning">{tErrors(formErrorKey)}</Alert>
          ) : null}

          <form
            onSubmit={(event) => void handleSubmit(event)}
            className="flex flex-col gap-4"
          >
            <div className="grid gap-4 md:grid-cols-2">
              <Input
                label={tSecrets('nameLabel')}
                placeholder={tSecrets('namePlaceholder')}
                value={secretName}
                onChange={(event) => setSecretName(event.target.value)}
                mono
                required
              />
              <div className="flex flex-col gap-2">
                <Input
                  label={tSecrets('domainLabel')}
                  placeholder={tSecrets('domainPlaceholder')}
                  value={domainMatch}
                  onChange={(event) => setDomainMatch(event.target.value)}
                  mono
                  required
                />
                <label
                  htmlFor={subdomainsId}
                  className="inline-flex min-h-[32px] cursor-pointer items-center gap-2 text-[13px] font-bold text-ink-2"
                >
                  <input
                    id={subdomainsId}
                    type="checkbox"
                    checked={matchSubdomains}
                    onChange={(event) =>
                      setMatchSubdomains(event.target.checked)
                    }
                    className="h-4 w-4 accent-green-600"
                  />
                  <span>{tSecrets('subdomainsLabel')}</span>
                </label>
              </div>
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <Input
                label={tSecrets('pathPrefixLabel')}
                placeholder={tSecrets('pathPrefixPlaceholder')}
                value={pathPrefix}
                onChange={(event) => setPathPrefix(event.target.value)}
                mono
              />
              <Select
                label={tSecrets('schemeLabel')}
                value={authScheme}
                onChange={(event) =>
                  setAuthScheme(event.target.value as SecretAuthScheme)
                }
              >
                <option value="bearer">{tSecrets('schemeBearer')}</option>
                <option value="header">{tSecrets('schemeHeader')}</option>
                <option value="basic">{tSecrets('schemeBasic')}</option>
              </Select>
            </div>

            {authScheme === 'header' ? (
              <Input
                label={tSecrets('headerNameLabel')}
                placeholder={tSecrets('headerNamePlaceholder')}
                value={headerName}
                onChange={(event) => setHeaderName(event.target.value)}
                mono
                required
              />
            ) : null}

            {authScheme === 'basic' ? (
              <Input
                label={tSecrets('usernameLabel')}
                placeholder={tSecrets('usernamePlaceholder')}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                mono
                required
              />
            ) : null}

            <Input
              label={
                editingSecret
                  ? tSecrets('valueReplaceLabel')
                  : tSecrets('valueCreateLabel')
              }
              placeholder={tSecrets('valuePlaceholder')}
              type="password"
              autoComplete="new-password"
              value={secretValue}
              onChange={(event) => setSecretValue(event.target.value)}
              mono
              required={!editingSecret}
            />

            <div className="flex justify-end">
              <Button type="submit" arrow disabled={saving}>
                {saving
                  ? tSecrets('savingSecret')
                  : editingSecret
                    ? tSecrets('updateSubmit')
                    : tSecrets('createSubmit')}
              </Button>
            </div>
          </form>
        </Card>
      )}

      <div className="flex flex-col gap-4 rounded-lg bg-terminal p-6 text-on-dark shadow-card">
        <div className="flex flex-col gap-1">
          <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-green-light">
            {`// ${t('invariantEyebrow')}`}
          </p>
          <h2 className="text-[20px] font-black text-paper">
            {t('invariantTitle')}
          </h2>
        </div>
        <div className="grid gap-4 md:grid-cols-3">
          <div className="flex flex-col gap-1.5 rounded-md border border-dark bg-dark-2 p-4">
            <div className="flex items-center gap-2 font-mono text-[12px] font-bold text-green-light">
              <ShieldCheck className="h-4 w-4" aria-hidden="true" />
              <span>{t('invariant1Title')}</span>
            </div>
            <p className="text-[13px] leading-[1.55] text-on-dark-muted">
              {t('invariant1Body')}
            </p>
          </div>
          <div className="flex flex-col gap-1.5 rounded-md border border-dark bg-dark-2 p-4">
            <div className="flex items-center gap-2 font-mono text-[12px] font-bold text-green-light">
              <EyeOff className="h-4 w-4" aria-hidden="true" />
              <span>{t('invariant2Title')}</span>
            </div>
            <p className="text-[13px] leading-[1.55] text-on-dark-muted">
              {t('invariant2Body')}
            </p>
          </div>
          <div className="flex flex-col gap-1.5 rounded-md border border-dark bg-dark-2 p-4">
            <div className="flex items-center gap-2 font-mono text-[12px] font-bold text-green-light">
              <Lock className="h-4 w-4" aria-hidden="true" />
              <span>{t('invariant3Title')}</span>
            </div>
            <p className="text-[13px] leading-[1.55] text-on-dark-muted">
              {t('invariant3Body')}
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}
