'use client';

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { Alert, Button } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type Runtime = components['schemas']['BotRuntime'];

/** Pauses the bot across conversations while preserving queued input. */
export function RunnerControl({
  workspaceId,
  botId,
  userId,
  canManage,
}: {
  workspaceId: string;
  botId: string;
  userId: string;
  canManage: boolean;
}) {
  const t = useTranslations('runner');
  const client = useQueryClient();
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState(false);
  const key = ['runner', workspaceId, botId, userId];
  const path = `/api/workspaces/${workspaceId}/bots/${botId}/runtime`;
  const state = useQuery({
    queryKey: key,
    queryFn: () => requestJson<Runtime>(path),
    refetchInterval: 3000,
  });
  async function toggle() {
    if (!state.data || saving) return;
    setSaving(true);
    setFailed(false);
    try {
      const updated = await requestJson<Runtime>(path, {
        method: 'PUT',
        body: { paused: !state.data.paused },
      });
      client.setQueryData(key, updated);
    } catch {
      // A failed response can follow a committed pause; reload authoritative state.
      setFailed(true);
      await client.invalidateQueries({ queryKey: key });
    } finally {
      setSaving(false);
    }
  }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-3">
        <span role="status" className="text-[13px] font-bold text-ink-2">
          {t(state.data?.paused ? 'paused' : state.data ? 'ready' : 'loading')}
        </span>
        {canManage && (
          <Button
            variant="secondary"
            disabled={!state.data || saving}
            onClick={() => void toggle()}
          >
            {t(state.data?.paused ? 'resume' : 'pause')}
          </Button>
        )}
      </div>
      {state.data?.paused && (
        <p className="text-[13px] text-ink-2">{t('queuePreserved')}</p>
      )}
      {(failed || state.isError) && <Alert tone="warning">{t('failed')}</Alert>}
    </div>
  );
}
