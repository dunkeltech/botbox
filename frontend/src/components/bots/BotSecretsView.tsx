'use client';

import {
  ArrowLeft,
  ArrowRight,
  Ban,
  Check,
  CheckCircle2,
  Globe,
  KeyRound,
  Lock,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  ShieldCheck,
  Trash2,
  X,
} from 'lucide-react';
import Link from 'next/link';
import { useTranslations } from 'next-intl';
import {
  type FormEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';

import { BudgetPanel } from '@/components/chat/BudgetPanel';

import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  RadioCardGroup,
  Select,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import {
  ApiClientError,
  createBotSecret,
  createWorkspaceSecret,
  deleteBotSecret,
  deleteWorkspaceSecret,
  getBotEgressPolicy,
  listBotEgressLogs,
  listBotSecrets,
  updateBotEgressPolicy,
  updateBotSecret,
  updateWorkspaceSecret,
} from '@/lib/api-client';
import {
  computeTriageMutation,
  computeUndoMutation,
  evaluateHostInPolicy,
  isPolicyDraftDirty,
  normalizeHostPattern,
  type TriageUndoSpec,
} from '@/lib/egress-policy';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canManageBot, canManageMembers } from '@/lib/rbac';
import type {
  Bot,
  EgressEnforcementStatus,
  EgressLogAction,
  EgressLogEntry,
  EgressPolicyMode,
  EgressPolicyResponse,
  SecretAuthScheme,
  SecretMetadata,
  SecretScope,
  WorkspaceSummary,
} from '@/types/api';

export interface BotSecretsViewProps {
  workspace: WorkspaceSummary;
  bot: Bot;
}

type LogFilter = 'all' | EgressLogAction;

const HOW_IT_WORKS_STEPS = [
  {
    id: 'step1',
    icon: KeyRound,
    numKey: 'step1Num',
    titleKey: 'step1Title',
    descKey: 'step1Desc',
  },
  {
    id: 'step2',
    icon: Globe,
    numKey: 'step2Num',
    titleKey: 'step2Title',
    descKey: 'step2Desc',
  },
  {
    id: 'step3',
    icon: ShieldCheck,
    numKey: 'step3Num',
    titleKey: 'step3Title',
    descKey: 'step3Desc',
  },
] as const;

const LOG_FILTER_OPTIONS: ReadonlyArray<{
  value: LogFilter;
  labelKey: 'filterAll' | 'filterTokenAdded' | 'filterPassed' | 'filterBlocked';
}> = [
  { value: 'all', labelKey: 'filterAll' },
  { value: 'mitm_token_added', labelKey: 'filterTokenAdded' },
  { value: 'tunnel_passthrough', labelKey: 'filterPassed' },
  { value: 'blocked_host', labelKey: 'filterBlocked' },
];

const LOG_BADGE_STYLES: Record<
  EgressLogAction,
  {
    className: string;
    labelKey: 'logBadgeTokenAdded' | 'logBadgePassed' | 'logBadgeBlocked';
  }
> = {
  mitm_token_added: {
    className: 'border-mint-line/40 bg-mint-soft text-green-600',
    labelKey: 'logBadgeTokenAdded',
  },
  tunnel_passthrough: {
    className: 'border-on-dark-faint/30 bg-dark-2 text-on-dark',
    labelKey: 'logBadgePassed',
  },
  blocked_host: {
    className: 'border-dashed border-amber-line bg-dark-2 text-amber-on-dark',
    labelKey: 'logBadgeBlocked',
  },
};

function formatDestinationTarget(secret: SecretMetadata): string {
  return `${secret.domainMatch}${secret.pathPrefix}`;
}

function formatLogTarget(entry: EgressLogEntry): string {
  return `${entry.method} ${entry.host}:${entry.port}${entry.path}`;
}

function formatTimestamp(iso: string): string {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) {
    return iso;
  }
  return parsed.toISOString().slice(0, 19).replace('T', ' ');
}

function normalizeHostEntry(raw: string): string {
  return normalizeHostPattern(raw);
}

interface HostTriageFeedback {
  host: string;
  op: 'allow' | 'block';
  status: EgressEnforcementStatus;
  revision: number;
  undo: TriageUndoSpec;
}

type PolicyStatusNotice =
  | { kind: 'applied' }
  | { kind: 'pending'; revision: number }
  | { kind: 'not_running'; revision: number }
  | { kind: 'reconciled'; revision: number }
  | { kind: 'undo_complete'; host: string }
  | { kind: 'undo_conflict'; host: string };

function isUncertainWriteError(err: unknown): boolean {
  if (!(err instanceof ApiClientError)) {
    return true;
  }
  return err.status === 0 || err.status >= 500;
}

/**
 * Per-bot Secrets & Outbound Egress Network manager backed by workspace/bot
 * secrets endpoints, egress allowlist controls, and sanitized MITM audit logs.
 */
