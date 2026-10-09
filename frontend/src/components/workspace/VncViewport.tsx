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
  /** Initial interactive control state when used uncontrolled. */
  initialControl?: boolean;
  /** Controlled interactive control state synchronized with the backend control lock. */
  inControl?: boolean;
  /** Whether the current user is permitted to take or release control. */
  canTakeControl?: boolean;
  /** Disables the control toggle while a control lock request is in flight. */
  controlBusy?: boolean;
  /** Hide the standalone header when embedded inside the tabbed workspace panel. */
  hideHeader?: boolean;
  /** Optional custom RFB connector for deterministic unit tests. */
  connectRFB?: RFBConnector;
  onControlChange?: (inControl: boolean) => void;
  onConnectionChange?: (connected: boolean) => void;
  /** Optional handler invoked instead of mutating local state in controlled mode. */
  onRequestControlToggle?: (nextControl: boolean) => void | Promise<void>;
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
  inControl: controlledInControl,
  canTakeControl = true,
  controlBusy = false,
  hideHeader = false,
  connectRFB = defaultConnectRFB,
  onControlChange,
  onConnectionChange,
  onRequestControlToggle,
}: VncViewportProps) {
  const t = useTranslations('workspace.vnc');
  const stageRef = useRef<HTMLDivElement | null>(null);
  const clientRef = useRef<RFBClient | null>(null);
  const [internalControl, setInternalControl] = useState(initialControl);
  const effectiveControl = controlledInControl ?? internalControl;
  const inControlRef = useRef(effectiveControl);
  const onConnectionChangeRef = useRef(onConnectionChange);
  const [connected, setConnected] = useState(false);

  onConnectionChangeRef.current = onConnectionChange;

  const hasValidEndpoint = wsUrl.trim().length > 0 && ticket.trim().length > 0;

  useEffect(() => {
    inControlRef.current = effectiveControl;
    if (clientRef.current) {
      clientRef.current.viewOnly = !effectiveControl;
    }
  }, [effectiveControl]);

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage || !hasValidEndpoint) {
      setConnected(false);
      onConnectionChangeRef.current?.(false);
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
      client.addEventListener('connect', () => {
        setConnected(true);
        onConnectionChangeRef.current?.(true);
      });
      client.addEventListener('disconnect', () => {
        setConnected(false);
        onConnectionChangeRef.current?.(false);
      });
      clientRef.current = client;
    });

    return () => {
      cancelled = true;
      clientRef.current?.disconnect();
      clientRef.current = null;
      onConnectionChangeRef.current?.(false);
    };
  }, [wsUrl, ticket, hasValidEndpoint, connectRFB]);

  const handleToggleControl = () => {
    const next = !effectiveControl;
    if (onRequestControlToggle) {
      void onRequestControlToggle(next);
      return;
    }
    setInternalControl(next);
    onControlChange?.(next);
  };

  const statusChip = !hasValidEndpoint
    ? { status: 'stopped' as const, label: t('disconnected') }
    : !connected
      ? { status: 'thinking' as const, label: t('connecting') }
      : effectiveControl
        ? { status: 'budgetWarning' as const, label: t('connectedInControl') }
        : { status: 'working' as const, label: t('connectedViewOnly') };

  const body = (
    <>
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
        className={`aspect-[16/10] w-full overflow-hidden bg-terminal ${hideHeader ? '' : 'rounded-md border border-line'}`.trim()}
      />
    </>
  );

  if (hideHeader) {
    return <div className="flex flex-col">{body}</div>;
  }

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
            variant={effectiveControl ? 'primary' : 'secondary'}
            aria-pressed={effectiveControl}
            disabled={
              !hasValidEndpoint ||
              controlBusy ||
              (!effectiveControl && !canTakeControl)
            }
            onClick={handleToggleControl}
          >
            {effectiveControl ? t('releaseControl') : t('takeControl')}
          </Button>
        </div>
      </div>

      {body}
    </section>
  );
}
