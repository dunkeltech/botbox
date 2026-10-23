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

/** First wait after a closed socket. Each later wait doubles until the cap. */
export const VNC_RETRY_BASE_MS = 500;

/** Upper bound so a desktop that never opens does not back off forever. */
export const VNC_RETRY_CAP_MS = 4000;

/**
 * noVNC prints this when the relay closes the socket during connect.
 * Startup does that until websockify is listening; it is not a user error.
 */
export const EXPECTED_VNC_CLOSE = 'Failed when connecting: Connection closed';

/** Delay before opening another RFB object. `failedAttempts` is how many closes already happened. */
export function vncRetryDelayMs(failedAttempts: number): number {
  const exponent = Math.max(0, Math.min(failedAttempts, 6));
  return Math.min(VNC_RETRY_BASE_MS * 2 ** exponent, VNC_RETRY_CAP_MS);
}

/**
 * Hides the expected startup close and forwards every other error.
 * noVNC binds `console.error` once, at module evaluation, so the caller
 * must install this wrapper before the first `import('@novnc/novnc')`.
 */
export function silenceExpectedVncClose(
  report: (...args: unknown[]) => void,
): (...args: unknown[]) => void {
  return (...args: unknown[]) => {
    const text = args.map((arg) => String(arg)).join(' ');
    if (text.includes(EXPECTED_VNC_CLOSE)) return;
    report(...args);
  };
}

let novncConsolePatched = false;

function installExpectedVncCloseFilter(): void {
  if (novncConsolePatched) return;
  novncConsolePatched = true;
  const report = console.error.bind(console);
  console.error = silenceExpectedVncClose(report) as typeof console.error;
}

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
  /**
   * Socket closed while this view is still mounted.
   * Not called for cleanup, so the parent can renew the ticket without
   * aborting the fast retries on every unmount.
   */
  onConnectFailed?: () => void;
  /** Optional handler invoked instead of mutating local state in controlled mode. */
  onRequestControlToggle?: (nextControl: boolean) => void | Promise<void>;
  /** Bot pointer in desktop pixels. Shown only while the human does not hold the lock. */
  cursor?: { x: number; y: number; label: string } | null;
  showCursor?: boolean;
}

async function defaultConnectRFB(
  target: HTMLElement,
  url: string,
): Promise<RFBClient> {
  // The library copies console.error at import time. Patch first, or the
  // startup close still reaches the Next.js error overlay.
  installExpectedVncCloseFilter();
  const { default: RFB } = await import('@novnc/novnc');
  return new RFB(target, url, { wsProtocols: ['binary'] });
}

/**
 * Drops a client that is still open. noVNC keeps `disconnected` forever, and
 * disconnect() after the socket already closed logs
 * "Tried changing state of a disconnected RFB object". That happens when the
 * machine stops: the relay closes the socket, then this view unmounts.
 */
function releaseRfb(client: RFBClient) {
  const state = (client as { _rfbConnectionState?: string })
    ._rfbConnectionState;
  if (state === 'disconnected' || state === 'disconnecting') return;
  client.disconnect();
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
  onConnectFailed,
  onRequestControlToggle,
  cursor = null,
  showCursor = false,
}: VncViewportProps) {
  const t = useTranslations('workspace.vnc');
  const stageRef = useRef<HTMLDivElement | null>(null);
  const clientRef = useRef<RFBClient | null>(null);
  const [internalControl, setInternalControl] = useState(initialControl);
  const effectiveControl = controlledInControl ?? internalControl;
  const inControlRef = useRef(effectiveControl);
  const onConnectionChangeRef = useRef(onConnectionChange);
  const onConnectFailedRef = useRef(onConnectFailed);
  const [connected, setConnected] = useState(false);

  onConnectionChangeRef.current = onConnectionChange;
  onConnectFailedRef.current = onConnectFailed;

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
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let failedAttempts = 0;
    let attempt = 0;
    const separator = wsUrl.includes('?') ? '&' : '?';
    const fullUrl = `${wsUrl}${separator}ticket=${encodeURIComponent(ticket)}`;

    const clearRetry = () => {
      if (retryTimer !== null) {
        clearTimeout(retryTimer);
        retryTimer = null;
      }
    };

    // The sandbox reports running before websockify accepts. The relay then
    // closes the socket (1006) and noVNC will not reuse that RFB object.
    // A new object on the same ticket covers the usual few seconds of boot.
    // Cleanup sets `cancelled` first so our own disconnect is not a failure.
    const scheduleRetry = () => {
      if (cancelled) return;
      onConnectFailedRef.current?.();
      if (retryTimer !== null) return;
      const delay = vncRetryDelayMs(failedAttempts);
      failedAttempts += 1;
      retryTimer = setTimeout(() => {
        retryTimer = null;
        open();
      }, delay);
    };

    const open = () => {
      if (cancelled) return;
      const previous = clientRef.current;
      clientRef.current = null;
      if (previous) releaseRfb(previous);
      const generation = ++attempt;
      void connectRFB(stage, fullUrl)
        .then((client) => {
          if (cancelled || generation !== attempt) {
            releaseRfb(client);
            return;
          }
          client.addEventListener('disconnect', () => {
            // The socket is already gone. Cleanup must not call disconnect() again.
            if (clientRef.current === client) clientRef.current = null;
            if (cancelled) return;
            setConnected(false);
            onConnectionChangeRef.current?.(false);
            scheduleRetry();
          });
          client.viewOnly = !inControlRef.current;
          client.scaleViewport = true;
          client.addEventListener('connect', () => {
            if (cancelled || generation !== attempt) return;
            failedAttempts = 0;
            clearRetry();
            setConnected(true);
            onConnectionChangeRef.current?.(true);
          });
          clientRef.current = client;
        })
        .catch(() => {
          if (cancelled || generation !== attempt) return;
          scheduleRetry();
        });
    };

    open();

    return () => {
      cancelled = true;
      clearRetry();
      const client = clientRef.current;
      clientRef.current = null;
      if (client) releaseRfb(client);
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

      <div className="relative">
        <div
          ref={stageRef}
          data-testid="vnc-stage"
          aria-label={t('resolution')}
          className={`aspect-[16/10] w-full overflow-hidden bg-terminal ${hideHeader ? '' : 'rounded-md border border-line'}`.trim()}
        />
        {showCursor && cursor ? (
          <div
            data-testid="bot-cursor"
            role="img"
            aria-label={cursor.label}
            className="pointer-events-none absolute z-10"
            style={{
              left: `${(cursor.x / 1280) * 100}%`,
              top: `${(cursor.y / 800) * 100}%`,
            }}
          >
            <span
              aria-hidden="true"
              className="block h-3 w-3 border-b-2 border-l-2 border-green bg-paper"
            />
            <span className="mt-1 inline-flex min-h-[24px] items-center rounded-sm bg-dark px-1.5 text-[11px] font-bold text-paper">
              {cursor.label}
            </span>
          </div>
        ) : null}
      </div>
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
