'use client';

import { Ban, Plus, RefreshCw, X } from 'lucide-react';
import { useTranslations } from 'next-intl';
import type { KeyboardEvent, RefObject } from 'react';

import type { useBotEgressPolicy } from '@/components/bots/useBotEgressPolicy';
import { Alert, Button, Card, Input, RadioCardGroup } from '@/components/ui';
import type { EgressPolicyMode } from '@/types/api';

/** Displays saved egress policy status and edits the outbound policy draft. */
export function BotEgressPolicyPanel({
  editor,
  canEditBotSecrets,
  blockedSectionRef,
}: {
  editor: ReturnType<typeof useBotEgressPolicy>;
  canEditBotSecrets: boolean;
  blockedSectionRef: RefObject<HTMLDivElement | null>;
}) {
  const t = useTranslations('secrets');
  const tErrors = useTranslations('errors');
  const {
    policy,
    policyMode,
    changePolicyMode,
    allowedHosts,
    blockedHosts,
    hostDraft,
    setHostDraft,
    blockedHostDraft,
    setBlockedHostDraft,
    savingPolicy,
    policyMutationInFlight,
    policyStatusNotice,
    policyErrorKey,
    draftConflictPrompt,
    policyStateUnknown,
    reloadingPolicy,
    handleLoadSavedDraft,
    handleReloadPolicy,
    handleSavePolicy,
    addHost,
    removeHost,
    dismissDraftConflictPrompt,
  } = editor;
  const effectiveSecretHosts = policy?.effectiveSecretHosts ?? [];

  function handleHostInputKeyDown(
    event: KeyboardEvent<HTMLInputElement>,
    operation: 'allow' | 'block',
  ) {
    if (!event.nativeEvent.isComposing && event.key === 'Enter') {
      event.preventDefault();
      addHost(operation);
    }
  }

  return (
    <section
      aria-labelledby="outbound-policy-heading"
      className="flex min-w-0 flex-col gap-4"
    >
      <div className="flex flex-col gap-1">
        <h3
          id="outbound-policy-heading"
          className="text-[18px] font-black tracking-[-0.01em] text-ink"
        >
          {t('policyTitle')}
        </h3>
        <p className="text-[14px] leading-[1.55] text-ink-2">
          {t('policyLead')}
        </p>
      </div>

      {policyStateUnknown ? (
        <div role="alert" className="flex flex-col gap-3">
          <Alert tone="warning">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span>{t('policyStateUnknownBanner')}</span>
              <Button
                type="button"
                variant="secondary"
                disabled={reloadingPolicy}
                onClick={() => void handleReloadPolicy()}
              >
                <RefreshCw className="h-4 w-4" aria-hidden="true" />
                <span>{t('policyReloadAction')}</span>
              </Button>
            </div>
          </Alert>
        </div>
      ) : null}

      {policyStatusNotice ? (
        <div role="status">
          <Alert
            tone={
              policyStatusNotice.kind === 'pending' ||
              policyStatusNotice.kind === 'reconciled' ||
              policyStatusNotice.kind === 'undo_conflict'
                ? 'warning'
                : 'success'
            }
          >
            {policyStatusNotice.kind === 'applied'
              ? t('policySavedBanner')
              : policyStatusNotice.kind === 'pending'
                ? t('policyPendingBanner', {
                    revision: policyStatusNotice.revision,
                  })
                : policyStatusNotice.kind === 'not_running'
                  ? t('policyNotRunningBanner', {
                      revision: policyStatusNotice.revision,
                    })
                  : policyStatusNotice.kind === 'reconciled'
                    ? t('policyReconciledNotice', {
                        revision: policyStatusNotice.revision,
                      })
                    : policyStatusNotice.kind === 'undo_complete'
                      ? t('triageUndoComplete', {
                          host: policyStatusNotice.host,
                        })
                      : t('triageUndoConflict', {
                          host: policyStatusNotice.host,
                        })}
          </Alert>
        </div>
      ) : null}

      {policyErrorKey ? (
        <div role="alert">
          <Alert tone="warning">{tErrors(policyErrorKey)}</Alert>
        </div>
      ) : null}

      {draftConflictPrompt && policy ? (
        <div role="status">
          <Alert tone="warning">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span>
                {t('policyDraftConflictBanner', {
                  revision: policy.revision,
                })}
              </span>
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  variant="secondary"
                  onClick={dismissDraftConflictPrompt}
                >
                  {t('policyDraftKeep')}
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={handleLoadSavedDraft}
                >
                  {t('policyDraftRebase')}
                </Button>
              </div>
            </div>
          </Alert>
        </div>
      ) : null}

      <Card className="p-6">
        <form
          aria-labelledby="outbound-policy-heading"
          onSubmit={(event) => void handleSavePolicy(event)}
          className="flex flex-col gap-6"
        >
          <RadioCardGroup<EgressPolicyMode>
            legend={t('policyTitle')}
            name="egress-policy-mode"
            value={policyMode}
            onChange={changePolicyMode}
            disabled={
              !canEditBotSecrets || policyMutationInFlight || policyStateUnknown
            }
            options={[
              {
                value: 'allowlist',
                title: t('policyAllowlistTitle'),
                description: t('policyAllowlistDesc'),
              },
              {
                value: 'open',
                title: t('policyOpenTitle'),
                description: t('policyOpenDesc'),
              },
            ]}
          />

          <div className="flex flex-col gap-3">
            <h4 className="text-[15px] font-bold text-ink">
              {t('allowedHostsTitle')}
            </h4>

            <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
              <div className="flex-1">
                <Input
                  name="allowedHostDraft"
                  label={t('hostInputLabel')}
                  placeholder={t('hostInputPlaceholder')}
                  mono
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={hostDraft}
                  disabled={
                    !canEditBotSecrets ||
                    policyMutationInFlight ||
                    policyStateUnknown
                  }
                  onKeyDown={(event) => handleHostInputKeyDown(event, 'allow')}
                  onChange={(event) => setHostDraft(event.target.value)}
                />
              </div>
              <Button
                type="button"
                variant="secondary"
                disabled={
                  !canEditBotSecrets ||
                  policyMutationInFlight ||
                  policyStateUnknown ||
                  !hostDraft.trim()
                }
                onClick={() => addHost('allow')}
              >
                <Plus className="h-4 w-4" aria-hidden="true" />
                <span>{t('addHostAction')}</span>
              </Button>
            </div>

            {allowedHosts.length === 0 ? (
              <p className="text-[14px] text-muted">{t('emptyHosts')}</p>
            ) : (
              <ul className="flex flex-wrap gap-2">
                {allowedHosts.map((host) => (
                  <li
                    key={host}
                    className="inline-flex min-h-[44px] items-center gap-1 rounded-sm border border-line bg-paper-2 pl-3 pr-0.5 font-mono text-[13px] text-ink"
                  >
                    <span>{host}</span>
                    {canEditBotSecrets ? (
                      <button
                        type="button"
                        aria-label={t('removeHostAria', { host })}
                        disabled={policyMutationInFlight || policyStateUnknown}
                        onClick={() => removeHost('allow', host)}
                        className="inline-flex h-11 w-11 items-center justify-center rounded-xs text-ink-2 transition hover:bg-mint-soft hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                      >
                        <X className="h-3.5 w-3.5" aria-hidden="true" />
                      </button>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div
            ref={blockedSectionRef}
            id="blocked-hosts-section"
            className="flex flex-col gap-3 border-t border-line pt-4"
          >
            <div className="flex flex-col gap-1">
              <h4 className="text-[15px] font-bold text-ink">
                {t('blockedHostsTitle')}
              </h4>
              <p className="text-[13px] leading-[1.45] text-muted">
                {t('blockedHostsDesc')}
              </p>
            </div>

            <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
              <div className="flex-1">
                <Input
                  name="blockedHostDraft"
                  label={t('blockedHostInputLabel')}
                  placeholder={t('blockedHostInputPlaceholder')}
                  mono
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={blockedHostDraft}
                  disabled={
                    !canEditBotSecrets ||
                    policyMutationInFlight ||
                    policyStateUnknown
                  }
                  onKeyDown={(event) => handleHostInputKeyDown(event, 'block')}
                  onChange={(event) => setBlockedHostDraft(event.target.value)}
                />
              </div>
              <Button
                type="button"
                variant="secondary"
                disabled={
                  !canEditBotSecrets ||
                  policyMutationInFlight ||
                  policyStateUnknown ||
                  !blockedHostDraft.trim()
                }
                onClick={() => addHost('block')}
              >
                <Ban className="h-4 w-4" aria-hidden="true" />
                <span>{t('blockHostAction')}</span>
              </Button>
            </div>

            {blockedHosts.length === 0 ? (
              <p className="text-[14px] text-muted">{t('emptyBlockedHosts')}</p>
            ) : (
              <ul className="flex flex-wrap gap-2">
                {blockedHosts.map((host) => (
                  <li
                    key={host}
                    className="inline-flex min-h-[44px] items-center gap-1 rounded-sm border border-dashed border-amber-line bg-amber-tint pl-3 pr-0.5 font-mono text-[13px] font-bold text-amber"
                  >
                    <span>{host}</span>
                    {canEditBotSecrets ? (
                      <button
                        type="button"
                        aria-label={t('removeBlockedHostAria', {
                          host,
                        })}
                        disabled={policyMutationInFlight || policyStateUnknown}
                        onClick={() => removeHost('block', host)}
                        className="inline-flex h-11 w-11 items-center justify-center rounded-xs text-amber transition hover:bg-paper/80 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                      >
                        <X className="h-3.5 w-3.5" aria-hidden="true" />
                      </button>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="flex flex-col gap-2 border-t border-line pt-4">
            <h4 className="text-[14px] font-bold text-ink">
              {t('effectiveSecretHostsTitle')}
            </h4>
            {effectiveSecretHosts.length === 0 ? (
              <p className="text-[14px] text-muted">
                {t('effectiveSecretHostsEmpty')}
              </p>
            ) : (
              <ul className="flex flex-wrap gap-2">
                {effectiveSecretHosts.map((host) => (
                  <li
                    key={host}
                    className="rounded-xs border border-mint-line bg-mint-soft px-2.5 py-1 font-mono text-[13px] font-bold text-green-600"
                  >
                    {host}
                  </li>
                ))}
              </ul>
            )}
          </div>

          {canEditBotSecrets ? (
            <div className="flex justify-end border-t border-line pt-4">
              <Button
                type="submit"
                arrow
                disabled={
                  savingPolicy || policyMutationInFlight || policyStateUnknown
                }
              >
                {savingPolicy ? t('savingPolicy') : t('savePolicyAction')}
              </Button>
            </div>
          ) : null}
        </form>
      </Card>
    </section>
  );
}
