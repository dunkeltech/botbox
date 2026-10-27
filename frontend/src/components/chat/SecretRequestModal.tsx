'use client';

import { Eye, EyeOff, Lock, ShieldCheck, X } from 'lucide-react';
import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Button } from '@/components/ui';
import { ApiClientError, requestJson } from '@/lib/api-client';
import { resolveErrorKey, type ErrorTranslationKey } from '@/lib/errors';
import type { SecretRequestSummary } from '@/components/chat/SecretRequestCard';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type SecretScope = components['schemas']['SecretRequestScope'];
type SecretRequestResult = components['schemas']['SecretRequestResult'];

/**
 * Write-only modal dialog for fulfilling or denying an in-chat secret request
 * without writing credential values to chat history or AI context.
 */
export function SecretRequestModal({
  workspaceId,
  botName,
  role,
  summary,
  onClose,
  onCompleted,
  onOpenTakeover,
}: {
  workspaceId: string;
  botName: string;
  role: WorkspaceRole;
  summary: SecretRequestSummary | null;
  onClose: () => void;
  onCompleted: () => void | Promise<void>;
  onOpenTakeover?: (summary: SecretRequestSummary) => void;
}) {
  const t = useTranslations('chat');
  const errors = useTranslations('errors');
  const titleId = useId();
  const secretInputId = useId();
  const userInputId = useId();
  const replaceInputId = useId();
  const secretInputRef = useRef<HTMLInputElement>(null);

  const [secretValue, setSecretValue] = useState('');
  const [username, setUsername] = useState('');
  const [reveal, setReveal] = useState(false);
  const [scope, setScope] = useState<SecretScope>('ephemeral');
  const [replaceExisting, setReplaceExisting] = useState(false);
  const [showReplaceOption, setShowReplaceOption] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [mfaRequired, setMfaRequired] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  const canWorkspaceScope = role === 'owner' || role === 'admin';
  const canDecide = role !== 'viewer';

  function clearSensitiveFields() {
    setSecretValue('');
    setUsername('');
    setReveal(false);
    setReplaceExisting(false);
    setShowReplaceOption(false);
    setMfaRequired(false);
    if (secretInputRef.current) {
      secretInputRef.current.value = '';
    }
  }

  useEffect(() => {
    setErrorKey(null);
    setScope('ephemeral');
    clearSensitiveFields();
    if (summary) {
      requestAnimationFrame(() => secretInputRef.current?.focus());
    }
    return () => {
      clearSensitiveFields();
    };
  }, [summary]);

  if (!summary) return null;

  const isBrowser = summary.kind === 'browser_login';
  const isBrowserBroker = isBrowser && Boolean(summary.brokerEnabled);
  const isUnsupported = isBrowser && !summary.brokerEnabled;

  function handleClose() {
    clearSensitiveFields();
    setErrorKey(null);
    onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape' && !submitting) {
      event.preventDefault();
      handleClose();
    }
  }

  async function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (!summary || isUnsupported || !canDecide || !secretValue.trim()) return;
    setSubmitting(true);
    setErrorKey(null);
    try {
      const res = await requestJson<SecretRequestResult>(
        `/api/workspaces/${workspaceId}/secret-requests/${summary.secretRequestId}/fulfill`,
        {
          method: 'POST',
          body: {
            secretValue,
            username: username.trim() || undefined,
            scope,
            replaceExisting,
          },
        },
      );
      clearSensitiveFields();
      await onCompleted();
      if (res.mfaRequired) {
        if (onOpenTakeover) {
          onClose();
          onOpenTakeover(res.secretRequest);
          return;
        }
        setMfaRequired(true);
        return;
      }
      onClose();
    } catch (err) {
      if (
        err instanceof ApiClientError &&
        err.code === 'secret_request.rule_conflict'
      ) {
        setShowReplaceOption(true);
      }
      // Clear held secret on failure as well so stale values are never retained.
      setSecretValue('');
      setReveal(false);
      if (secretInputRef.current) {
        secretInputRef.current.value = '';
      }
      setErrorKey(resolveErrorKey(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function handleDeny() {
    if (!summary || !canDecide || submitting) return;
    setSubmitting(true);
    setErrorKey(null);
    try {
      await requestJson<SecretRequestResult>(
        `/api/workspaces/${workspaceId}/secret-requests/${summary.secretRequestId}/deny`,
        { method: 'POST' },
      );
      clearSensitiveFields();
      await onCompleted();
      onClose();
    } catch (err) {
      clearSensitiveFields();
      setErrorKey(resolveErrorKey(err));
    } finally {
      setSubmitting(false);
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
        className="flex max-h-[92vh] w-full max-w-[640px] flex-col overflow-hidden rounded-lg border border-line bg-paper shadow-card-hover"
      >
        <div className="flex items-start justify-between gap-4 border-b border-line px-6 py-4">
          <div>
            <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
              <span className="mr-1 font-black text-green">{'//'}</span>
              {t('secretModalEyebrow')}
            </p>
            <h2
              id={titleId}
              className="mt-1 text-[22px] font-black leading-tight text-ink"
            >
              {t('secretModalTitle', { name: botName })}
            </h2>
          </div>
          <button
            type="button"
            aria-label={t('secretModalClose')}
            onClick={handleClose}
            disabled={submitting}
            className="grid h-11 w-11 shrink-0 place-items-center rounded-sm border border-line text-ink-2 transition-colors hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <X className="h-5 w-5" aria-hidden="true" />
          </button>
        </div>

        <form
          onSubmit={(event) => void handleSubmit(event)}
          className="flex flex-col overflow-y-auto px-6 py-4"
        >
          <div className="flex items-start gap-2.5 rounded-sm border border-mint-line bg-mint-soft px-3.5 py-2.5 text-[13px] text-ink">
            <ShieldCheck
              className="mt-0.5 h-4 w-4 shrink-0 text-green-600"
              aria-hidden="true"
            />
            <span>
              {t(
                isBrowserBroker
                  ? 'secretModalBrowserBanner'
                  : 'secretModalBanner',
              )}
            </span>
          </div>

          <div className="mt-4">
            <div className="flex items-center justify-between gap-2">
              <span className="text-[13px] font-bold text-ink">
                {t('secretModalAdapterLabel')}
              </span>
              <span className="rounded-pill border border-mint-line bg-mint-soft px-2.5 py-0.5 text-[11px] font-bold text-green-600">
                {t('secretModalReviewedBadge')}
              </span>
            </div>
            <div className="mt-1.5 space-y-1 rounded-sm border border-line bg-paper-2 px-3.5 py-2.5 font-mono text-[12px] text-ink-2">
              <div className="font-bold text-ink">
                {summary.adapterLabel || summary.adapterId} ({summary.adapterId}
                )
              </div>
              <div>
                {t('secretModalDestination', { origin: summary.targetOrigin })}
              </div>
              {summary.headerScheme ? (
                <div>
                  {t('secretModalScheme', { scheme: summary.headerScheme })}
                </div>
              ) : null}
              {summary.envPlaceholderName && summary.placeholderValue ? (
                <div>
                  {t('secretModalPlaceholder', {
                    env: summary.envPlaceholderName,
                    value: summary.placeholderValue,
                  })}
                </div>
              ) : null}
              <div className="font-sans text-[13px] text-ink-2">
                {t('secretModalReason', { reason: summary.reason })}
              </div>
            </div>
          </div>

          {isUnsupported ? (
            <Alert tone="warning" className="mt-4">
              {t('secretBrowserUnsupported')}
            </Alert>
          ) : (
            <>
              <div className="mt-4">
                <label
                  htmlFor={userInputId}
                  className="block text-[14px] font-bold text-ink"
                >
                  {t('secretModalUsernameLabel')}
                </label>
                <input
                  id={userInputId}
                  type="text"
                  autoComplete="off"
                  value={username}
                  disabled={!canDecide || submitting}
                  onChange={(event) => setUsername(event.target.value)}
                  className="mt-1.5 min-h-[44px] w-full rounded-sm border-[1.5px] border-line-strong bg-paper px-3 font-mono text-[14px] text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:bg-paper-2"
                />
              </div>

              <div className="mt-4">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <label
                    htmlFor={secretInputId}
                    className="text-[14px] font-bold text-ink"
                  >
                    {t('secretModalSecretLabel')}
                  </label>
                  <span className="rounded-xs border border-mint-line bg-mint-soft px-2 py-0.5 font-mono text-[11px] font-bold text-green-600">
                    {t('secretModalWriteOnlyBadge')}
                  </span>
                </div>
                <div className="mt-1.5 flex items-center gap-2">
                  <input
                    ref={secretInputRef}
                    id={secretInputId}
                    type={reveal ? 'text' : 'password'}
                    autoComplete="off"
                    required
                    value={secretValue}
                    disabled={!canDecide || submitting}
                    onChange={(event) => setSecretValue(event.target.value)}
                    className="min-h-[44px] flex-1 rounded-sm border-[1.5px] border-line-strong bg-paper px-3 font-mono text-[14px] text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:bg-paper-2"
                  />
                  <button
                    type="button"
                    aria-label={
                      reveal
                        ? t('secretModalHideSecret')
                        : t('secretModalShowSecret')
                    }
                    aria-pressed={reveal}
                    onClick={() => setReveal((v) => !v)}
                    disabled={!canDecide || submitting}
                    className="grid h-11 w-11 shrink-0 place-items-center rounded-sm border border-line bg-paper text-ink-2 hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                  >
                    {reveal ? (
                      <EyeOff className="h-4 w-4" aria-hidden="true" />
                    ) : (
                      <Eye className="h-4 w-4" aria-hidden="true" />
                    )}
                  </button>
                </div>
                <p className="mt-1 text-[13px] text-muted">
                  {t('secretModalClearHelper')}
                </p>
              </div>

              <fieldset className="mt-4 space-y-2">
                <legend className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                  {t('secretModalScopeHeading')}
                </legend>
                {(isBrowserBroker
                  ? [
                      {
                        value: 'ephemeral' as const,
                        title: t('secretModalBrowserScope'),
                        desc: t('secretModalBrowserScopeDesc'),
                        disabled: false,
                      },
                    ]
                  : [
                      {
                        value: 'ephemeral' as const,
                        title: t('secretModalScopeEphemeral'),
                        desc: t('secretModalScopeEphemeralDesc'),
                        disabled: false,
                      },
                      {
                        value: 'bot_secret' as const,
                        title: t('secretModalScopeBot', { name: botName }),
                        desc: t('secretModalScopeBotDesc'),
                        disabled: false,
                      },
                      {
                        value: 'workspace_secret' as const,
                        title: t('secretModalScopeWorkspace'),
                        desc: canWorkspaceScope
                          ? t('secretModalScopeWorkspaceDesc')
                          : t('secretModalWorkspaceRestricted'),
                        disabled: !canWorkspaceScope,
                      },
                    ]
                ).map((option) => {
                  const selected = scope === option.value;
                  return (
                    <label
                      key={option.value}
                      className={`flex cursor-pointer items-start gap-3 rounded-sm border-2 p-3 transition-colors ${
                        option.disabled
                          ? 'cursor-not-allowed border-line bg-paper-2 opacity-60'
                          : selected
                            ? 'border-green bg-mint-soft'
                            : 'border-line bg-paper hover:border-line-strong'
                      }`}
                    >
                      <input
                        type="radio"
                        name="secret-scope"
                        value={option.value}
                        checked={selected}
                        disabled={option.disabled || !canDecide || submitting}
                        onChange={() => setScope(option.value)}
                        className="mt-1 h-4 w-4 accent-green-600"
                      />
                      <span className="min-w-0 flex-1">
                        <span className="block text-[14px] font-bold text-ink">
                          {option.title}
                        </span>
                        <span className="block text-[13px] text-ink-2">
                          {option.desc}
                        </span>
                      </span>
                    </label>
                  );
                })}
              </fieldset>

              {showReplaceOption ? (
                <label
                  htmlFor={replaceInputId}
                  className="mt-3 flex min-h-[44px] cursor-pointer items-center gap-2.5 rounded-sm border border-amber-line bg-amber-tint px-3 py-2 text-[13px] font-bold text-amber"
                >
                  <input
                    id={replaceInputId}
                    type="checkbox"
                    checked={replaceExisting}
                    onChange={(event) =>
                      setReplaceExisting(event.target.checked)
                    }
                    className="h-4 w-4 accent-amber"
                  />
                  <span>{t('secretModalReplaceLabel')}</span>
                </label>
              ) : null}

              <div className="mt-4 flex items-start gap-2 rounded-sm border border-mint-line bg-paper-2 px-3 py-2 text-[12px] text-ink-2">
                <Lock
                  className="mt-0.5 h-3.5 w-3.5 shrink-0 text-green-600"
                  aria-hidden="true"
                />
                <span>
                  {t(
                    isBrowserBroker
                      ? 'secretModalEnforcedBrowserNote'
                      : 'secretModalEnforcedNote',
                  )}
                </span>
              </div>
            </>
          )}

          {mfaRequired ? (
            <Alert tone="warning" className="mt-3">
              {t('secretModalMfaPrompt')}
            </Alert>
          ) : null}

          {errorKey ? (
            <Alert tone="warning" className="mt-3">
              {errors(errorKey)}
            </Alert>
          ) : null}

          <div className="mt-5 flex flex-wrap items-center justify-between gap-2 border-t border-line pt-4">
            {canDecide ? (
              <Button
                type="button"
                variant="secondary"
                disabled={submitting}
                onClick={() => void handleDeny()}
                className="min-h-[44px] border-amber-line text-amber hover:border-amber hover:bg-amber-tint"
              >
                {t('secretModalDeny')}
              </Button>
            ) : (
              <span className="text-[12px] font-bold text-muted">
                {t('secretViewerHint')}
              </span>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="secondary"
                disabled={submitting}
                onClick={handleClose}
                className="min-h-[44px]"
              >
                {t('secretModalCancel')}
              </Button>
              {isBrowserBroker && onOpenTakeover && canDecide ? (
                <Button
                  type="button"
                  variant="secondary"
                  disabled={submitting}
                  onClick={() => {
                    const current = summary;
                    handleClose();
                    onOpenTakeover(current);
                  }}
                  className="min-h-[44px]"
                >
                  {t('secretModalSwitchTakeover')}
                </Button>
              ) : null}
              {!isUnsupported && canDecide ? (
                <Button
                  type="submit"
                  disabled={submitting || !secretValue.trim()}
                  className="min-h-[44px]"
                >
                  <Lock className="h-4 w-4" aria-hidden="true" />
                  <span>
                    {submitting
                      ? t('secretModalSubmitting')
                      : t(
                          isBrowserBroker
                            ? 'secretModalSubmitBrowser'
                            : 'secretModalSubmit',
                        )}
                  </span>
                </Button>
              ) : null}
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}