export function BotSecretsView({ workspace, bot }: BotSecretsViewProps) {
  const t = useTranslations('secrets');
  const tTabs = useTranslations('builder.tabs');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');
  const { user } = useAuth();

  const canEditBotSecrets = canManageBot(workspace.role, user?.id, bot);
  const canEditWorkspaceSecrets = canManageMembers(workspace.role);

  const subdomainsCheckboxId = useId();

  const [secrets, setSecrets] = useState<SecretMetadata[]>([]);
  const [policy, setPolicy] = useState<EgressPolicyResponse | null>(null);
  const [logs, setLogs] = useState<EgressLogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadErrorKey, setLoadErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );

  // Secret create / edit form state. Secret value stays strictly write-only.
  const [editingSecret, setEditingSecret] = useState<SecretMetadata | null>(
    null,
  );
  const [scope, setScope] = useState<SecretScope>('bot');
  const [secretName, setSecretName] = useState('');
  const [domainMatch, setDomainMatch] = useState('');
  const [matchSubdomains, setMatchSubdomains] = useState(false);
  const [pathPrefix, setPathPrefix] = useState('');
  const [authScheme, setAuthScheme] = useState<SecretAuthScheme>('bearer');
  const [headerName, setHeaderName] = useState('');
  const [username, setUsername] = useState('');
  const [secretValue, setSecretValue] = useState('');
  const [savingSecret, setSavingSecret] = useState(false);
  const [deletingSecretId, setDeletingSecretId] = useState<string | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [secretBannerKey, setSecretBannerKey] = useState<
    'savedBanner' | 'deletedBanner' | null
  >(null);
  const [secretErrorKey, setSecretErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  // Outbound policy form draft state (kept separate from authoritative `policy`).
  const [policyMode, setPolicyMode] = useState<EgressPolicyMode>('allowlist');
  const [allowedHosts, setAllowedHosts] = useState<string[]>([]);
  const [blockedHosts, setBlockedHosts] = useState<string[]>([]);
  const [hostDraft, setHostDraft] = useState('');
  const [blockedHostDraft, setBlockedHostDraft] = useState('');
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [policyMutationInFlight, setPolicyMutationInFlight] = useState(false);
  const [triagePendingHost, setTriagePendingHost] = useState<string | null>(
    null,
  );
  const [policyStatusNotice, setPolicyStatusNotice] =
    useState<PolicyStatusNotice | null>(null);
  const [policyErrorKey, setPolicyErrorKey] =
    useState<ErrorTranslationKey | null>(null);
  const [draftConflictPrompt, setDraftConflictPrompt] = useState(false);
  const [policyStateUnknown, setPolicyStateUnknown] = useState(false);
  const [reloadingPolicy, setReloadingPolicy] = useState(false);
  const [triageFeedbackByHost, setTriageFeedbackByHost] = useState<
    Record<string, HostTriageFeedback>
  >({});
  const [triageAnnouncement, setTriageAnnouncement] = useState<string | null>(
    null,
  );

  const policyRef = useRef<EgressPolicyResponse | null>(policy);
  policyRef.current = policy;
  const draftStateRef = useRef({
    mode: policyMode,
    allowed: allowedHosts,
    blocked: blockedHosts,
  });
  draftStateRef.current = {
    mode: policyMode,
    allowed: allowedHosts,
    blocked: blockedHosts,
  };
  const mutatingPolicyRef = useRef(false);
  const blockedSectionRef = useRef<HTMLDivElement | null>(null);

  // Egress audit log filter state.
  const [logFilter, setLogFilter] = useState<LogFilter>('all');
  const [refreshingLogs, setRefreshingLogs] = useState(false);
  const [logsErrorKey, setLogsErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );

  const resetSecretForm = useCallback(() => {
    setEditingSecret(null);
    setScope('bot');
    setSecretName('');
    setDomainMatch('');
    setMatchSubdomains(false);
    setPathPrefix('');
    setAuthScheme('bearer');
    setHeaderName('');
    setUsername('');
    setSecretValue('');
    setSecretErrorKey(null);
  }, []);

  const applyAuthoritativePolicy = useCallback(
    (
      nextPolicy: EgressPolicyResponse,
      options?: { preserveDraftIfDirty?: boolean },
    ) => {
      const currentSaved = policyRef.current;
      const currentDraft = draftStateRef.current;
      const dirty = isPolicyDraftDirty(
        currentSaved,
        currentDraft.mode,
        currentDraft.allowed,
        currentDraft.blocked,
      );

      policyRef.current = nextPolicy;
      setPolicy(nextPolicy);
      setPolicyStateUnknown(false);

      if (!dirty || !options?.preserveDraftIfDirty) {
        setPolicyMode(nextPolicy.policy);
        setAllowedHosts(nextPolicy.allowedHosts);
        setBlockedHosts(nextPolicy.blockedHosts ?? []);
        setDraftConflictPrompt(false);
      } else if (
        currentSaved &&
        currentSaved.revision !== nextPolicy.revision
      ) {
        setDraftConflictPrompt(true);
      }

      setTriageFeedbackByHost((prev) => {
        let changed = false;
        const next: Record<string, HostTriageFeedback> = {};
        for (const [host, fb] of Object.entries(prev)) {
          if (
            fb.status === 'pending' &&
            nextPolicy.enforcementStatus === 'applied' &&
            nextPolicy.acknowledgedRevision >= fb.revision
          ) {
            next[host] = { ...fb, status: 'applied' };
            changed = true;
          } else {
            next[host] = fb;
          }
        }
        return changed ? next : prev;
      });
    },
    [],
  );

  const loadAll = useCallback(async () => {
    setLoading(true);
    setLoadErrorKey(null);
    try {
      const [secretsResp, policyResp, logsResp] = await Promise.all([
        listBotSecrets(workspace.id, bot.id),
        getBotEgressPolicy(workspace.id, bot.id),
        listBotEgressLogs(workspace.id, bot.id, { limit: 50 }),
      ]);
      setSecrets(secretsResp.secrets);
      applyAuthoritativePolicy(policyResp, { preserveDraftIfDirty: false });
      setLogs(logsResp.logs);
    } catch (err) {
      setLoadErrorKey(resolveErrorKey(err));
    } finally {
      setLoading(false);
    }
  }, [applyAuthoritativePolicy, bot.id, workspace.id]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  useEffect(() => {
    if (policy?.enforcementStatus !== 'pending') {
      return;
    }
    let active = true;
    const timer = window.setTimeout(() => {
      void getBotEgressPolicy(workspace.id, bot.id)
        .then((latest) => {
          if (!active) {
            return;
          }
          applyAuthoritativePolicy(latest, { preserveDraftIfDirty: true });
          if (latest.enforcementStatus === 'applied') {
            setPolicyStatusNotice({ kind: 'applied' });
          }
        })
        .catch(() => {
          // Leave pending until next refresh or user reload.
        });
    }, 1000);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [
    applyAuthoritativePolicy,
    bot.id,
    policy?.enforcementStatus,
    policy?.revision,
    workspace.id,
  ]);

  const fetchFilteredLogs = useCallback(
    async (filter: LogFilter) => {
      setRefreshingLogs(true);
      setLogsErrorKey(null);
      try {
        const resp = await listBotEgressLogs(workspace.id, bot.id, {
          action: filter === 'all' ? undefined : filter,
          limit: 50,
        });
        setLogs(resp.logs);
      } catch (err) {
        setLogsErrorKey(resolveErrorKey(err));
      } finally {
        setRefreshingLogs(false);
      }
    },
    [bot.id, workspace.id],
  );

  function prepareClaudeSubscription() {
    const existing = secrets.find(
      (secret) =>
        secret.scope === 'bot' && secret.name === 'llm-anthropic-oauth',
    );
    if (existing) startEditSecret(existing);
    else resetSecretForm();
    setScope('bot');
    setSecretName('llm-anthropic-oauth');
    setDomainMatch('api.anthropic.com');
    setMatchSubdomains(false);
    setPathPrefix('');
    setAuthScheme('bearer');
    setHeaderName('Authorization');
    setUsername('');
    setSecretValue('');
    document
      .getElementById('secret-form')
      ?.scrollIntoView?.({ block: 'start' });
  }

  function canMutateSecret(secret: SecretMetadata): boolean {
    return secret.scope === 'workspace'
      ? canEditWorkspaceSecrets
      : canEditBotSecrets;
  }

  function startEditSecret(secret: SecretMetadata) {
    if (!canMutateSecret(secret)) {
      return;
    }
    setEditingSecret(secret);
    setScope(secret.scope);
    setSecretName(secret.name);
    setDomainMatch(secret.domainMatch);
    setMatchSubdomains(secret.matchSubdomains);
    setPathPrefix(secret.pathPrefix);
    setAuthScheme(secret.authScheme);
    setHeaderName(secret.headerName);
    setUsername(secret.username);
    // Keep secretValue empty so plaintext is never initialized or round-tripped.
    setSecretValue('');
    setSecretBannerKey(null);
    setSecretErrorKey(null);
    setConfirmDeleteId(null);
  }

  async function refreshSecretsAndPolicy() {
    const [secretsResp, policyResp] = await Promise.all([
      listBotSecrets(workspace.id, bot.id),
      getBotEgressPolicy(workspace.id, bot.id),
    ]);
    setSecrets(secretsResp.secrets);
    applyAuthoritativePolicy(policyResp, { preserveDraftIfDirty: true });
  }

  async function handleSecretSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (savingSecret) {
      return;
    }

    const activeScope = editingSecret ? editingSecret.scope : scope;
    const allowed =
      activeScope === 'workspace' ? canEditWorkspaceSecrets : canEditBotSecrets;
    if (!allowed) {
      return;
    }

    setSavingSecret(true);
    setSecretBannerKey(null);
    setSecretErrorKey(null);

    const commonFields = {
      name: secretName.trim(),
      domainMatch: domainMatch.trim().toLowerCase(),
      matchSubdomains,
      pathPrefix: pathPrefix.trim(),
      authScheme,
      headerName: authScheme === 'header' ? headerName.trim() : undefined,
      username: authScheme === 'basic' ? username.trim() : undefined,
    };

    try {
      if (editingSecret) {
        const updatePayload = {
          ...commonFields,
          value: secretValue.trim() ? secretValue : undefined,
        };
        if (editingSecret.scope === 'workspace') {
          await updateWorkspaceSecret(
            workspace.id,
            editingSecret.id,
            updatePayload,
          );
        } else {
          await updateBotSecret(
            workspace.id,
            bot.id,
            editingSecret.id,
            updatePayload,
          );
        }
      } else {
        const createPayload = {
          ...commonFields,
          value: secretValue,
        };
        if (activeScope === 'workspace') {
          await createWorkspaceSecret(workspace.id, createPayload);
        } else {
          await createBotSecret(workspace.id, bot.id, createPayload);
        }
      }

      await refreshSecretsAndPolicy();
      resetSecretForm();
      setSecretBannerKey('savedBanner');
    } catch (err) {
      setSecretErrorKey(resolveErrorKey(err));
    } finally {
      setSavingSecret(false);
    }
  }

  async function handleDeleteSecret(secret: SecretMetadata) {
    if (!canMutateSecret(secret) || deletingSecretId) {
      return;
    }
    if (confirmDeleteId !== secret.id) {
      setConfirmDeleteId(secret.id);
      return;
    }

    setDeletingSecretId(secret.id);
    setSecretBannerKey(null);
    setSecretErrorKey(null);
    try {
      if (secret.scope === 'workspace') {
        await deleteWorkspaceSecret(workspace.id, secret.id);
      } else {
        await deleteBotSecret(workspace.id, bot.id, secret.id);
      }
      if (editingSecret?.id === secret.id) {
        resetSecretForm();
      }
      setConfirmDeleteId(null);
      await refreshSecretsAndPolicy();
      setSecretBannerKey('deletedBanner');
    } catch (err) {
      setSecretErrorKey(resolveErrorKey(err));
    } finally {
      setDeletingSecretId(null);
    }
  }

  function handleAddAllowedHost() {
    if (!canEditBotSecrets) {
      return;
    }
    const normalized = normalizeHostEntry(hostDraft);
    if (!normalized) {
      return;
    }
    if (!allowedHosts.includes(normalized)) {
      setAllowedHosts((prev) => [...prev, normalized]);
    }
    setBlockedHosts((prev) =>
      prev.filter((item) => normalizeHostPattern(item) !== normalized),
    );
    setHostDraft('');
    setPolicyStatusNotice(null);
    setPolicyErrorKey(null);
  }

  function handleHostInputKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!event.nativeEvent.isComposing && event.key === 'Enter') {
      event.preventDefault();
      handleAddAllowedHost();
    }
  }

  function handleRemoveAllowedHost(host: string) {
    if (!canEditBotSecrets) {
      return;
    }
    setAllowedHosts((prev) => prev.filter((item) => item !== host));
    setPolicyStatusNotice(null);
  }

  function handleAddBlockedHost() {
    if (!canEditBotSecrets) {
      return;
    }
    const normalized = normalizeHostEntry(blockedHostDraft);
    if (!normalized) {
      return;
    }
    if (!blockedHosts.includes(normalized)) {
      setBlockedHosts((prev) => [...prev, normalized]);
    }
    setAllowedHosts((prev) =>
      prev.filter((item) => normalizeHostPattern(item) !== normalized),
    );
    setBlockedHostDraft('');
    setPolicyStatusNotice(null);
    setPolicyErrorKey(null);
  }

  function handleBlockedHostInputKeyDown(
    event: KeyboardEvent<HTMLInputElement>,
  ) {
    if (!event.nativeEvent.isComposing && event.key === 'Enter') {
      event.preventDefault();
      handleAddBlockedHost();
    }
  }

  function handleRemoveBlockedHost(host: string) {
    if (!canEditBotSecrets) {
      return;
    }
    setBlockedHosts((prev) => prev.filter((item) => item !== host));
    setPolicyStatusNotice(null);
  }

  function handleLoadSavedDraft() {
    if (!policy) {
      return;
    }
    setPolicyMode(policy.policy);
    setAllowedHosts(policy.allowedHosts);
    setBlockedHosts(policy.blockedHosts ?? []);
    setDraftConflictPrompt(false);
    setPolicyErrorKey(null);
  }

  async function handleReloadPolicy(): Promise<EgressPolicyResponse | null> {
    if (reloadingPolicy) {
      return null;
    }
    setReloadingPolicy(true);
    setPolicyErrorKey(null);
    try {
      const latest = await getBotEgressPolicy(workspace.id, bot.id);
      applyAuthoritativePolicy(latest, { preserveDraftIfDirty: true });
      setPolicyStatusNotice({
        kind: 'reconciled',
        revision: latest.revision,
      });
      return latest;
    } catch (err) {
      setPolicyStateUnknown(true);
      setPolicyErrorKey(resolveErrorKey(err));
      return null;
    } finally {
      setReloadingPolicy(false);
    }
  }

  async function reconcilePolicyFailure(err: unknown) {
    if (
      err instanceof ApiClientError &&
      err.status === 409 &&
      err.code === 'policy_conflict'
    ) {
      setPolicyErrorKey('policyConflict');
      try {
        const latest = await getBotEgressPolicy(workspace.id, bot.id);
        applyAuthoritativePolicy(latest, { preserveDraftIfDirty: true });
      } catch {
        setPolicyStateUnknown(true);
      }
      return;
    }

    if (isUncertainWriteError(err)) {
      try {
        const authoritative = await getBotEgressPolicy(workspace.id, bot.id);
        applyAuthoritativePolicy(authoritative, { preserveDraftIfDirty: true });
        setPolicyStatusNotice({
          kind: 'reconciled',
          revision: authoritative.revision,
        });
      } catch {
        setPolicyStateUnknown(true);
        setPolicyErrorKey(resolveErrorKey(err));
      }
      return;
    }

    setPolicyErrorKey(resolveErrorKey(err));
  }

  function noticeForResponse(
    updated: EgressPolicyResponse,
  ): PolicyStatusNotice {
    if (updated.enforcementStatus === 'pending') {
      return { kind: 'pending', revision: updated.revision };
    }
    if (updated.enforcementStatus === 'not_running') {
      return { kind: 'not_running', revision: updated.revision };
    }
    return { kind: 'applied' };
  }

  async function handleSavePolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const currentSaved = policyRef.current;
    if (
      !currentSaved ||
      !canEditBotSecrets ||
      mutatingPolicyRef.current ||
      policyStateUnknown
    ) {
      return;
    }

    mutatingPolicyRef.current = true;
    setPolicyMutationInFlight(true);
    setSavingPolicy(true);
    setPolicyStatusNotice(null);
    setPolicyErrorKey(null);

    try {
      const pendingAllowed = normalizeHostEntry(hostDraft);
      const pendingBlocked = normalizeHostEntry(blockedHostDraft);

      let nextAllowed = [...allowedHosts];
      let nextBlocked = [...blockedHosts];

      if (pendingAllowed && !nextAllowed.includes(pendingAllowed)) {
        nextAllowed = [...nextAllowed, pendingAllowed];
        nextBlocked = nextBlocked.filter(
          (item) => normalizeHostPattern(item) !== pendingAllowed,
        );
      }
      if (pendingBlocked && !nextBlocked.includes(pendingBlocked)) {
        nextBlocked = [...nextBlocked, pendingBlocked];
        nextAllowed = nextAllowed.filter(
          (item) => normalizeHostPattern(item) !== pendingBlocked,
        );
      }

      const updated = await updateBotEgressPolicy(workspace.id, bot.id, {
        policy: policyMode,
        allowedHosts: nextAllowed,
        blockedHosts: nextBlocked,
        expectedRevision: currentSaved.revision,
      });
      applyAuthoritativePolicy(updated, { preserveDraftIfDirty: false });
      setHostDraft('');
      setBlockedHostDraft('');
      setPolicyStatusNotice(noticeForResponse(updated));
    } catch (err) {
      await reconcilePolicyFailure(err);
    } finally {
      mutatingPolicyRef.current = false;
      setPolicyMutationInFlight(false);
      setSavingPolicy(false);
    }
  }

  async function handleTriageHost(rawHost: string, op: 'allow' | 'block') {
    const currentSaved = policyRef.current;
    const normHost = normalizeHostPattern(rawHost);
    if (
      !currentSaved ||
      !normHost ||
      !canEditBotSecrets ||
      mutatingPolicyRef.current ||
      policyStateUnknown
    ) {
      return;
    }

    mutatingPolicyRef.current = true;
    setPolicyMutationInFlight(true);
    setTriagePendingHost(normHost);
    setPolicyErrorKey(null);
    setTriageAnnouncement(null);

    const mutation = computeTriageMutation(currentSaved, normHost, op);
    try {
      const updated = await updateBotEgressPolicy(workspace.id, bot.id, {
        policy: currentSaved.policy,
        allowedHosts: mutation.allowedHosts,
        blockedHosts: mutation.blockedHosts,
        expectedRevision: currentSaved.revision,
      });
      applyAuthoritativePolicy(updated, { preserveDraftIfDirty: true });
      const feedback: HostTriageFeedback = {
        host: normHost,
        op,
        status: updated.enforcementStatus,
        revision: updated.revision,
        undo: { ...mutation.undo, appliedRevision: updated.revision },
      };
      setTriageFeedbackByHost((prev) => ({ ...prev, [normHost]: feedback }));
      const notice = noticeForResponse(updated);
      setPolicyStatusNotice(notice);
      setTriageAnnouncement(
        updated.enforcementStatus === 'pending'
          ? `${normHost}: ${t('triagePendingEnforcement')}`
          : op === 'allow'
            ? `${normHost}: ${t('triageAllowedFeedback')}`
            : `${normHost}: ${t('triageBlockedFeedback')}`,
      );
    } catch (err) {
      await reconcilePolicyFailure(err);
    } finally {
      mutatingPolicyRef.current = false;
      setPolicyMutationInFlight(false);
      setTriagePendingHost(null);
    }
  }

  async function handleUndoTriage(rawHost: string) {
    const currentSaved = policyRef.current;
    const normHost = normalizeHostPattern(rawHost);
    const fb = triageFeedbackByHost[normHost];
    if (
      !currentSaved ||
      !fb ||
      !canEditBotSecrets ||
      mutatingPolicyRef.current ||
      policyStateUnknown
    ) {
      return;
    }

    const firstAttempt = computeUndoMutation(currentSaved, fb.undo);
    if (!firstAttempt) {
      setTriageFeedbackByHost((prev) => {
        const copy = { ...prev };
        delete copy[normHost];
        return copy;
      });
      setPolicyStatusNotice({ kind: 'undo_conflict', host: normHost });
      return;
    }

    mutatingPolicyRef.current = true;
    setPolicyMutationInFlight(true);
    setTriagePendingHost(normHost);
    setPolicyErrorKey(null);

    try {
      let updated: EgressPolicyResponse;
      try {
        updated = await updateBotEgressPolicy(workspace.id, bot.id, {
          policy: currentSaved.policy,
          allowedHosts: firstAttempt.allowedHosts,
          blockedHosts: firstAttempt.blockedHosts,
          expectedRevision: currentSaved.revision,
        });
      } catch (err) {
        if (
          err instanceof ApiClientError &&
          err.status === 409 &&
          err.code === 'policy_conflict'
        ) {
          const latest = await getBotEgressPolicy(workspace.id, bot.id);
          applyAuthoritativePolicy(latest, { preserveDraftIfDirty: true });
          const retryMutation = computeUndoMutation(latest, fb.undo);
          if (!retryMutation) {
            setTriageFeedbackByHost((prev) => {
              const copy = { ...prev };
              delete copy[normHost];
              return copy;
            });
            setPolicyStatusNotice({ kind: 'undo_conflict', host: normHost });
            return;
          }
          updated = await updateBotEgressPolicy(workspace.id, bot.id, {
            policy: latest.policy,
            allowedHosts: retryMutation.allowedHosts,
            blockedHosts: retryMutation.blockedHosts,
            expectedRevision: latest.revision,
          });
        } else {
          throw err;
        }
      }

      applyAuthoritativePolicy(updated, { preserveDraftIfDirty: true });
      setTriageFeedbackByHost((prev) => {
        const copy = { ...prev };
        delete copy[normHost];
        return copy;
      });
      if (updated.enforcementStatus === 'pending') {
        setPolicyStatusNotice({ kind: 'pending', revision: updated.revision });
      } else {
        setPolicyStatusNotice({ kind: 'undo_complete', host: normHost });
      }
      setTriageAnnouncement(null);
    } catch (err) {
      await reconcilePolicyFailure(err);
    } finally {
      mutatingPolicyRef.current = false;
      setPolicyMutationInFlight(false);
      setTriagePendingHost(null);
    }
  }

  function handleJumpToBlockedRule() {
    blockedSectionRef.current?.scrollIntoView?.({
      behavior: 'smooth',
      block: 'center',
    });
    blockedSectionRef.current?.querySelector('input')?.focus();
  }

  function handleSelectLogFilter(nextFilter: LogFilter) {
    setLogFilter(nextFilter);
    void fetchFilteredLogs(nextFilter);
  }

  function renderInjectionLabel(secret: SecretMetadata): string {
    if (secret.authScheme === 'header') {
      return t('injectionHeader', { headerName: secret.headerName });
    }
    if (secret.authScheme === 'basic') {
      return secret.username
        ? t('injectionBasic', { username: secret.username })
        : t('injectionBasicAnon');
    }
    return t('injectionBearer');
  }

  const effectiveScope: SecretScope = editingSecret
    ? editingSecret.scope
    : scope;
  const formEditable =
    effectiveScope === 'workspace'
      ? canEditWorkspaceSecrets
      : canEditBotSecrets;
  const effectiveSecretHosts = policy?.effectiveSecretHosts ?? [];

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <Link
          href={`/bots/${bot.id}`}
          className="inline-flex min-h-[44px] w-fit items-center gap-2 rounded-sm px-2 text-[14px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <ArrowLeft className="h-4 w-4" aria-hidden="true" />
          <span>{t('backToWorkspace')}</span>
        </Link>

        <Eyebrow>{t('eyebrow')}</Eyebrow>
        <h1 className="text-[34px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[42px]">
          {t('title', { name: bot.name })}
        </h1>
        <p className="max-w-3xl text-[16px] leading-[1.6] text-ink-2">
          {t('lead')}
        </p>
        <p className="max-w-3xl text-[16px] leading-[1.6] text-ink-2">
          {t('protectedPushNote')}
        </p>

        <nav
          aria-label={tTabs('ariaLabel')}
          className="mt-1 flex flex-wrap gap-1 border-b border-line"
        >
          <Link
            href={`/bots/${bot.id}/settings`}
            className="inline-flex min-h-[44px] items-center px-3.5 text-[14px] font-bold text-ink-2 transition duration-150 hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {tTabs('general')}
          </Link>
          <Link
            href={`/bots/${bot.id}/secrets`}
            aria-current="page"
            className="inline-flex min-h-[44px] items-center px-3.5 text-[14px] font-bold text-green-600 shadow-[inset_0_-2px_0_#3f9147] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {tTabs('secrets')}
          </Link>
        </nav>
      </div>

      {bot.llmMode === 'cli' &&
      ['anthropic', 'claude'].includes(bot.llmProvider) ? (
        <Card className="flex flex-col gap-3 p-6">
          <h2 className="text-[20px] font-black text-ink">
            {t('claudeSubscriptionTitle')}
          </h2>
          <p className="text-[14px] text-ink-2">
            {t('claudeSubscriptionSetup')}
          </p>
          <code className="w-fit rounded-xs bg-paper-2 px-3 py-2 text-[13px] text-ink">
            {t('claudeSubscriptionCommand')}
          </code>
          <p className="text-[14px] text-ink-2">
            {t('claudeSubscriptionDockerSetup')}
          </p>
          <code className="w-fit rounded-xs bg-paper-2 px-3 py-2 text-[13px] text-ink">
            {t('claudeSubscriptionDockerCommand')}
          </code>
          <p className="text-[14px] text-ink-2">
            {t('claudeSubscriptionPrivacy')}
          </p>
          {canEditBotSecrets ? (
            <Button
              variant="secondary"
              disabled={loading || savingSecret}
              onClick={prepareClaudeSubscription}
            >
              {t('claudeSubscriptionConfigure')}
            </Button>
          ) : null}
        </Card>
      ) : null}

      {!canEditBotSecrets ? (
        <Alert tone="info">{t('readOnlyNotice')}</Alert>
      ) : null}

      {loadErrorKey ? (
        <Alert tone="warning">{tErrors(loadErrorKey)}</Alert>
      ) : null}

      {loading ? (
        <Card className="p-8 text-center">
          <p role="status" className="text-[15px] font-bold text-ink-2">
            {tActions('loading')}
          </p>
        </Card>
      ) : (
        <>
          <section
            aria-labelledby="how-secrets-heading"
            className="flex flex-col gap-3 rounded-lg border border-line bg-paper p-5 shadow-card"
          >
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h2
                id="how-secrets-heading"
                className="text-[20px] font-black tracking-[-0.01em] text-ink"
              >
                {t('howTitle')}
              </h2>
              <p className="text-[13px] text-muted">{t('howSubtitle')}</p>
            </div>
            <ol className="grid items-stretch gap-3 lg:grid-cols-[1fr_auto_1fr_auto_1fr]">
              {HOW_IT_WORKS_STEPS.map((step, index) => {
                const Icon = step.icon;
                const isFirst = index === 0;
                const isMiddle = index === 1;
                return (
                  <li key={step.id} className="contents">
                    <div
                      className={`flex flex-col justify-between gap-2 rounded-md border p-4 ${
                        isFirst
                          ? 'border-dark bg-terminal text-on-dark'
                          : isMiddle
                            ? 'border-mint-line bg-mint-soft text-ink'
                            : 'border-line bg-paper-2 text-ink'
                      }`}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span
                          className={`rounded-xs px-2 py-0.5 font-mono text-[11px] font-bold uppercase ${
                            isFirst
                              ? 'bg-dark-2 text-green-light'
                              : isMiddle
                                ? 'border border-mint-line bg-paper text-green-600'
                                : 'border border-line bg-paper text-ink-2'
                          }`}
                        >
                          {t(step.numKey)}
                        </span>
                        <Icon
                          className={`h-4 w-4 ${
                            isFirst ? 'text-green-light' : 'text-green-600'
                          }`}
                          aria-hidden="true"
                        />
                      </div>
                      <h3
                        className={`text-[15px] font-black ${
                          isFirst ? 'text-paper' : 'text-ink'
                        }`}
                      >
                        {t(step.titleKey)}
                      </h3>
                      <p
                        className={`text-[13px] leading-[1.5] ${
                          isFirst ? 'text-on-dark-muted' : 'text-ink-2'
                        }`}
                      >
                        {t(step.descKey)}
                      </p>
                      {index === 2 ? (
                        <div className="mt-1 flex flex-wrap gap-1.5 font-mono text-[11px]">
                          <span className="rounded-xs border border-mint-line bg-mint-soft px-2 py-0.5 font-bold text-green-600">
                            {t('howAllowBadge')}
                          </span>
                          <span className="rounded-xs border border-dashed border-amber-line bg-amber-tint px-2 py-0.5 font-bold text-amber">
                            {t('howBlockBadge')}
                          </span>
                        </div>
                      ) : null}
                    </div>
                    {index < 2 ? (
                      <div
                        aria-hidden="true"
                        className="hidden items-center justify-center text-green-600 lg:flex"
                      >
                        <ArrowRight className="h-5 w-5" />
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ol>
          </section>

          <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_340px] xl:items-start">
            <section
              aria-labelledby="configured-secrets-heading"
              className="flex min-w-0 flex-col gap-4"
            >
              <div className="flex flex-col gap-1">
                <h2
                  id="configured-secrets-heading"
                  className="text-[22px] font-black tracking-[-0.01em] text-ink"
                >
                  {t('sectionTitle')}
                </h2>
                <p className="text-[15px] leading-[1.55] text-ink-2">
                  {t('sectionLead')}
                </p>
              </div>

              {secretBannerKey ? (
                <div role="status">
                  <Alert tone="success">{t(secretBannerKey)}</Alert>
                </div>
              ) : null}

              {secretErrorKey ? (
                <Alert tone="warning">{tErrors(secretErrorKey)}</Alert>
              ) : null}

              <Card className="overflow-hidden p-0">
                {secrets.length === 0 ? (
                  <p className="p-6 text-[15px] text-ink-2">
                    {t('emptySecrets')}
                  </p>
                ) : (
                  <div
                    tabIndex={0}
                    role="region"
                    aria-labelledby="configured-secrets-heading"
                    className="overflow-x-auto"
                  >
                    <table
                      aria-label={t('tableAria')}
                      className="w-full min-w-[720px] border-collapse text-left text-[14px]"
                    >
                      <thead className="border-b border-line bg-paper-2 text-[12px] font-bold uppercase tracking-[0.04em] text-muted">
                        <tr>
                          <th scope="col" className="px-4 py-3">
                            {t('colName')}
                          </th>
                          <th scope="col" className="px-4 py-3">
                            {t('colDestination')}
                          </th>
                          <th scope="col" className="px-4 py-3">
                            {t('colInjection')}
                          </th>
                          <th scope="col" className="px-4 py-3">
                            {t('colPreview')}
                          </th>
                          <th scope="col" className="px-4 py-3">
                            {t('colUsage')}
                          </th>
                          <th scope="col" className="px-4 py-3 text-right">
                            {t('colActions')}
                          </th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-line">
                        {secrets.map((secret) => {
                          const editable = canMutateSecret(secret);
                          const isConfirmingDelete =
                            confirmDeleteId === secret.id;
                          const deleteLabel = isConfirmingDelete
                            ? t('deleteConfirmAction')
                            : t('deleteAction');
                          return (
                            <tr
                              key={secret.id}
                              className="bg-paper transition-colors hover:bg-paper-2/50"
                            >
                              <th scope="row" className="px-4 py-3 align-top">
                                <div className="flex flex-col items-start gap-1">
                                  <span className="font-mono text-[13px] font-bold text-ink">
                                    {secret.name}
                                  </span>
                                  <span
                                    className={`rounded-xs border px-2 py-0.5 text-[11px] font-bold ${
                                      secret.scope === 'workspace'
                                        ? 'border-blue-line bg-blue-tint text-blue'
                                        : 'border-mint-line bg-mint-soft text-green-600'
                                    }`}
                                  >
                                    {secret.scope === 'workspace'
                                      ? t('scopeWorkspaceBadge')
                                      : t('scopeBotBadge')}
                                  </span>
                                </div>
                              </th>
                              <td className="px-4 py-3 align-top">
                                <div className="flex flex-col items-start gap-1">
                                  <span className="font-mono text-[13px] text-ink">
                                    {formatDestinationTarget(secret)}
                                  </span>
                                  <div className="flex flex-wrap gap-1.5">
                                    {!secret.pathPrefix ? (
                                      <span className="rounded-xs border border-line bg-paper-2 px-1.5 py-0.5 text-[11px] text-ink-2">
                                        {t('allPaths')}
                                      </span>
                                    ) : null}
                                    {secret.matchSubdomains ? (
                                      <span className="rounded-xs border border-line bg-paper-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-2">
                                        {t('subdomainsTag')}
                                      </span>
                                    ) : null}
                                  </div>
                                </div>
                              </td>
                              <td className="px-4 py-3 align-top font-mono text-[13px] text-ink-2">
                                {renderInjectionLabel(secret)}
                              </td>
                              <td className="px-4 py-3 align-top font-mono text-[13px] text-ink-2">
                                {secret.maskedPreview}
                              </td>
                              <td className="px-4 py-3 align-top text-[13px] text-ink-2">
                                {secret.usedCount === 0
                                  ? t('neverUsed')
                                  : t('usageCountLabel', {
                                      count: secret.usedCount,
                                    })}
                              </td>
                              <td className="px-4 py-3 align-top">
                                <div className="flex justify-end gap-2">
                                  <Button
                                    type="button"
                                    variant="secondary"
                                    disabled={!editable || savingSecret}
                                    aria-label={`${t('editAction')} ${secret.name}`}
                                    className="px-3 text-[13px]"
                                    onClick={() => startEditSecret(secret)}
                                  >
                                    <Pencil
                                      className="h-3.5 w-3.5"
                                      aria-hidden="true"
                                    />
                                    <span>{t('editAction')}</span>
                                  </Button>
                                  <Button
                                    type="button"
                                    variant="secondary"
                                    disabled={
                                      !editable ||
                                      deletingSecretId === secret.id
                                    }
                                    aria-label={`${deleteLabel} ${secret.name}`}
                                    className={`px-3 text-[13px] ${
                                      isConfirmingDelete
                                        ? 'border-dashed border-amber-line bg-amber-tint text-amber hover:border-amber-line hover:bg-amber-tint'
                                        : ''
                                    }`.trim()}
                                    onClick={() =>
                                      void handleDeleteSecret(secret)
                                    }
                                  >
                                    <Trash2
                                      className="h-3.5 w-3.5"
                                      aria-hidden="true"
                                    />
                                    <span>{deleteLabel}</span>
                                  </Button>
                                </div>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                )}
              </Card>

              <Card className="p-6">
                <form
                  id="secret-form"
                  aria-labelledby="secret-form-heading"
                  onSubmit={(event) => void handleSecretSubmit(event)}
                  className="flex flex-col gap-5"
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <h3
                      id="secret-form-heading"
                      className="text-[18px] font-black text-ink"
                    >
                      {editingSecret
                        ? t('editFormTitle', { name: editingSecret.name })
                        : t('addFormTitle')}
                    </h3>
                    {editingSecret ? (
                      <Button
                        type="button"
                        variant="secondary"
                        onClick={resetSecretForm}
                      >
                        {t('cancelEdit')}
                      </Button>
                    ) : null}
                  </div>

                  <RadioCardGroup<SecretScope>
                    legend={t('scopeLabel')}
                    name="secret-scope"
                    value={effectiveScope}
                    onChange={setScope}
                    disabled={!canEditBotSecrets || editingSecret !== null}
                    options={[
                      { value: 'bot', title: t('scopeBot') },
                      {
                        value: 'workspace',
                        title: t('scopeWorkspace'),
                        description: !canEditWorkspaceSecrets
                          ? t('workspaceScopeAdminOnly')
                          : undefined,
                        disabled: !canEditWorkspaceSecrets,
                      },
                    ]}
                  />

                  <div className="grid gap-4 sm:grid-cols-2">
                    <Input
                      name="secretName"
                      label={t('nameLabel')}
                      placeholder={t('namePlaceholder')}
                      mono
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={secretName}
                      disabled={!formEditable || savingSecret}
                      required
                      onChange={(event) => setSecretName(event.target.value)}
                    />
                    <Input
                      name="domainMatch"
                      label={t('domainLabel')}
                      placeholder={t('domainPlaceholder')}
                      mono
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={domainMatch}
                      disabled={!formEditable || savingSecret}
                      required
                      onChange={(event) => setDomainMatch(event.target.value)}
                    />
                  </div>

                  <div className="grid gap-4 sm:grid-cols-2">
                    <Input
                      name="pathPrefix"
                      label={t('pathPrefixLabel')}
                      placeholder={t('pathPrefixPlaceholder')}
                      mono
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={pathPrefix}
                      disabled={!formEditable || savingSecret}
                      onChange={(event) => setPathPrefix(event.target.value)}
                    />
                    <div className="flex items-end">
                      <div className="flex min-h-[44px] w-full items-center gap-3 rounded-sm border border-line bg-paper-2 px-3.5 py-2">
                        <input
                          id={subdomainsCheckboxId}
                          name="matchSubdomains"
                          type="checkbox"
                          checked={matchSubdomains}
                          disabled={!formEditable || savingSecret}
                          onChange={(event) =>
                            setMatchSubdomains(event.target.checked)
                          }
                          className="h-4 w-4 shrink-0 accent-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                        />
                        <label
                          htmlFor={subdomainsCheckboxId}
                          className="cursor-pointer text-[14px] font-bold text-ink"
                        >
                          {t('subdomainsLabel')}
                        </label>
                      </div>
                    </div>
                  </div>

                  <div className="grid gap-4 sm:grid-cols-2">
                    <Select
                      name="authScheme"
                      label={t('schemeLabel')}
                      value={authScheme}
                      disabled={!formEditable || savingSecret}
                      onChange={(event) =>
                        setAuthScheme(event.target.value as SecretAuthScheme)
                      }
                    >
                      <option value="bearer">{t('schemeBearer')}</option>
                      <option value="header">{t('schemeHeader')}</option>
                      <option value="basic">{t('schemeBasic')}</option>
                    </Select>

                    {authScheme === 'header' ? (
                      <Input
                        name="headerName"
                        label={t('headerNameLabel')}
                        placeholder={t('headerNamePlaceholder')}
                        mono
                        autoCorrect="off"
                        autoCapitalize="none"
                        spellCheck={false}
                        value={headerName}
                        disabled={!formEditable || savingSecret}
                        required
                        onChange={(event) => setHeaderName(event.target.value)}
                      />
                    ) : null}

                    {authScheme === 'basic' ? (
                      <Input
                        name="username"
                        label={t('usernameLabel')}
                        placeholder={t('usernamePlaceholder')}
                        mono
                        autoCorrect="off"
                        autoCapitalize="none"
                        spellCheck={false}
                        value={username}
                        disabled={!formEditable || savingSecret}
                        onChange={(event) => setUsername(event.target.value)}
                      />
                    ) : null}
                  </div>

                  <Input
                    name="secretValue"
                    type="password"
                    autoComplete="new-password"
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    label={
                      editingSecret
                        ? t('valueReplaceLabel')
                        : t('valueCreateLabel')
                    }
                    placeholder={t('valuePlaceholder')}
                    mono
                    value={secretValue}
                    disabled={!formEditable || savingSecret}
                    required={editingSecret === null}
                    onChange={(event) => setSecretValue(event.target.value)}
                  />

                  {formEditable ? (
                    <div className="flex justify-end border-t border-line pt-4">
                      <Button type="submit" arrow disabled={savingSecret}>
                        {savingSecret
                          ? t('savingSecret')
                          : editingSecret
                            ? t('updateSubmit')
                            : t('createSubmit')}
                      </Button>
                    </div>
                  ) : null}
                </form>
              </Card>
            </section>

            <section
              aria-labelledby="outbound-policy-heading"
              className="flex flex-col gap-4"
            >
              <div className="flex flex-col gap-1">
                <h2
                  id="outbound-policy-heading"
                  className="text-[22px] font-black tracking-[-0.01em] text-ink"
                >
                  {t('policyTitle')}
                </h2>
                <p className="text-[15px] leading-[1.55] text-ink-2">
                  {t('policyLead')}
                </p>
              </div>

              {policyStateUnknown ? (
                <div role="alert" className="flex flex-col gap-3">
                  <Alert tone="warning">
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <span>{t('policyStateUnknownBanner')}</span>
                      <Button
                        type="button"
                        variant="secondary"
                        disabled={reloadingPolicy}
                        onClick={() => void handleReloadPolicy()}
                      >
                        <RefreshCw className="h-4 w-4" aria-hidden="true" />
                        <span>{t('policyReloadAction')}</span>
                      </Button>
                    </div>
                  </Alert>
                </div>
              ) : null}

              {policyStatusNotice ? (
                <div role="status">
                  <Alert
                    tone={
                      policyStatusNotice.kind === 'pending' ||
                      policyStatusNotice.kind === 'reconciled' ||
                      policyStatusNotice.kind === 'undo_conflict'
                        ? 'warning'
                        : 'success'
                    }
                  >
                    {policyStatusNotice.kind === 'applied'
                      ? t('policySavedBanner')
                      : policyStatusNotice.kind === 'pending'
                        ? t('policyPendingBanner', {
                            revision: policyStatusNotice.revision,
                          })
                        : policyStatusNotice.kind === 'not_running'
                          ? t('policyNotRunningBanner', {
                              revision: policyStatusNotice.revision,
                            })
                          : policyStatusNotice.kind === 'reconciled'
                            ? t('policyReconciledNotice', {
                                revision: policyStatusNotice.revision,
                              })
                            : policyStatusNotice.kind === 'undo_complete'
                              ? t('triageUndoComplete', {
                                  host: policyStatusNotice.host,
                                })
                              : t('triageUndoConflict', {
                                  host: policyStatusNotice.host,
                                })}
                  </Alert>
                </div>
              ) : null}

              {policyErrorKey ? (
                <div role="alert">
                  <Alert tone="warning">{tErrors(policyErrorKey)}</Alert>
                </div>
              ) : null}

              {draftConflictPrompt && policy ? (
                <div role="status">
                  <Alert tone="warning">
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <span>
                        {t('policyDraftConflictBanner', {
                          revision: policy.revision,
                        })}
                      </span>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          type="button"
                          variant="secondary"
                          onClick={() => setDraftConflictPrompt(false)}
                        >
                          {t('policyDraftKeep')}
                        </Button>
                        <Button
                          type="button"
                          variant="secondary"
                          onClick={handleLoadSavedDraft}
                        >
                          {t('policyDraftRebase')}
                        </Button>
                      </div>
                    </div>
                  </Alert>
                </div>
              ) : null}

              <Card className="p-6">
                <form
                  aria-labelledby="outbound-policy-heading"
                  onSubmit={(event) => void handleSavePolicy(event)}
                  className="flex flex-col gap-6"
                >
                  <RadioCardGroup<EgressPolicyMode>
                    legend={t('policyTitle')}
                    name="egress-policy-mode"
                    value={policyMode}
                    onChange={(nextMode) => {
                      setPolicyMode(nextMode);
                      setPolicyStatusNotice(null);
                    }}
                    disabled={
                      !canEditBotSecrets ||
                      policyMutationInFlight ||
                      policyStateUnknown
                    }
                    options={[
                      {
                        value: 'allowlist',
                        title: t('policyAllowlistTitle'),
                        description: t('policyAllowlistDesc'),
                      },
                      {
                        value: 'open',
                        title: t('policyOpenTitle'),
                        description: t('policyOpenDesc'),
                      },
                    ]}
                  />

                  <div className="flex flex-col gap-3">
                    <h3 className="text-[15px] font-bold text-ink">
                      {t('allowedHostsTitle')}
                    </h3>

                    <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
                      <div className="flex-1">
                        <Input
                          name="allowedHostDraft"
                          label={t('hostInputLabel')}
                          placeholder={t('hostInputPlaceholder')}
                          mono
                          autoCorrect="off"
                          autoCapitalize="none"
                          spellCheck={false}
                          value={hostDraft}
                          disabled={
                            !canEditBotSecrets ||
                            policyMutationInFlight ||
                            policyStateUnknown
                          }
                          onKeyDown={handleHostInputKeyDown}
                          onChange={(event) => setHostDraft(event.target.value)}
                        />
                      </div>
                      <Button
                        type="button"
                        variant="secondary"
                        disabled={
                          !canEditBotSecrets ||
                          policyMutationInFlight ||
                          policyStateUnknown ||
                          !hostDraft.trim()
                        }
                        onClick={handleAddAllowedHost}
                      >
                        <Plus className="h-4 w-4" aria-hidden="true" />
                        <span>{t('addHostAction')}</span>
                      </Button>
                    </div>

                    {allowedHosts.length === 0 ? (
                      <p className="text-[14px] text-muted">
                        {t('emptyHosts')}
                      </p>
                    ) : (
                      <ul className="flex flex-wrap gap-2">
                        {allowedHosts.map((host) => (
                          <li
                            key={host}
                            className="inline-flex min-h-[44px] items-center gap-1 rounded-sm border border-line bg-paper-2 pl-3 pr-0.5 font-mono text-[13px] text-ink"
                          >
                            <span>{host}</span>
                            {canEditBotSecrets ? (
                              <button
                                type="button"
                                aria-label={t('removeHostAria', { host })}
                                disabled={
                                  policyMutationInFlight || policyStateUnknown
                                }
                                onClick={() => handleRemoveAllowedHost(host)}
                                className="inline-flex h-11 w-11 items-center justify-center rounded-xs text-ink-2 transition hover:bg-mint-soft hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                              >
                                <X className="h-3.5 w-3.5" aria-hidden="true" />
                              </button>
                            ) : null}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>

                  <div
                    ref={blockedSectionRef}
                    id="blocked-hosts-section"
                    className="flex flex-col gap-3 border-t border-line pt-4"
                  >
                    <div className="flex flex-col gap-1">
                      <h3 className="text-[15px] font-bold text-ink">
                        {t('blockedHostsTitle')}
                      </h3>
                      <p className="text-[13px] leading-[1.45] text-muted">
                        {t('blockedHostsDesc')}
                      </p>
                    </div>

                    <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
                      <div className="flex-1">
                        <Input
                          name="blockedHostDraft"
                          label={t('blockedHostInputLabel')}
                          placeholder={t('blockedHostInputPlaceholder')}
                          mono
                          autoCorrect="off"
                          autoCapitalize="none"
                          spellCheck={false}
                          value={blockedHostDraft}
                          disabled={
                            !canEditBotSecrets ||
                            policyMutationInFlight ||
                            policyStateUnknown
                          }
                          onKeyDown={handleBlockedHostInputKeyDown}
                          onChange={(event) =>
                            setBlockedHostDraft(event.target.value)
                          }
                        />
                      </div>
                      <Button
                        type="button"
                        variant="secondary"
                        disabled={
                          !canEditBotSecrets ||
                          policyMutationInFlight ||
                          policyStateUnknown ||
                          !blockedHostDraft.trim()
                        }
                        onClick={handleAddBlockedHost}
                      >
                        <Ban className="h-4 w-4" aria-hidden="true" />
                        <span>{t('blockHostAction')}</span>
                      </Button>
                    </div>

                    {blockedHosts.length === 0 ? (
                      <p className="text-[14px] text-muted">
                        {t('emptyBlockedHosts')}
                      </p>
                    ) : (
                      <ul className="flex flex-wrap gap-2">
                        {blockedHosts.map((host) => (
                          <li
                            key={host}
                            className="inline-flex min-h-[44px] items-center gap-1 rounded-sm border border-dashed border-amber-line bg-amber-tint pl-3 pr-0.5 font-mono text-[13px] font-bold text-amber"
                          >
                            <span>{host}</span>
                            {canEditBotSecrets ? (
                              <button
                                type="button"
                                aria-label={t('removeBlockedHostAria', {
                                  host,
                                })}
                                disabled={
                                  policyMutationInFlight || policyStateUnknown
                                }
                                onClick={() => handleRemoveBlockedHost(host)}
                                className="inline-flex h-11 w-11 items-center justify-center rounded-xs text-amber transition hover:bg-paper/80 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                              >
                                <X className="h-3.5 w-3.5" aria-hidden="true" />
                              </button>
                            ) : null}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>

                  <div className="flex flex-col gap-2 border-t border-line pt-4">
                    <h3 className="text-[14px] font-bold text-ink">
                      {t('effectiveSecretHostsTitle')}
                    </h3>
                    {effectiveSecretHosts.length === 0 ? (
                      <p className="text-[14px] text-muted">
                        {t('effectiveSecretHostsEmpty')}
                      </p>
                    ) : (
                      <ul className="flex flex-wrap gap-2">
                        {effectiveSecretHosts.map((host) => (
                          <li
                            key={host}
                            className="rounded-xs border border-mint-line bg-mint-soft px-2.5 py-1 font-mono text-[13px] font-bold text-green-600"
                          >
                            {host}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>

                  {canEditBotSecrets ? (
                    <div className="flex justify-end border-t border-line pt-4">
                      <Button
                        type="submit"
                        arrow
                        disabled={
                          savingPolicy ||
                          policyMutationInFlight ||
                          policyStateUnknown
                        }
                      >
                        {savingPolicy
                          ? t('savingPolicy')
                          : t('savePolicyAction')}
                      </Button>
                    </div>
                  ) : null}
                </form>
              </Card>
            </section>
          </div>

          <section
            aria-labelledby="egress-activity-heading"
            className="flex flex-col gap-4"
          >
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div className="flex flex-col gap-1">
                <h2
                  id="egress-activity-heading"
                  className="text-[22px] font-black tracking-[-0.01em] text-ink"
                >
                  {t('egressTitle')}
                </h2>
                <p className="text-[15px] leading-[1.55] text-ink-2">
                  {t('egressLead')}
                </p>
                <p className="text-[13px] font-bold text-green-600">
                  {t('triageSyncNote')}
                </p>
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <div
                  role="group"
                  aria-label={t('filterAria')}
                  className="flex flex-wrap gap-1.5"
                >
                  {LOG_FILTER_OPTIONS.map((opt) => {
                    const active = logFilter === opt.value;
                    return (
                      <button
                        key={opt.value}
                        type="button"
                        aria-pressed={active}
                        onClick={() => handleSelectLogFilter(opt.value)}
                        className={`inline-flex min-h-[44px] items-center rounded-sm border px-3.5 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                          active
                            ? 'border-dark bg-dark text-paper'
                            : 'border-line bg-paper text-ink-2 hover:border-green-light hover:bg-mint-soft hover:text-green-600'
                        }`}
                      >
                        {t(opt.labelKey)}
                      </button>
                    );
                  })}
                </div>

                <Button
                  type="button"
                  variant="secondary"
                  disabled={refreshingLogs}
                  onClick={() => void fetchFilteredLogs(logFilter)}
                >
                  <RefreshCw className="h-4 w-4" aria-hidden="true" />
                  <span>{t('refreshLogs')}</span>
                </Button>
              </div>
            </div>

            {triageAnnouncement ? (
              <div role="status" aria-live="polite" className="sr-only">
                {triageAnnouncement}
              </div>
            ) : null}

            {logsErrorKey ? (
              <Alert tone="warning">{tErrors(logsErrorKey)}</Alert>
            ) : null}

            <div className="rounded-lg border border-dark-2 bg-terminal p-4 text-on-dark shadow-card">
              {logs.length === 0 ? (
                <p className="py-6 text-center font-mono text-[13px] text-on-dark-muted">
                  {t('emptyLogs')}
                </p>
              ) : (
                <div
                  tabIndex={0}
                  role="region"
                  aria-labelledby="egress-activity-heading"
                  className="overflow-x-auto"
                >
                  <table className="w-full min-w-[840px] border-collapse text-left text-[13px]">
                    <thead className="border-b border-on-dark-faint/20 text-[12px] font-bold uppercase tracking-[0.04em] text-on-dark-muted">
                      <tr>
                        <th scope="col" className="px-3 py-2.5">
                          {t('logColOutcome')}
                        </th>
                        <th scope="col" className="px-3 py-2.5">
                          {t('logColDestination')}
                        </th>
                        <th scope="col" className="px-3 py-2.5">
                          {t('logColSecret')}
                        </th>
                        <th scope="col" className="px-3 py-2.5">
                          {t('logColMetrics')}
                        </th>
                        <th scope="col" className="px-3 py-2.5 text-right">
                          {t('logColTime')}
                        </th>
                        <th scope="col" className="px-3 py-2.5 text-right">
                          {t('logColAction')}
                        </th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-on-dark-faint/15 font-mono">
                      {logs.map((entry) => {
                        const badge = LOG_BADGE_STYLES[entry.action];
                        const BadgeIcon =
                          entry.action === 'blocked_host'
                            ? ShieldAlert
                            : entry.action === 'mitm_token_added'
                              ? CheckCircle2
                              : Globe;
                        const normHost = normalizeHostPattern(entry.host);
                        const evalHost = policy
                          ? evaluateHostInPolicy(policy, normHost)
                          : null;
                        const feedback = triageFeedbackByHost[normHost];
                        const rowPending = triagePendingHost === normHost;
                        const controlsDisabled =
                          !canEditBotSecrets ||
                          policyMutationInFlight ||
                          policyStateUnknown ||
                          !policy;

                        return (
                          <tr key={entry.id}>
                            <th
                              scope="row"
                              className="whitespace-nowrap px-3 py-2.5 font-sans"
                            >
                              <span
                                className={`inline-flex items-center gap-1.5 rounded-xs border px-2 py-0.5 text-[12px] font-bold ${badge.className}`}
                              >
                                <BadgeIcon
                                  className="h-3.5 w-3.5 shrink-0"
                                  aria-hidden="true"
                                />
                                <span>{t(badge.labelKey)}</span>
                              </span>
                            </th>
                            <td className="px-3 py-2.5 text-on-dark">
                              <div className="flex flex-col gap-0.5">
                                <span>{formatLogTarget(entry)}</span>
                                <span className="text-[11px] text-on-dark-muted">
                                  {t('logStatusCode', {
                                    code: entry.statusCode,
                                  })}
                                </span>
                              </div>
                            </td>
                            <td className="px-3 py-2.5 text-on-dark-muted">
                              {entry.secretName
                                ? t('logSecretLabel', {
                                    scope: entry.secretScope,
                                    name: entry.secretName,
                                  })
                                : t('logNoSecret')}
                            </td>
                            <td className="whitespace-nowrap px-3 py-2.5 text-on-dark-muted">
                              {t('logMetaLatency', {
                                ms: entry.latencyMs,
                                bytes: entry.bytesTransferred,
                              })}
                            </td>
                            <td className="whitespace-nowrap px-3 py-2.5 text-right text-on-dark-muted">
                              {formatTimestamp(entry.createdAt)}
                            </td>
                            <td className="whitespace-nowrap px-3 py-2.5 text-right font-sans">
                              <div className="inline-flex flex-wrap items-center justify-end gap-2">
                                {entry.action === 'mitm_token_added' ? (
                                  <span
                                    className={`inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border px-3 text-[12px] font-bold ${
                                      evalHost?.matchedBlock
                                        ? 'border-dashed border-amber-line bg-dark-2 text-amber-on-dark'
                                        : 'border-on-dark-faint/30 bg-dark-2 text-on-dark-muted'
                                    }`}
                                  >
                                    <Lock
                                      className="h-3.5 w-3.5 shrink-0"
                                      aria-hidden="true"
                                    />
                                    <span>
                                      {evalHost?.matchedBlock
                                        ? t('triageViaSecretBlocked')
                                        : t('triageViaSecret')}
                                    </span>
                                  </span>
                                ) : rowPending ? (
                                  <span className="inline-flex min-h-[44px] items-center rounded-sm border border-on-dark-faint/30 bg-dark-2 px-3 text-[12px] font-bold text-on-dark-muted">
                                    {t('triageSaving')}
                                  </span>
                                ) : feedback ? (
                                  <div className="inline-flex flex-wrap items-center justify-end gap-2">
                                    <span
                                      className={`inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border px-3 text-[12px] font-bold ${
                                        feedback.status === 'pending' ||
                                        feedback.op === 'block'
                                          ? 'border-dashed border-amber-line bg-dark-2 text-amber-on-dark'
                                          : 'border-mint-line/40 bg-mint-soft text-green-600'
                                      }`}
                                    >
                                      {feedback.status === 'pending'
                                        ? t('triagePendingEnforcement')
                                        : feedback.op === 'allow'
                                          ? t('triageAllowedFeedback')
                                          : t('triageBlockedFeedback')}
                                    </span>
                                    {canEditBotSecrets ? (
                                      <button
                                        type="button"
                                        aria-label={t('triageUndoAria', {
                                          host: normHost,
                                        })}
                                        disabled={controlsDisabled}
                                        onClick={() =>
                                          void handleUndoTriage(normHost)
                                        }
                                        className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-on-dark-faint/40 bg-dark px-3 text-[12px] font-bold text-paper transition hover:border-green-light hover:text-mint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-50"
                                      >
                                        <RotateCcw
                                          className="h-3.5 w-3.5 shrink-0"
                                          aria-hidden="true"
                                        />
                                        <span>{t('triageUndoAction')}</span>
                                      </button>
                                    ) : null}
                                  </div>
                                ) : evalHost?.isBlockedWildcard &&
                                  evalHost.matchedBlock ? (
                                  <div className="inline-flex flex-wrap items-center justify-end gap-2">
                                    <span className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-dashed border-amber-line bg-dark-2 px-3 font-mono text-[12px] font-bold text-amber-on-dark">
                                      <Ban
                                        className="h-3.5 w-3.5 shrink-0"
                                        aria-hidden="true"
                                      />
                                      <span>
                                        {t('triageWildcardBlocked', {
                                          pattern: evalHost.matchedBlock,
                                        })}
                                      </span>
                                    </span>
                                    {canEditBotSecrets ? (
                                      <button
                                        type="button"
                                        onClick={handleJumpToBlockedRule}
                                        className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-amber-line bg-dark px-3 text-[12px] font-bold text-amber-on-dark transition hover:bg-dark-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                                      >
                                        <Pencil
                                          className="h-3.5 w-3.5 shrink-0"
                                          aria-hidden="true"
                                        />
                                        <span>{t('triageEditRuleAction')}</span>
                                      </button>
                                    ) : null}
                                  </div>
                                ) : evalHost ? (
                                  <>
                                    {evalHost.isBlockedExact ? (
                                      <span className="inline-flex min-h-[44px] items-center rounded-sm border border-dashed border-amber-line bg-dark-2 px-3 text-[12px] font-bold text-amber-on-dark">
                                        {t('triageBlockedStatus')}
                                      </span>
                                    ) : null}
                                    {!evalHost.isAllowed &&
                                    canEditBotSecrets ? (
                                      <button
                                        type="button"
                                        aria-label={t('triageAllowAria', {
                                          host: normHost,
                                        })}
                                        disabled={controlsDisabled}
                                        onClick={() =>
                                          void handleTriageHost(
                                            normHost,
                                            'allow',
                                          )
                                        }
                                        className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-mint-line/40 bg-mint-soft px-3 text-[12px] font-bold text-green-600 transition hover:bg-mint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-50"
                                      >
                                        <Check
                                          className="h-3.5 w-3.5 shrink-0"
                                          aria-hidden="true"
                                        />
                                        <span>{t('triageAllowAction')}</span>
                                      </button>
                                    ) : null}
                                    {(evalHost.isAllowed ||
                                      !evalHost.isBlockedExact) &&
                                    canEditBotSecrets ? (
                                      <button
                                        type="button"
                                        aria-label={t('triageBlockAria', {
                                          host: normHost,
                                        })}
                                        disabled={controlsDisabled}
                                        onClick={() =>
                                          void handleTriageHost(
                                            normHost,
                                            'block',
                                          )
                                        }
                                        className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border border-amber-line bg-dark-2 px-3 text-[12px] font-bold text-amber-on-dark transition hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-50"
                                      >
                                        <Ban
                                          className="h-3.5 w-3.5 shrink-0"
                                          aria-hidden="true"
                                        />
                                        <span>{t('triageBlockAction')}</span>
                                      </button>
                                    ) : null}
                                    {!canEditBotSecrets &&
                                    !evalHost.isBlockedExact ? (
                                      <span className="inline-flex min-h-[44px] items-center rounded-sm border border-on-dark-faint/30 bg-dark-2 px-3 text-[12px] font-bold text-on-dark-muted">
                                        {evalHost.isAllowed
                                          ? t('triageAllowedStatus')
                                          : t('triageBlockedStatus')}
                                      </span>
                                    ) : null}
                                  </>
                                ) : null}
                              </div>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </section>

          <section className="flex flex-col gap-4">
            <div className="flex flex-wrap items-end justify-between gap-3">
              <div>
                <Eyebrow>{t('budgetsEyebrow')}</Eyebrow>
                <h2 className="mt-1 text-[22px] font-black tracking-[-0.01em] text-ink">
                  {t('budgetsTitle')}
                </h2>
              </div>
              <Link
                href="/settings/members#budgets"
                className="inline-flex min-h-[44px] items-center gap-1.5 rounded-sm border-[1.5px] border-line bg-paper px-4 text-[14px] font-bold text-green-600 transition duration-150 hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              >
                <span>{t('workspaceBudgetsLink')}</span>
                <ArrowRight className="h-4 w-4" aria-hidden="true" />
              </Link>
            </div>
            <BudgetPanel
              workspaceId={workspace.id}
              botId={bot.id}
              canManage={canManageMembers(workspace.role)}
              ignoreMissing
            />
          </section>
        </>
      )}
    </div>
  );
}
