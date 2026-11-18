// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useTranslations } from 'next-intl';
import {
  type FormEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react';

import {
  ApiClientError,
  getBotEgressPolicy,
  updateBotEgressPolicy,
} from '@/lib/api-client';
import {
  computeTriageMutation,
  computeUndoMutation,
  isPolicyDraftDirty,
  normalizeHostPattern,
  type TriageUndoSpec,
} from '@/lib/egress-policy';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type {
  EgressEnforcementStatus,
  EgressPolicyMode,
  EgressPolicyResponse,
  UpdateEgressPolicyRequest,
} from '@/types/api';

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

function isPolicyConflict(err: unknown): boolean {
  return (
    err instanceof ApiClientError &&
    err.status === 409 &&
    err.code === 'policy_conflict'
  );
}

function noticeForResponse(updated: EgressPolicyResponse): PolicyStatusNotice {
  if (updated.enforcementStatus === 'pending') {
    return { kind: 'pending', revision: updated.revision };
  }
  if (updated.enforcementStatus === 'not_running') {
    return { kind: 'not_running', revision: updated.revision };
  }
  return { kind: 'applied' };
}

/** Keeps the saved egress revision, form draft, and per-host triage in sync. */
export function useBotEgressPolicy(
  workspaceId: string,
  botId: string,
  canEditBotSecrets: boolean,
) {
  const t = useTranslations('secrets');
  const [policy, setPolicy] = useState<EgressPolicyResponse | null>(null);

  // Outbound policy form draft state (kept separate from authoritative `policy`).
  const [policyMode, setPolicyMode] = useState<EgressPolicyMode>('allowlist');
  const [allowedHosts, setAllowedHosts] = useState<string[]>([]);
  const [blockedHosts, setBlockedHosts] = useState<string[]>([]);
  const [hostDraft, setHostDraft] = useState('');
  const [blockedHostDraft, setBlockedHostDraft] = useState('');
  const [mutation, setMutation] = useState<{ host?: string } | null>(null);
  const policyMutationInFlight = mutation !== null;
  const triagePendingHost = mutation?.host ?? null;
  const savingPolicy = policyMutationInFlight && triagePendingHost === null;
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

  const policyRef = useRef<EgressPolicyResponse | null>(null);
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
  // Clicks can arrive before React renders the disabled controls.
  const mutatingPolicyRef = useRef(false);

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

  useEffect(() => {
    if (policy?.enforcementStatus !== 'pending') {
      return;
    }
    let active = true;
    const timer = window.setTimeout(() => {
      void getBotEgressPolicy(workspaceId, botId)
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
    botId,
    policy?.enforcementStatus,
    policy?.revision,
    workspaceId,
  ]);

  function changePolicyMode(nextMode: EgressPolicyMode) {
    setPolicyMode(nextMode);
    setPolicyStatusNotice(null);
  }

  function addHost(operation: 'allow' | 'block') {
    if (!canEditBotSecrets) return;

    const allowing = operation === 'allow';
    const normalized = normalizeHostPattern(
      allowing ? hostDraft : blockedHostDraft,
    );
    if (!normalized) return;

    const setMatchingHosts = allowing ? setAllowedHosts : setBlockedHosts;
    const setOtherHosts = allowing ? setBlockedHosts : setAllowedHosts;
    const setInput = allowing ? setHostDraft : setBlockedHostDraft;
    setMatchingHosts((hosts) =>
      hosts.includes(normalized) ? hosts : [...hosts, normalized],
    );
    setOtherHosts((hosts) =>
      hosts.filter((item) => normalizeHostPattern(item) !== normalized),
    );
    setInput('');
    setPolicyStatusNotice(null);
    setPolicyErrorKey(null);
  }

  function removeHost(operation: 'allow' | 'block', host: string) {
    if (!canEditBotSecrets) return;
    const setHosts = operation === 'allow' ? setAllowedHosts : setBlockedHosts;
    setHosts((hosts) => hosts.filter((item) => item !== host));
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

  function dismissDraftConflictPrompt() {
    setDraftConflictPrompt(false);
  }

  async function handleReloadPolicy(): Promise<EgressPolicyResponse | null> {
    if (reloadingPolicy) {
      return null;
    }
    setReloadingPolicy(true);
    setPolicyErrorKey(null);
    try {
      const latest = await getBotEgressPolicy(workspaceId, botId);
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
    if (isPolicyConflict(err)) {
      setPolicyErrorKey('policyConflict');
      try {
        const latest = await getBotEgressPolicy(workspaceId, botId);
        applyAuthoritativePolicy(latest, { preserveDraftIfDirty: true });
      } catch {
        setPolicyStateUnknown(true);
      }
      return;
    }

    if (isUncertainWriteError(err)) {
      try {
        const authoritative = await getBotEgressPolicy(workspaceId, botId);
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

  function getWritablePolicy() {
    if (!canEditBotSecrets || mutatingPolicyRef.current || policyStateUnknown) {
      return null;
    }
    return policyRef.current;
  }

  async function runPolicyMutation(
    operation: () => Promise<void>,
    host?: string,
  ) {
    mutatingPolicyRef.current = true;
    setMutation({ host });
    setPolicyErrorKey(null);
    try {
      await operation();
    } catch (err) {
      await reconcilePolicyFailure(err);
    } finally {
      mutatingPolicyRef.current = false;
      setMutation(null);
    }
  }

  function writePolicy(
    saved: EgressPolicyResponse,
    {
      allowedHosts,
      blockedHosts,
    }: Pick<UpdateEgressPolicyRequest, 'allowedHosts' | 'blockedHosts'>,
    mode = saved.policy,
  ) {
    return updateBotEgressPolicy(workspaceId, botId, {
      policy: mode,
      allowedHosts,
      blockedHosts,
      expectedRevision: saved.revision,
    });
  }

  async function handleSavePolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const currentSaved = getWritablePolicy();
    if (!currentSaved) return;

    await runPolicyMutation(async () => {
      setPolicyStatusNotice(null);
      const pendingAllowed = normalizeHostPattern(hostDraft);
      const pendingBlocked = normalizeHostPattern(blockedHostDraft);

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

      const updated = await writePolicy(
        currentSaved,
        { allowedHosts: nextAllowed, blockedHosts: nextBlocked },
        policyMode,
      );
      applyAuthoritativePolicy(updated, { preserveDraftIfDirty: false });
      setHostDraft('');
      setBlockedHostDraft('');
      setPolicyStatusNotice(noticeForResponse(updated));
    });
  }

  async function handleTriageHost(rawHost: string, op: 'allow' | 'block') {
    const currentSaved = getWritablePolicy();
    const normHost = normalizeHostPattern(rawHost);
    if (!currentSaved || !normHost) return;

    await runPolicyMutation(async () => {
      setTriageAnnouncement(null);
      const mutation = computeTriageMutation(currentSaved, normHost, op);
      const updated = await writePolicy(currentSaved, mutation);
      applyAuthoritativePolicy(updated, { preserveDraftIfDirty: true });
      const feedback: HostTriageFeedback = {
        host: normHost,
        op,
        status: updated.enforcementStatus,
        revision: updated.revision,
        undo: { ...mutation.undo, appliedRevision: updated.revision },
      };
      setTriageFeedbackByHost((prev) => ({ ...prev, [normHost]: feedback }));
      setPolicyStatusNotice(noticeForResponse(updated));
      setTriageAnnouncement(
        updated.enforcementStatus === 'pending'
          ? `${normHost}: ${t('triagePendingEnforcement')}`
          : op === 'allow'
            ? `${normHost}: ${t('triageAllowedFeedback')}`
            : `${normHost}: ${t('triageBlockedFeedback')}`,
      );
    }, normHost);
  }

  async function handleUndoTriage(rawHost: string) {
    const currentSaved = getWritablePolicy();
    const normHost = normalizeHostPattern(rawHost);
    const feedback = triageFeedbackByHost[normHost];
    if (!currentSaved || !feedback) return;

    function clearFeedback() {
      setTriageFeedbackByHost((prev) => {
        const next = { ...prev };
        delete next[normHost];
        return next;
      });
    }

    function reportUndoConflict() {
      clearFeedback();
      setPolicyStatusNotice({ kind: 'undo_conflict', host: normHost });
    }

    const firstAttempt = computeUndoMutation(currentSaved, feedback.undo);
    if (!firstAttempt) {
      reportUndoConflict();
      return;
    }

    await runPolicyMutation(async () => {
      let updated: EgressPolicyResponse;
      try {
        updated = await writePolicy(currentSaved, firstAttempt);
      } catch (err) {
        if (!isPolicyConflict(err)) throw err;

        // Retry undo if the latest policy still contains the same host rule.
        const latest = await getBotEgressPolicy(workspaceId, botId);
        applyAuthoritativePolicy(latest, { preserveDraftIfDirty: true });
        const retryMutation = computeUndoMutation(latest, feedback.undo);
        if (!retryMutation) {
          reportUndoConflict();
          return;
        }
        updated = await writePolicy(latest, retryMutation);
      }

      applyAuthoritativePolicy(updated, { preserveDraftIfDirty: true });
      clearFeedback();
      setPolicyStatusNotice(
        updated.enforcementStatus === 'pending'
          ? { kind: 'pending', revision: updated.revision }
          : { kind: 'undo_complete', host: normHost },
      );
      setTriageAnnouncement(null);
    }, normHost);
  }

  return {
    policy,
    policyMode,
    changePolicyMode,
    allowedHosts,
    blockedHosts,
    hostDraft,
    setHostDraft,
    blockedHostDraft,
    setBlockedHostDraft,
    savingPolicy,
    policyMutationInFlight,
    triagePendingHost,
    policyStatusNotice,
    policyErrorKey,
    draftConflictPrompt,
    policyStateUnknown,
    reloadingPolicy,
    triageFeedbackByHost,
    triageAnnouncement,
    applyAuthoritativePolicy,
    addHost,
    removeHost,
    dismissDraftConflictPrompt,
    handleLoadSavedDraft,
    handleReloadPolicy,
    handleSavePolicy,
    handleTriageHost,
    handleUndoTriage,
  };
}
