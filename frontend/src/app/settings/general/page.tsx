'use client';

import { AlertTriangle, Trash2 } from 'lucide-react';
import { useRouter } from 'next/navigation';
import {
  type FormEvent,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useState,
} from 'react';
import { useTranslations } from 'next-intl';

import { WorkspaceSettingsShell } from '@/components/settings/WorkspaceSettingsShell';
import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  ModalDialog,
  RadioCardGroup,
  Select,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import {
  deleteWorkspace,
  listMembers,
  updateMemberRole,
  updateWorkspace,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { DriverType, WorkspaceMember } from '@/types/api';

function GeneralSettingsContent() {
  const t = useTranslations('workspaceSettings.general');
  const tDanger = useTranslations('workspaceSettings.danger');
  const tLang = useTranslations('builder.languages');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const router = useRouter();
  const { activeWorkspace, workspaces, reloadWorkspaces } = useAuth();
  const retentionId = useId();

  const isOwner = activeWorkspace?.role === 'owner';

  const [wsName, setWsName] = useState('');
  const [wsLocale, setWsLocale] = useState('en');
  const [wsDriver, setWsDriver] = useState<DriverType>('docker');
  const [retentionDays, setRetentionDays] = useState(7);

  const [savingProfile, setSavingProfile] = useState(false);
  const [profileSaved, setProfileSaved] = useState(false);
  const [profileErrorKey, setProfileErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  const [savingRuntime, setSavingRuntime] = useState(false);
  const [runtimeSaved, setRuntimeSaved] = useState(false);
  const [runtimeErrorKey, setRuntimeErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  const [members, setMembers] = useState<WorkspaceMember[]>([]);
  const [transferUserId, setTransferUserId] = useState('');
  const [transferring, setTransferring] = useState(false);
  const [transferredName, setTransferredName] = useState<string | null>(null);
  const [dangerErrorKey, setDangerErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  const [deleteOpen, setDeleteOpen] = useState(false);
  const [confirmSlug, setConfirmSlug] = useState('');
  const [deleting, setDeleting] = useState(false);
  const [deleteErrorKey, setDeleteErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  useEffect(() => {
    if (!activeWorkspace) {
      return;
    }
    setWsName(activeWorkspace.name);
    setWsLocale(activeWorkspace.defaultLocale || 'en');
    setWsDriver(activeWorkspace.defaultDriver || 'docker');
    setRetentionDays(activeWorkspace.screenshotRetentionDays ?? 7);
    setProfileSaved(false);
    setProfileErrorKey(null);
    setRuntimeSaved(false);
    setRuntimeErrorKey(null);
    setTransferredName(null);
    setDangerErrorKey(null);
    setDeleteOpen(false);
    setConfirmSlug('');
    setDeleteErrorKey(null);
  }, [activeWorkspace]);

  const loadCandidates = useCallback(async () => {
    if (!activeWorkspace || !isOwner) {
      setMembers([]);
      return;
    }
    try {
      const res = await listMembers(activeWorkspace.id);
      setMembers(res.members ?? []);
    } catch {
      setMembers([]);
    }
  }, [activeWorkspace, isOwner]);

  useEffect(() => {
    void loadCandidates();
  }, [loadCandidates]);

  const transferCandidates = useMemo(
    () => members.filter((member) => member.role !== 'owner'),
    [members],
  );

  useEffect(() => {
    const firstCandidate = transferCandidates[0];
    if (
      firstCandidate &&
      !transferCandidates.some((m) => m.userId === transferUserId)
    ) {
      setTransferUserId(firstCandidate.userId);
    } else if (!firstCandidate) {
      setTransferUserId('');
    }
  }, [transferCandidates, transferUserId]);

  if (!activeWorkspace) {
    return null;
  }

  async function handleSaveProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeWorkspace || !isOwner || savingProfile) {
      return;
    }
    setSavingProfile(true);
    setProfileSaved(false);
    setProfileErrorKey(null);
    try {
      await updateWorkspace(activeWorkspace.id, {
        name: wsName.trim(),
        defaultLocale: wsLocale,
      });
      await reloadWorkspaces(activeWorkspace.id);
      setProfileSaved(true);
    } catch (err) {
      setProfileErrorKey(resolveErrorKey(err));
    } finally {
      setSavingProfile(false);
    }
  }

  async function handleSaveRuntime(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeWorkspace || !isOwner || savingRuntime) {
      return;
    }
    setSavingRuntime(true);
    setRuntimeSaved(false);
    setRuntimeErrorKey(null);
    try {
      await updateWorkspace(activeWorkspace.id, {
        defaultDriver: wsDriver,
        screenshotRetentionDays: retentionDays,
      });
      await reloadWorkspaces(activeWorkspace.id);
      setRuntimeSaved(true);
    } catch (err) {
      setRuntimeErrorKey(resolveErrorKey(err));
    } finally {
      setSavingRuntime(false);
    }
  }

  async function handleTransferOwnership(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!activeWorkspace || !isOwner || !transferUserId || transferring) {
      return;
    }
    setTransferring(true);
    setTransferredName(null);
    setDangerErrorKey(null);
    const target = transferCandidates.find((m) => m.userId === transferUserId);
    try {
      await updateMemberRole(activeWorkspace.id, transferUserId, {
        role: 'owner',
      });
      await loadCandidates();
      setTransferredName(target?.displayName ?? '');
    } catch (err) {
      setDangerErrorKey(resolveErrorKey(err));
    } finally {
      setTransferring(false);
    }
  }

  async function handleDeleteWorkspace(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      !activeWorkspace ||
      !isOwner ||
      deleting ||
      confirmSlug !== activeWorkspace.slug
    ) {
      return;
    }
    setDeleting(true);
    setDeleteErrorKey(null);
    const deletedId = activeWorkspace.id;
    const fallbackWorkspace = workspaces.find((ws) => ws.id !== deletedId);
    try {
      await deleteWorkspace(deletedId);
      setDeleteOpen(false);
      await reloadWorkspaces(fallbackWorkspace?.id);
      router.replace('/');
    } catch (err) {
      setDeleteErrorKey(resolveErrorKey(err));
    } finally {
      setDeleting(false);
    }
  }

  const canDeleteWorkspace = workspaces.length > 1;
  const slugConfirmed = confirmSlug === activeWorkspace.slug;

  return (
    <div className="flex flex-col gap-6">
      {!isOwner ? <Alert tone="info">{t('readOnlyNotice')}</Alert> : null}

      <Card className="flex flex-col gap-5 p-6">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('profileEyebrow')}</Eyebrow>
            <h2 className="text-[20px] font-black text-ink">
              {t('profileTitle')}
            </h2>
            <p className="text-[14px] text-ink-2">{t('profileLead')}</p>
          </div>
          <span className="rounded-pill border border-line bg-paper-2 px-3 py-0.5 text-[12px] font-bold text-muted">
            {t('ownerOnlyBadge')}
          </span>
        </div>

        {profileErrorKey ? (
          <Alert tone="warning">{tErrors(profileErrorKey)}</Alert>
        ) : null}
        {profileSaved ? (
          <Alert tone="success">{t('savedProfile')}</Alert>
        ) : null}

        <form
          onSubmit={(event) => void handleSaveProfile(event)}
          className="flex flex-col gap-4"
        >
          <div className="grid gap-4 md:grid-cols-2">
            <Input
              label={t('nameLabel')}
              value={wsName}
              required
              maxLength={100}
              disabled={!isOwner || savingProfile}
              onChange={(event) => {
                setWsName(event.target.value);
                setProfileSaved(false);
              }}
            />
            <Input
              label={t('slugLabel')}
              helperText={t('slugHelper')}
              value={activeWorkspace.slug}
              readOnly
              mono
            />
          </div>

          <div className="max-w-sm">
            <Select
              label={t('localeLabel')}
              helperText={t('localeHelper')}
              value={wsLocale}
              disabled={!isOwner || savingProfile}
              onChange={(event) => {
                setWsLocale(event.target.value);
                setProfileSaved(false);
              }}
            >
              <option value="en">{tLang('en')}</option>
              <option value="de">{tLang('de')}</option>
              <option value="fr">{tLang('fr')}</option>
            </Select>
          </div>

          {isOwner ? (
            <div className="flex justify-end">
              <Button type="submit" disabled={savingProfile || !wsName.trim()}>
                {savingProfile ? t('savingProfile') : t('saveProfile')}
              </Button>
            </div>
          ) : null}
        </form>
      </Card>

      <Card className="flex flex-col gap-5 p-6">
        <div className="flex flex-col gap-1">
          <Eyebrow>{t('runtimeEyebrow')}</Eyebrow>
          <h2 className="text-[20px] font-black text-ink">
            {t('runtimeTitle')}
          </h2>
          <p className="text-[14px] text-ink-2">{t('runtimeLead')}</p>
        </div>

        {runtimeErrorKey ? (
          <Alert tone="warning">{tErrors(runtimeErrorKey)}</Alert>
        ) : null}
        {runtimeSaved ? (
          <Alert tone="success">{t('savedRuntime')}</Alert>
        ) : null}

        <form
          onSubmit={(event) => void handleSaveRuntime(event)}
          className="flex flex-col gap-5"
        >
          <RadioCardGroup<DriverType>
            legend={t('driverLegend')}
            name="workspace-default-driver"
            value={wsDriver}
            disabled={!isOwner || savingRuntime}
            onChange={(nextDriver) => {
              setWsDriver(nextDriver);
              setRuntimeSaved(false);
            }}
            options={[
              {
                value: 'docker',
                title: t('driverDockerTitle'),
                description: t('driverDockerDesc'),
              },
              {
                value: 'kubernetes',
                title: t('driverK8sTitle'),
                description: t('driverK8sDesc'),
              },
            ]}
          />

          <div className="flex flex-col gap-2 rounded-md border border-line bg-paper-2 p-4">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label
                htmlFor={retentionId}
                className="text-[14px] font-bold text-ink"
              >
                {t('retentionLabel')}
              </label>
              <span className="rounded-pill border border-line bg-paper px-3 py-0.5 font-mono text-[13px] font-bold text-ink">
                {t('retentionDaysValue', { days: retentionDays })}
              </span>
            </div>

            <div className="flex flex-wrap items-center gap-4">
              <input
                id={retentionId}
                type="range"
                min={1}
                max={30}
                value={retentionDays}
                disabled={!isOwner || savingRuntime}
                onChange={(event) => {
                  setRetentionDays(Number(event.target.value));
                  setRuntimeSaved(false);
                }}
                className="min-h-[44px] flex-1 accent-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              />
              <input
                aria-label={t('retentionLabel')}
                type="number"
                min={1}
                max={30}
                value={retentionDays}
                disabled={!isOwner || savingRuntime}
                onChange={(event) => {
                  const parsed = Number(event.target.value);
                  if (Number.isFinite(parsed)) {
                    setRetentionDays(Math.min(30, Math.max(1, parsed)));
                    setRuntimeSaved(false);
                  }
                }}
                className="min-h-[44px] w-24 rounded-sm border-[1.5px] border-line-strong bg-paper px-3 py-2 font-mono text-[13px] text-ink transition duration-150 focus:border-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:bg-paper-2 disabled:text-muted"
              />
            </div>

            <p className="text-[13px] leading-[1.5] text-muted">
              {t('retentionHelper', { days: retentionDays })}
            </p>
          </div>

          {isOwner ? (
            <div className="flex justify-end">
              <Button type="submit" disabled={savingRuntime}>
                {savingRuntime ? t('savingRuntime') : t('saveRuntime')}
              </Button>
            </div>
          ) : null}
        </form>
      </Card>

      {isOwner ? (
        <section
          aria-labelledby="workspace-danger-zone-heading"
          className="flex flex-col gap-5 rounded-lg border-[1.5px] border-dashed border-amber-line bg-amber-tint p-6 shadow-card"
        >
          <div className="flex flex-col gap-1">
            <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-amber">
              {`// ${tDanger('eyebrow')}`}
            </p>
            <div className="flex items-center gap-2">
              <AlertTriangle
                className="h-5 w-5 text-amber"
                aria-hidden="true"
              />
              <h2
                id="workspace-danger-zone-heading"
                className="text-[20px] font-black text-ink"
              >
                {tDanger('title')}
              </h2>
            </div>
          </div>

          {dangerErrorKey ? (
            <Alert tone="warning">{tErrors(dangerErrorKey)}</Alert>
          ) : null}
          {transferredName ? (
            <Alert tone="success">
              {tDanger('transferredBanner', { name: transferredName })}
            </Alert>
          ) : null}

          <div className="flex flex-col gap-4 rounded-md border border-amber-line bg-paper p-5">
            <div className="flex flex-col gap-1">
              <h3 className="text-[16px] font-black text-ink">
                {tDanger('transferTitle')}
              </h3>
              <p className="text-[14px] leading-[1.55] text-ink-2">
                {tDanger('transferDesc')}
              </p>
            </div>

            {transferCandidates.length === 0 ? (
              <p className="text-[14px] text-muted">
                {tDanger('transferNoCandidates')}
              </p>
            ) : (
              <form
                onSubmit={(event) => void handleTransferOwnership(event)}
                className="flex flex-col gap-3 sm:flex-row sm:items-end"
              >
                <div className="flex-1">
                  <Select
                    label={tDanger('transferSelectLabel')}
                    value={transferUserId}
                    disabled={transferring}
                    onChange={(event) => setTransferUserId(event.target.value)}
                  >
                    {transferCandidates.map((member) => (
                      <option key={member.userId} value={member.userId}>
                        {`${member.displayName} (${member.email})`}
                      </option>
                    ))}
                  </Select>
                </div>
                <Button
                  type="submit"
                  variant="secondary"
                  disabled={transferring || !transferUserId}
                  className="border-amber-line text-amber"
                >
                  {transferring
                    ? tDanger('transferring')
                    : tDanger('transferSubmit')}
                </Button>
              </form>
            )}
          </div>

          <div className="flex flex-wrap items-center justify-between gap-4 rounded-md border border-dashed border-amber-line bg-paper p-5">
            <div className="flex max-w-xl flex-col gap-1">
              <h3 className="text-[16px] font-black text-ink">
                {tDanger('deleteTitle')}
              </h3>
              <p className="text-[14px] leading-[1.55] text-ink-2">
                {tDanger('deleteDesc', { workspaceName: activeWorkspace.name })}
              </p>
              {!canDeleteWorkspace ? (
                <p className="text-[13px] font-bold text-amber">
                  {tDanger('deleteLastWorkspaceHint')}
                </p>
              ) : null}
            </div>
            <Button
              type="button"
              variant="secondary"
              disabled={!canDeleteWorkspace}
              onClick={() => {
                setConfirmSlug('');
                setDeleteErrorKey(null);
                setDeleteOpen(true);
              }}
              className="border-dashed border-amber-line text-amber"
            >
              <Trash2 className="h-4 w-4" aria-hidden="true" />
              <span>{tDanger('deleteButton')}</span>
            </Button>
          </div>
        </section>
      ) : null}

      <ModalDialog
        open={deleteOpen}
        onClose={() => {
          if (!deleting) {
            setDeleteOpen(false);
          }
        }}
        title={tDanger('dialogTitle', { workspaceName: activeWorkspace.name })}
        subtitle={tDanger('dialogSubtitle')}
        closeLabel={tActions('close')}
      >
        <form
          onSubmit={(event) => void handleDeleteWorkspace(event)}
          className="flex flex-col gap-4"
        >
          <Alert tone="warning">
            {tDanger('dialogWarning', { workspaceName: activeWorkspace.name })}
          </Alert>

          {deleteErrorKey ? (
            <Alert tone="warning">{tErrors(deleteErrorKey)}</Alert>
          ) : null}

          <Input
            label={tDanger('confirmSlugLabel', { slug: activeWorkspace.slug })}
            helperText={tDanger('confirmSlugHelper', {
              slug: activeWorkspace.slug,
            })}
            value={confirmSlug}
            mono
            disabled={deleting}
            onChange={(event) => setConfirmSlug(event.target.value)}
          />

          <div className="flex flex-wrap justify-end gap-3 pt-2">
            <Button
              type="button"
              variant="secondary"
              autoFocus
              disabled={deleting}
              onClick={() => setDeleteOpen(false)}
            >
              {tActions('cancel')}
            </Button>
            <Button
              type="submit"
              variant="secondary"
              disabled={deleting || !slugConfirmed}
              className="border-dashed border-amber-line bg-amber-tint text-amber"
            >
              {deleting ? tDanger('deleting') : tDanger('confirmDelete')}
            </Button>
          </div>
        </form>
      </ModalDialog>
    </div>
  );
}

/**
 * Workspace General, Compute Driver, Screenshot Retention, and Danger Zone settings page.
 */
export default function GeneralSettingsPage() {
  return (
    <WorkspaceSettingsShell>
      <GeneralSettingsContent />
    </WorkspaceSettingsShell>
  );
}
