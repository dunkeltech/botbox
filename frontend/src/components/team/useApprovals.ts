// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  applyDecision,
  type ApprovalDecision,
} from '@/components/team/approvals';
import { requestJson } from '@/lib/api-client';
import { resolveErrorKey, type ErrorTranslationKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

type ApprovalList = components['schemas']['ApprovalList'];

/** Cache key shared by the sidebar badge, the transcript cards, and the inbox. */
export function approvalsQueryKey(workspaceId: string) {
  return ['approvals', workspaceId] as const;
}

function approvalsURL(workspaceId: string) {
  return `/api/workspaces/${encodeURIComponent(workspaceId)}/approvals`;
}

/**
 * Every visible approval, not only the pending ones. A decided card in the
 * open group still has to say Approved or Denied.
 */
export function useApprovalList(workspaceId: string) {
  return useQuery({
    queryKey: approvalsQueryKey(workspaceId),
    queryFn: () => requestJson<ApprovalList>(approvalsURL(workspaceId)),
    refetchInterval: 10000,
  });
}

/**
 * Posts a decision and updates the shared cache. A second click before the
 * re-render must not post again: the API would answer 409.
 */
export function useApprovalDecision(workspaceId: string) {
  const client = useQueryClient();
  const busy = useRef<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [error, setError] = useState<{
    id: string;
    key: ErrorTranslationKey;
  } | null>(null);

  async function decide(id: string, decision: ApprovalDecision) {
    if (busy.current) return;
    busy.current = id;
    setBusyId(id);
    setError(null);
    try {
      await requestJson<void>(
        `${approvalsURL(workspaceId)}/${encodeURIComponent(id)}/decision`,
        { method: 'POST', body: { decision } },
      );
      client.setQueryData<ApprovalList>(
        approvalsQueryKey(workspaceId),
        (current) => applyDecision(current, id, decision),
      );
      // The refetch picks up the server row, a deny notice, and a cleared pause.
      await client.invalidateQueries({
        queryKey: approvalsQueryKey(workspaceId),
      });
      await client.invalidateQueries({
        queryKey: ['group-messages', workspaceId],
      });
      await client.invalidateQueries({ queryKey: ['groups', workspaceId] });
    } catch (err) {
      setError({ id, key: resolveErrorKey(err) });
    } finally {
      busy.current = null;
      setBusyId(null);
    }
  }

  return { decide, busyId, error };
}
