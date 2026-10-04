'use client';

import { useTranslations } from 'next-intl';
import { useEffect, useRef, useState } from 'react';

import { Button, Eyebrow, StatusChip } from '@/components/ui';

export interface RFBClient {
  viewOnly: boolean;
  scaleViewport: boolean;
  disconnect: () => void;
  addEventListener: (type: string, listener: () => void) => void;
}

export type RFBConnector = (
  target: HTMLElement,
  url: string,
) => Promise<RFBClient>;

export interface VncViewportProps {
  /** Base WebSocket URL (e.g. wss://host/ws/bots/{ws}/{bot}/vnc). */
  wsUrl: string;
  /** Short-lived HMAC ticket required by the Go relay. */
  ticket: string;
  /** Whether the user currently holds the interactive control lock. */
  initialControl?: boolean;
  /** Optional custom RFB connector for deterministic unit tests. */
  connectRFB?: RFBConnector;
  onControlChange?: (inControl: boolean) => void;
}

async function defaultConnectRFB(
  target: HTMLElement,
  url: string,
): Promise<RFBClient> {
  const { default: RFB } = await import('@novnc/novnc');
  return new RFB(target, url, { wsProtocols: ['binary'] });
}

/**
 * Embeds a 16:10 (1280x800) noVNC viewport backed by the signed-ticket Go relay,
 * keeping viewer input blocked unless control is actively claimed.
 */
export function VncViewport({
  wsUrl,
  ticket,
  initialControl = false,
  connectRFB = defaultConnectRFB,
  onControlChange,
}: VncViewportProps) {
  const t = useTranslations('workspace.vnc');
  const stageRef = useRef<HTMLDivElement | null>(null);
  const clientRef = useRef<RFBClient | null>(null);
  const [inControl, setInControl] = useState(initialControl);
  const inControlRef = useRef(inControl);
  const [connected, setConnected] = useState(false);

  const hasValidEndpoint = wsUrl.trim().length > 0 && ticket.trim().length > 0;

  useEffect(() => {
    inControlRef.current = inControl;
    if (clientRef.current) {
      clientRef.current.viewOnly = !inControl;
    }
  }, [inControl]);

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage || !hasValidEndpoint) {
      setConnected(false);
      return;
    }

    let cancelled = false;
    const separator = wsUrl.includes('?') ? '&' : '?';
    const fullUrl = `${wsUrl}${separator}ticket=${encodeURIComponent(ticket)}`;

    void connectRFB(stage, fullUrl).then((client) => {
      if (cancelled) {
        client.disconnect();
        return;
      }
      client.viewOnly = !inControlRef.current;
      client.scaleViewport = true;
      client.addEventListener('connect', () => setConnected(true));
      client.addEventListener('disconnect', () => setConnected(false));
      clientRef.current = client;
    });

    return () => {
      cancelled = true;
      clientRef.current?.disconnect();
      clientRef.current = null;
    };
  }, [wsUrl, ticket, hasValidEndpoint, connectRFB]);

  const handleToggleControl = () => {
    const next = !inControl;
    setInControl(next);
    onControlChange?.(next);
  };

  const statusChip = !hasValidEndpoint
    ? { status: 'stopped' as const, label: t('disconnected') }
    : !connected
      ? { status: 'thinking' as const, label: t('connecting') }
      : inControl
        ? { status: 'budgetWarning' as const, label: t('connectedInControl') }
        : { status: 'working' as const, label: t('connectedViewOnly') };

  return (
    <section className="flex flex-col gap-3 rounded-lg border border-line bg-paper p-4 shadow-card">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-col gap-1">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h2 className="text-[18px] font-black leading-[1.3] text-ink">
            {t('title')}
          </h2>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <StatusChip status={statusChip.status} label={statusChip.label} />
          <Button
            variant={inControl ? 'primary' : 'secondary'}
            aria-pressed={inControl}
            disabled={!hasValidEndpoint}
            onClick={handleToggleControl}
          >
            {inControl ? t('releaseControl') : t('takeControl')}
          </Button>
        </div>
      </div>

      {!hasValidEndpoint ? (
        <p
          role="alert"
          className="rounded-sm border border-dashed border-amber-line bg-amber-tint px-3.5 py-2.5 text-[14px] font-bold text-amber"
        >
          {t('missingTicket')}
        </p>
      ) : null}

      <div
        ref={stageRef}
        data-testid="vnc-stage"
        aria-label={t('resolution')}
        className="aspect-[16/10] w-full overflow-hidden rounded-md border border-line bg-terminal"
      />
    </section>
  );
}
