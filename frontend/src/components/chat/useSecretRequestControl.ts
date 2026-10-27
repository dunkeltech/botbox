'use client';

import { useState } from 'react';
import { requestJson } from '@/lib/api-client';
import { resolveErrorKey, type ErrorTranslationKey } from '@/lib/errors';
import type { SecretRequestSummary } from '@/components/chat/SecretRequestCard';

/**
 * Manages active SecretRequestModal selection, inline denial requests, and
 * translated API error feedback across DM and group chat transcripts.
 */
export function useSecretRequestControl(
  workspaceId: string,
  refresh: () => Promise<void>,
) {
  const [activeRequest, setActiveRequest] =
    useState<SecretRequestSummary | null>(null);
  const [takeoverRequest, setTakeoverRequest] =
    useState<SecretRequestSummary | null>(null);
  const [busyRequestId, setBusyRequestId] = useState<string | null>(null);
  const [requestError, setRequestError] = useState<{
    id: string;
    key: ErrorTranslationKey;
  } | null>(null);

  async function deny(summary: SecretRequestSummary) {
    setBusyRequestId(summary.secretRequestId);
    setRequestError(null);
    try {
      await requestJson(
        `/api/workspaces/${workspaceId}/secret-requests/${summary.secretRequestId}/deny`,
        { method: 'POST' },
      );
      await refresh();
    } catch (err) {
      setRequestError({
        id: summary.secretRequestId,
        key: resolveErrorKey(err),
      });
    } finally {
      setBusyRequestId(null);
    }
  }

  return {
    activeRequest,
    takeoverRequest,
    openModal: (summary: SecretRequestSummary) => {
      setRequestError(null);
      setTakeoverRequest(null);
      setActiveRequest(summary);
    },
    closeModal: () => setActiveRequest(null),
    openTakeover: (summary: SecretRequestSummary) => {
      setRequestError(null);
      setActiveRequest(null);
      setTakeoverRequest(summary);
    },
    closeTakeover: () => setTakeoverRequest(null),
    busyRequestId,
    requestError,
    deny,
  };
}
