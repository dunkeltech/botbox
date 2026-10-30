'use client';

import { Square } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useState } from 'react';
import { Alert, Button } from '@/components/ui';
import { requestJson } from '@/lib/api-client';

/** Stops a single running turn and shows progress until the history refreshes. */
export function StopTurnButton({
  path,
  requested,
  refresh,
  botName,
}: {
  path: string;
  requested?: boolean;
  refresh: () => Promise<unknown>;
  botName?: string;
}) {
  const t = useTranslations('chat');
  const [stopping, setStopping] = useState(false);
  const [failed, setFailed] = useState(false);
  const pending = stopping || requested;

  async function stop() {
    if (pending) return;
    setStopping(true);
    setFailed(false);
    try {
      await requestJson<void>(path, { method: 'POST' });
    } catch {
      setFailed(true);
      setStopping(false);
    }
    // Keep the action disabled until the turn leaves the running state.
    // Query observers can render after the refetch promise resolves.
    await refresh();
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <Button
        variant="secondary"
        disabled={pending}
        aria-label={
          botName
            ? t(pending ? 'stoppingBotTurn' : 'stopBotTurn', { name: botName })
            : undefined
        }
        onClick={() => void stop()}
      >
        <Square className="h-4 w-4" aria-hidden="true" />
        {t(pending ? 'stoppingTurn' : 'stopTurn')}
      </Button>
      {failed ? <Alert tone="warning">{t('stopTurnFailed')}</Alert> : null}
    </div>
  );
}
