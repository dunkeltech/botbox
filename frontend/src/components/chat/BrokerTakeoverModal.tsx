// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import {
  CheckCircle2,
  CornerDownLeft,
  Monitor,
  RefreshCw,
  ShieldAlert,
  X,
} from 'lucide-react';
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type MouseEvent,
} from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Button } from '@/components/ui';
import { requestBlob, requestJson } from '@/lib/api-client';
import { resolveErrorKey, type ErrorTranslationKey } from '@/lib/errors';
import type { SecretRequestSummary } from '@/components/chat/SecretRequestCard';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type BrokerTakeoverState = components['schemas']['BrokerTakeoverState'];
type SecretRequestResult = components['schemas']['SecretRequestResult'];

/**
 * Dedicated human-only modal for resolving MFA or CAPTCHA inside the isolated
 * authentication broker while bot access and captures remain locked out.
 */
export function BrokerTakeoverModal({
  workspaceId,
  botName,
  role,
  summary,
  onClose,
  onCompleted,
}: {
  workspaceId: string;
  botName: string;
  role: WorkspaceRole;
  summary: SecretRequestSummary | null;
  onClose: () => void;
  onCompleted: () => void | Promise<void>;
}) {
  const t = useTranslations('chat');
  const errors = useTranslations('errors');
  const titleId = useId();
  const textInputId = useId();
  const imgRef = useRef<HTMLImageElement>(null);

  const [state, setState] = useState<BrokerTakeoverState | null>(null);
  const [frameUrl, setFrameUrl] = useState<string | null>(null);
  const [typedText, setTypedText] = useState('');
  const [busy, setBusy] = useState(false);
  const [starting, setStarting] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  const canDecide = role !== 'viewer';

  const replaceFrameUrl = useCallback((nextUrl: string | null) => {
    setFrameUrl((prev) => {
      if (prev) {
        URL.revokeObjectURL(prev);
      }
      return nextUrl;
    });
  }, []);

  const loadFrame = useCallback(
    async (requestId: string, ticket?: string) => {
      const qs = ticket ? `?ticket=${encodeURIComponent(ticket)}` : '';
      const blob = await requestBlob(
        `/api/workspaces/${workspaceId}/secret-requests/${requestId}/broker-takeover/frame${qs}`,
      );
      replaceFrameUrl(URL.createObjectURL(blob));
    },
    [replaceFrameUrl, workspaceId],
  );

  useEffect(() => {
    if (!summary || !canDecide) {
      setState(null);
      replaceFrameUrl(null);
      return;
    }
    let cancelled = false;
    setStarting(true);
    setErrorKey(null);
    setTypedText('');

    void (async () => {
      try {
        const started = await requestJson<BrokerTakeoverState>(
          `/api/workspaces/${workspaceId}/secret-requests/${summary.secretRequestId}/broker-takeover/start`,
          { method: 'POST' },
        );
        if (cancelled) return;
        setState(started);
        await loadFrame(summary.secretRequestId, started.takeoverTicket);
      } catch (err) {
        if (!cancelled) {
          setErrorKey(resolveErrorKey(err));
        }
      } finally {
        if (!cancelled) {
          setStarting(false);
        }
      }
    })();

    return () => {
      cancelled = true;
      replaceFrameUrl(null);
    };
  }, [canDecide, loadFrame, replaceFrameUrl, summary, workspaceId]);

  if (!summary) return null;

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape' && !busy) {
      event.preventDefault();
      onClose();
    }
  }

  async function sendInput(payload: {
    kind: 'click' | 'type' | 'key';
    x?: number;
    y?: number;
    text?: string;
    key?: string;
  }) {
    if (!summary || !canDecide || busy) return;
    setBusy(true);
    setErrorKey(null);
    try {
      const next = await requestJson<BrokerTakeoverState>(
        `/api/workspaces/${workspaceId}/secret-requests/${summary.secretRequestId}/broker-takeover/input`,
        { method: 'POST', body: payload },
      );
      setState((prev) => ({
        ...next,
        takeoverTicket: next.takeoverTicket ?? prev?.takeoverTicket,
      }));
      await loadFrame(
        summary.secretRequestId,
        next.takeoverTicket ?? state?.takeoverTicket,
      );
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusy(false);
    }
  }

  async function handleViewportClick(event: MouseEvent<HTMLImageElement>) {
    if (!imgRef.current || busy) return;
    const rect = imgRef.current.getBoundingClientRect();
    const x = Math.max(0, Math.round(event.clientX - rect.left));
    const y = Math.max(0, Math.round(event.clientY - rect.top));
    await sendInput({ kind: 'click', x, y });
  }

  async function handleSendText(event: FormEvent) {
    event.preventDefault();
    if (!typedText) return;
    const value = typedText;
    setTypedText('');
    await sendInput({ kind: 'type', text: value });
  }

  async function handleComplete() {
    if (!summary || !canDecide || busy) return;
    setBusy(true);
    setErrorKey(null);
    try {
      await requestJson<SecretRequestResult>(
        `/api/workspaces/${workspaceId}/secret-requests/${summary.secretRequestId}/broker-takeover/complete`,
        { method: 'POST' },
      );
      setTypedText('');
      await onCompleted();
      onClose();
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-overlay p-4"
      role="presentation"
      onKeyDown={handleKeyDown}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="flex max-h-[92vh] w-full max-w-[720px] flex-col overflow-hidden rounded-lg border border-amber-line bg-paper shadow-card-hover"
      >
        <div className="flex items-start justify-between gap-4 border-b border-line px-6 py-4">
          <div>
            <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-amber">
              {t('brokerTakeoverEyebrow')}
            </p>
            <h2
              id={titleId}
              className="mt-1 text-[22px] font-black leading-tight text-ink"
            >
              {t('brokerTakeoverTitle', { name: botName })}
            </h2>
          </div>
          <button
            type="button"
            aria-label={t('brokerTakeoverClose')}
            onClick={onClose}
            disabled={busy}
            className="grid h-11 w-11 shrink-0 place-items-center rounded-sm border border-line text-ink-2 transition-colors hover:border-amber hover:bg-amber-tint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <X className="h-5 w-5" aria-hidden="true" />
          </button>
        </div>

        <div className="flex flex-col overflow-y-auto px-6 py-4">
          <div className="flex items-start gap-2.5 rounded-sm border border-amber-line bg-amber-tint px-3.5 py-2.5 text-[13px] font-bold text-amber">
            <ShieldAlert
              className="mt-0.5 h-4 w-4 shrink-0"
              aria-hidden="true"
            />
            <span>{t('brokerTakeoverLockBanner')}</span>
          </div>

          <div className="mt-3 flex flex-wrap items-center justify-between gap-2 rounded-sm border border-line bg-paper-2 px-3.5 py-2 font-mono text-[12px] text-ink-2">
            <span>
              {t('brokerTakeoverOriginLabel', {
                origin: state?.origin ?? summary.targetOrigin,
              })}
            </span>
            {summary.brokerSessionId ? (
              <span>{summary.brokerSessionId}</span>
            ) : null}
          </div>

          <div className="mt-3 overflow-hidden rounded-md border border-line bg-paper-2">
            {starting ? (
              <div className="flex h-64 items-center justify-center text-[14px] text-muted">
                {t('brokerTakeoverLoading')}
              </div>
            ) : frameUrl ? (
              // eslint-disable-next-line @next/next/no-img-element
              <img
                ref={imgRef}
                src={frameUrl}
                alt={t('brokerTakeoverViewportAlt')}
                onClick={(event) => void handleViewportClick(event)}
                className="max-h-[340px] w-full cursor-crosshair object-contain"
              />
            ) : (
              <div className="flex h-64 flex-col items-center justify-center gap-2 text-[14px] text-muted">
                <Monitor className="h-6 w-6" aria-hidden="true" />
                <span>{summary.targetOrigin}</span>
              </div>
            )}
          </div>

          <form
            onSubmit={(event) => void handleSendText(event)}
            className="mt-3 flex flex-wrap items-end gap-2"
          >
            <div className="min-w-[220px] flex-1">
              <label
                htmlFor={textInputId}
                className="block text-[13px] font-bold text-ink"
              >
                {t('brokerTakeoverTextLabel')}
              </label>
              <input
                id={textInputId}
                type="text"
                autoComplete="off"
                value={typedText}
                disabled={!canDecide || busy || starting}
                onChange={(event) => setTypedText(event.target.value)}
                placeholder={t('brokerTakeoverTextPlaceholder')}
                className="mt-1 min-h-[44px] w-full rounded-sm border-[1.5px] border-line-strong bg-paper px-3 font-mono text-[14px] text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
              />
            </div>
            <Button
              type="submit"
              variant="secondary"
              disabled={!canDecide || busy || starting || !typedText}
              className="min-h-[44px]"
            >
              {t('brokerTakeoverSendText')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={!canDecide || busy || starting}
              onClick={() => void sendInput({ kind: 'key', key: 'Enter' })}
              className="min-h-[44px]"
            >
              <CornerDownLeft className="h-4 w-4" aria-hidden="true" />
              <span>{t('brokerTakeoverSendEnter')}</span>
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={!canDecide || busy || starting}
              onClick={() =>
                void loadFrame(summary.secretRequestId, state?.takeoverTicket)
              }
              className="min-h-[44px]"
            >
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
              <span>{t('brokerTakeoverRefreshFrame')}</span>
            </Button>
          </form>

          {errorKey ? (
            <Alert tone="warning" className="mt-3">
              {errors(errorKey)}
            </Alert>
          ) : null}

          <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-line pt-4">
            <Button
              type="button"
              variant="secondary"
              disabled={busy}
              onClick={onClose}
              className="min-h-[44px]"
            >
              {t('secretModalCancel')}
            </Button>
            <Button
              type="button"
              disabled={!canDecide || busy || starting}
              onClick={() => void handleComplete()}
              className="min-h-[44px]"
            >
              <CheckCircle2 className="h-4 w-4" aria-hidden="true" />
              <span>
                {busy
                  ? t('brokerTakeoverCompleting')
                  : t('brokerTakeoverComplete')}
              </span>
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
