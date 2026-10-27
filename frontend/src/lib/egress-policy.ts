import type { EgressPolicyMode, EgressPolicyResponse } from '@/types/api';

export type HostPolicyState = 'allowed' | 'blocked' | 'none';

export interface HostEvaluation {
  normalizedHost: string;
  matchedBlock: string | null;
  isBlockedExact: boolean;
  isBlockedWildcard: boolean;
  matchedAllow: string | null;
  isSecretHost: boolean;
  isAllowed: boolean;
}

export interface TriageUndoSpec {
  host: string;
  appliedOp: 'allow' | 'block';
  restoreTo: HostPolicyState;
  appliedRevision: number;
}

/** Normalizes a hostname or wildcard pattern for policy comparisons. */
export function normalizeHostPattern(raw: string): string {
  return raw.trim().toLowerCase().replace(/\.+$/, '');
}

/**
 * Checks whether host matches pattern. Wildcard patterns ("*.example.com") match
 * subdomains (including nested subdomains), never the apex ("example.com").
 */
export function hostMatchesPattern(host: string, pattern: string): boolean {
  const normHost = normalizeHostPattern(host);
  const normPattern = normalizeHostPattern(pattern);
  if (!normHost || !normPattern) {
    return false;
  }
  if (normPattern.startsWith('*.')) {
    const base = normPattern.slice(2);
    return base.length > 0 && normHost.endsWith(`.${base}`);
  }
  return normHost === normPattern;
}

/**
 * Returns the most specific matching pattern for host (exact matches before
 * wildcards, longer patterns before shorter ones), or null if none match.
 */
export function matchingHostPattern(
  patterns: readonly string[],
  host: string,
): string | null {
  const normHost = normalizeHostPattern(host);
  if (!normHost) {
    return null;
  }
  let best: string | null = null;
  let bestRank = -1;
  for (const raw of patterns) {
    const pattern = normalizeHostPattern(raw);
    if (!pattern || !hostMatchesPattern(normHost, pattern)) {
      continue;
    }
    const rank = pattern.length + (pattern.startsWith('*.') ? 0 : 10000);
    if (rank > bestRank) {
      best = pattern;
      bestRank = rank;
    }
  }
  return best;
}

/** Evaluates a hostname against a saved egress policy using deny-first precedence. */
export function evaluateHostInPolicy(
  policy: EgressPolicyResponse,
  rawHost: string,
): HostEvaluation {
  const normalizedHost = normalizeHostPattern(rawHost);
  const matchedBlock = matchingHostPattern(policy.blockedHosts, normalizedHost);
  const matchedAllow = matchingHostPattern(policy.allowedHosts, normalizedHost);
  const isSecretHost =
    matchingHostPattern(policy.effectiveSecretHosts, normalizedHost) !== null;
  const isBlockedExact =
    matchedBlock !== null && matchedBlock === normalizedHost;
  const isBlockedWildcard =
    matchedBlock !== null && matchedBlock.startsWith('*.');
  const isAllowed =
    matchedBlock === null &&
    (policy.policy === 'open' || matchedAllow !== null || isSecretHost);

  return {
    normalizedHost,
    matchedBlock,
    isBlockedExact,
    isBlockedWildcard,
    matchedAllow,
    isSecretHost,
    isAllowed,
  };
}

/** Returns true when two host lists contain the same normalized patterns in order. */
export function hostListsEqual(
  a: readonly string[],
  b: readonly string[],
): boolean {
  if (a.length !== b.length) {
    return false;
  }
  for (let i = 0; i < a.length; i += 1) {
    if (normalizeHostPattern(a[i] ?? '') !== normalizeHostPattern(b[i] ?? '')) {
      return false;
    }
  }
  return true;
}

/** Checks whether unsaved policy-card edits differ from the authoritative policy. */
export function isPolicyDraftDirty(
  saved: EgressPolicyResponse | null,
  draftMode: EgressPolicyMode,
  draftAllowed: readonly string[],
  draftBlocked: readonly string[],
): boolean {
  if (!saved) {
    return false;
  }
  return (
    draftMode !== saved.policy ||
    !hostListsEqual(draftAllowed, saved.allowedHosts) ||
    !hostListsEqual(draftBlocked, saved.blockedHosts)
  );
}

/** Returns the exact explicit state of a single pattern inside a policy. */
export function exactHostStateInPolicy(
  policy: Pick<EgressPolicyResponse, 'allowedHosts' | 'blockedHosts'>,
  rawPattern: string,
): HostPolicyState {
  const norm = normalizeHostPattern(rawPattern);
  if (policy.blockedHosts.some((h) => normalizeHostPattern(h) === norm)) {
    return 'blocked';
  }
  if (policy.allowedHosts.some((h) => normalizeHostPattern(h) === norm)) {
    return 'allowed';
  }
  return 'none';
}

/**
 * Computes the next allowed/blocked lists when applying a single-host triage
 * operation against an authoritative policy without touching unrelated rules.
 */
export function computeTriageMutation(
  base: EgressPolicyResponse,
  rawHost: string,
  op: 'allow' | 'block',
): {
  allowedHosts: string[];
  blockedHosts: string[];
  undo: TriageUndoSpec;
} {
  const norm = normalizeHostPattern(rawHost);
  const prevState = exactHostStateInPolicy(base, norm);

  const nextAllowed = base.allowedHosts.filter(
    (h) => normalizeHostPattern(h) !== norm,
  );
  const nextBlocked = base.blockedHosts.filter(
    (h) => normalizeHostPattern(h) !== norm,
  );

  if (op === 'allow') {
    if (base.policy === 'allowlist') {
      nextAllowed.push(norm);
    }
  } else {
    nextBlocked.push(norm);
  }

  return {
    allowedHosts: nextAllowed,
    blockedHosts: nextBlocked,
    undo: {
      host: norm,
      appliedOp: op,
      restoreTo: prevState,
      appliedRevision: base.revision,
    },
  };
}

/**
 * Computes a pattern-specific Undo mutation against the latest authoritative policy.
 * Returns null if the affected pattern was modified by another operation since
 * the original triage action, protecting concurrent edits from being overwritten.
 */
export function computeUndoMutation(
  current: EgressPolicyResponse,
  undo: TriageUndoSpec,
): { allowedHosts: string[]; blockedHosts: string[] } | null {
  const norm = normalizeHostPattern(undo.host);
  const currentState = exactHostStateInPolicy(current, norm);

  if (undo.appliedOp === 'block') {
    if (currentState !== 'blocked') {
      return null;
    }
  } else {
    const expectedState: HostPolicyState =
      current.policy === 'allowlist' ? 'allowed' : 'none';
    if (currentState !== expectedState) {
      return null;
    }
  }

  const nextAllowed = current.allowedHosts.filter(
    (h) => normalizeHostPattern(h) !== norm,
  );
  const nextBlocked = current.blockedHosts.filter(
    (h) => normalizeHostPattern(h) !== norm,
  );

  if (undo.restoreTo === 'allowed') {
    nextAllowed.push(norm);
  } else if (undo.restoreTo === 'blocked') {
    nextBlocked.push(norm);
  }

  return {
    allowedHosts: nextAllowed,
    blockedHosts: nextBlocked,
  };
}
