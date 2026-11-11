'use client';

import { Lock, Pencil, Shield, Trash2 } from 'lucide-react';
import { type FormEvent, useCallback, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  SegmentedControl,
} from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canManageMembers } from '@/lib/rbac';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type WorkspaceGuardrailsResponse =
  components['schemas']['WorkspaceGuardrailsResponse'];
type WorkspaceGuardrailPolicy =
  components['schemas']['WorkspaceGuardrailPolicy'];
type WorkspaceGuardrailRule = components['schemas']['WorkspaceGuardrailRule'];
type GuardrailDirection = components['schemas']['GuardrailDirection'];
type GuardrailAction = components['schemas']['GuardrailAction'];
type GuardrailFailureMode = components['schemas']['GuardrailFailureMode'];
type GuardrailMatchMode = components['schemas']['GuardrailMatchMode'];
type GuardrailPIIEntity = components['schemas']['GuardrailPIIEntity'];
type GuardrailEvent = components['schemas']['GuardrailEvent'];
type GuardrailEventListResponse =
  components['schemas']['GuardrailEventListResponse'];
type GuardrailEventAction = components['schemas']['GuardrailEventAction'];
type TestGuardrailRuleResponse =
  components['schemas']['TestGuardrailRuleResponse'];

const ALL_PII_ENTITIES: ReadonlyArray<GuardrailPIIEntity> = [
  'email',
  'iban',
  'card',
  'phone',
];

function slugifyRuleName(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64);
}

function actionPillClass(action: GuardrailAction): string {
  switch (action) {
    case 'block':
      return 'border-dashed border-amber-line bg-amber-tint text-amber';
    case 'redact':
      return 'border-blue-line bg-blue-tint text-blue';
    case 'audit':
      return 'border-mint-line bg-mint-soft text-green-600';
  }
}

function ToggleSwitch({
  checked,
  disabled,
  ariaLabel,
  onToggle,
}: {
  checked: boolean;
  disabled: boolean;
  ariaLabel: string;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={onToggle}
      className={`relative inline-flex h-[30px] w-[52px] shrink-0 items-center rounded-pill p-0.5 transition-colors duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
        checked ? 'bg-green' : 'bg-on-dark-muted'
      }`}
    >
      <span
        aria-hidden="true"
        className={`h-6 w-6 rounded-full bg-paper shadow transition-transform duration-150 ${
          checked ? 'translate-x-[22px]' : 'translate-x-0'
        }`}
      />
    </button>
  );
}

function DetectorDirectionControls({
  value,
  disabled,
  ariaLabel,
  onChange,
  labels,
}: {
  value: GuardrailDirection;
  disabled: boolean;
  ariaLabel: string;
  onChange: (next: GuardrailDirection) => void;
  labels: { input: string; output: string; both: string };
}) {
  const options: Array<{ value: GuardrailDirection; label: string }> = [
    { value: 'input', label: labels.input },
    { value: 'output', label: labels.output },
    { value: 'both', label: labels.both },
  ];
  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className="inline-flex flex-wrap items-center gap-1 rounded-md border border-line bg-paper-2 p-1"
    >
      {options.map((opt) => {
        const active = value === opt.value;
        return (
          <button
            key={opt.value}
            type="button"
            disabled={disabled}
            aria-pressed={active}
            onClick={() => onChange(opt.value)}
            className={`min-h-[38px] rounded-xs px-2.5 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
              active
                ? 'bg-dark text-paper'
                : 'text-ink-2 hover:bg-paper hover:text-ink'
            }`}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}

function DetectorActionControls({
  value,
  disabled,
  ariaLabel,
  onChange,
  labels,
}: {
  value: GuardrailAction;
  disabled: boolean;
  ariaLabel: string;
  onChange: (next: GuardrailAction) => void;
  labels: { block: string; redact: string; audit: string };
}) {
  const options: Array<{ value: GuardrailAction; label: string }> = [
    { value: 'block', label: labels.block },
    { value: 'redact', label: labels.redact },
    { value: 'audit', label: labels.audit },
  ];
  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className="inline-flex flex-wrap items-center gap-1 rounded-md border border-line bg-paper-2 p-1"
    >
      {options.map((opt) => {
        const active = value === opt.value;
        return (
          <button
            key={opt.value}
            type="button"
            disabled={disabled}
            aria-pressed={active}
            onClick={() => onChange(opt.value)}
            className={`min-h-[38px] rounded-xs border px-2.5 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
              active
                ? actionPillClass(opt.value)
                : 'border-transparent text-ink-2 hover:bg-paper hover:text-ink'
            }`}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}

export interface LLMGuardrailsPanelProps {
  workspaceId: string;
  role: WorkspaceRole;
}

/**
 * Workspace-wide LLM proxy guardrail management panel: enforcement policy,
 * managed built-in detectors, custom RE2/keyword rules with Quick Test, and
 * privacy-safe llmproxy audit log.
 */
export function LLMGuardrailsPanel({
  workspaceId,
  role,
}: LLMGuardrailsPanelProps) {
  const t = useTranslations('llmGuardrails');
  const tErrors = useTranslations('errors');

  const canManage = canManageMembers(role);

  const [data, setData] = useState<WorkspaceGuardrailsResponse | null>(null);
  const [policyDraft, setPolicyDraft] =
    useState<WorkspaceGuardrailPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadErrorKey, setLoadErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [policySaving, setPolicySaving] = useState(false);
  const [policyBanner, setPolicyBanner] = useState<string | null>(null);
  const [policyErrorKey, setPolicyErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  const [editingRule, setEditingRule] = useState<WorkspaceGuardrailRule | null>(
    null,
  );
  const [ruleName, setRuleName] = useState('');
  const [ruleSlug, setRuleSlug] = useState('');
  const [slugTouched, setSlugTouched] = useState(false);
  const [matchMode, setMatchMode] = useState<GuardrailMatchMode>('regex');
  const [pattern, setPattern] = useState('');
  const [direction, setDirection] = useState<GuardrailDirection>('both');
  const [action, setAction] = useState<GuardrailAction>('redact');
  const [replacementToken, setReplacementToken] = useState('[REDACTED]');
  const [ruleSubmitting, setRuleSubmitting] = useState(false);
  const [ruleBanner, setRuleBanner] = useState<string | null>(null);
  const [ruleErrorKey, setRuleErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [busyRuleId, setBusyRuleId] = useState<string | null>(null);

  const [sampleText, setSampleText] = useState('');
  const [testBusy, setTestBusy] = useState(false);
  const [testResult, setTestResult] =
    useState<TestGuardrailRuleResponse | null>(null);

  const [events, setEvents] = useState<GuardrailEvent[]>([]);
  const [eventsLoading, setEventsLoading] = useState(true);
  const [actionFilter, setActionFilter] = useState<'' | GuardrailEventAction>(
    '',
  );
  const [directionFilter, setDirectionFilter] = useState<
    '' | 'input' | 'output'
  >('');

  const resetRuleForm = useCallback(() => {
    setEditingRule(null);
    setRuleName('');
    setRuleSlug('');
    setSlugTouched(false);
    setMatchMode('regex');
    setPattern('');
    setDirection('both');
    setAction('redact');
    setReplacementToken('[REDACTED]');
    setRuleErrorKey(null);
    setTestResult(null);
  }, []);

  const loadGuardrails = useCallback(async () => {
    setLoading(true);
    setLoadErrorKey(null);
    try {
      const res = await requestJson<WorkspaceGuardrailsResponse>(
        `/api/workspaces/${workspaceId}/guardrails`,
      );
      setData(res);
      setPolicyDraft(res.policy);
    } catch (err) {
      setLoadErrorKey(resolveErrorKey(err));
    } finally {
      setLoading(false);
    }
  }, [workspaceId]);

  const loadEvents = useCallback(async () => {
    setEventsLoading(true);
    try {
      const params = new URLSearchParams();
      if (actionFilter) params.set('action', actionFilter);
      if (directionFilter) params.set('direction', directionFilter);
      const query = params.toString();
      const res = await requestJson<GuardrailEventListResponse>(
        `/api/workspaces/${workspaceId}/guardrails/events${query ? `?${query}` : ''}`,
      );
      setEvents(res.events ?? []);
    } catch {
      setEvents([]);
    } finally {
      setEventsLoading(false);
    }
  }, [actionFilter, directionFilter, workspaceId]);

  useEffect(() => {
    resetRuleForm();
    void loadGuardrails();
  }, [loadGuardrails, resetRuleForm]);

  useEffect(() => {
    void loadEvents();
  }, [loadEvents]);

  async function savePolicy(nextPolicy: WorkspaceGuardrailPolicy) {
    if (!canManage || policySaving) return;
    setPolicySaving(true);
    setPolicyErrorKey(null);
    setPolicyBanner(null);
    try {
      const updated = await requestJson<WorkspaceGuardrailPolicy>(
        `/api/workspaces/${workspaceId}/guardrails/policy`,
        {
          method: 'PUT',
          body: {
            enabled: nextPolicy.enabled,
            failureMode: nextPolicy.failureMode,
            secrets: nextPolicy.secrets,
            pii: nextPolicy.pii,
            promptInjection: nextPolicy.promptInjection,
          },
        },
      );
      setPolicyDraft(updated);
      setPolicyBanner(t('savedPolicy'));
      await loadGuardrails();
    } catch (err) {
      setPolicyErrorKey(resolveErrorKey(err));
    } finally {
      setPolicySaving(false);
    }
  }

  function updateDraft(
    updater: (prev: WorkspaceGuardrailPolicy) => WorkspaceGuardrailPolicy,
  ) {
    setPolicyDraft((prev) => (prev ? updater(prev) : prev));
    setPolicyBanner(null);
  }

  function togglePIIEntity(entity: GuardrailPIIEntity) {
    if (!canManage) return;
    updateDraft((prev) => {
      const exists = prev.pii.entities.includes(entity);
      const nextEntities = exists
        ? prev.pii.entities.filter((item) => item !== entity)
        : [...prev.pii.entities, entity];
      return {
        ...prev,
        pii: {
          ...prev.pii,
          entities: nextEntities,
        },
      };
    });
  }

  async function handleQuickTest() {
    if (!canManage || testBusy || !pattern.trim()) return;
    setTestBusy(true);
    setRuleErrorKey(null);
    try {
      const res = await requestJson<TestGuardrailRuleResponse>(
        `/api/workspaces/${workspaceId}/guardrails/test`,
        {
          method: 'POST',
          body: {
            matchMode,
            pattern: pattern.trim(),
            replacementToken: replacementToken.trim(),
            sampleText,
          },
        },
      );
      setTestResult(res);
    } catch (err) {
      setRuleErrorKey(resolveErrorKey(err));
    } finally {
      setTestBusy(false);
    }
  }

  async function handleRuleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canManage || ruleSubmitting) return;
    setRuleSubmitting(true);
    setRuleErrorKey(null);
    setRuleBanner(null);

    try {
      if (editingRule) {
        await requestJson<WorkspaceGuardrailRule>(
          `/api/workspaces/${workspaceId}/guardrails/rules/${editingRule.id}`,
          {
            method: 'PATCH',
            body: {
              name: ruleName.trim(),
              matchMode,
              pattern: pattern.trim(),
              direction,
              action,
              replacementToken: replacementToken.trim(),
            },
          },
        );
      } else {
        await requestJson<WorkspaceGuardrailRule>(
          `/api/workspaces/${workspaceId}/guardrails/rules`,
          {
            method: 'POST',
            body: {
              slug: ruleSlug.trim(),
              name: ruleName.trim(),
              matchMode,
              pattern: pattern.trim(),
              direction,
              action,
              replacementToken: replacementToken.trim(),
              enabled: true,
            },
          },
        );
      }
      resetRuleForm();
      setRuleBanner(t('ruleSaved'));
      await loadGuardrails();
    } catch (err) {
      setRuleErrorKey(resolveErrorKey(err));
    } finally {
      setRuleSubmitting(false);
    }
  }

  async function handleToggleRule(rule: WorkspaceGuardrailRule) {
    if (!canManage || busyRuleId) return;
    setBusyRuleId(rule.id);
    setRuleErrorKey(null);
    try {
      await requestJson<WorkspaceGuardrailRule>(
        `/api/workspaces/${workspaceId}/guardrails/rules/${rule.id}`,
        {
          method: 'PATCH',
          body: { enabled: !rule.enabled },
        },
      );
      await loadGuardrails();
    } catch (err) {
      setRuleErrorKey(resolveErrorKey(err));
    } finally {
      setBusyRuleId(null);
    }
  }

  async function handleDeleteRule(ruleId: string) {
    if (!canManage || busyRuleId) return;
    setBusyRuleId(ruleId);
    setRuleErrorKey(null);
    setRuleBanner(null);
    try {
      await requestJson<void>(
        `/api/workspaces/${workspaceId}/guardrails/rules/${ruleId}`,
        { method: 'DELETE' },
      );
      setConfirmDeleteId(null);
      if (editingRule?.id === ruleId) {
        resetRuleForm();
      }
      setRuleBanner(t('ruleDeleted'));
      await loadGuardrails();
    } catch (err) {
      setRuleErrorKey(resolveErrorKey(err));
    } finally {
      setBusyRuleId(null);
    }
  }

  function startEditRule(rule: WorkspaceGuardrailRule) {
    setEditingRule(rule);
    setRuleName(rule.name);
    setRuleSlug(rule.slug);
    setSlugTouched(true);
    setMatchMode(rule.matchMode);
    setPattern(rule.pattern);
    setDirection(rule.direction);
    setAction(rule.action);
    setReplacementToken(rule.replacementToken);
    setRuleErrorKey(null);
    setTestResult(null);
  }

  if (loading && !policyDraft) {
    return (
      <Card>
        <p className="text-[15px] text-muted">{t('loading')}</p>
      </Card>
    );
  }

  if (loadErrorKey && !policyDraft) {
    return <Alert tone="warning">{tErrors(loadErrorKey)}</Alert>;
  }

  if (!policyDraft || !data) {
    return null;
  }

  const rules = data.rules ?? [];

  return (
    <div className="flex flex-col gap-6">
      <Card className="flex flex-col gap-5">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('policyEyebrow')}</Eyebrow>
            <h2 className="text-[22px] font-black leading-[1.3] tracking-[-0.01em] text-ink">
              {t('policyTitle')}
            </h2>
            <p className="max-w-2xl text-[14px] leading-[1.6] text-ink-2">
              {t('policyLead')}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <span
              className={`inline-flex items-center gap-1.5 rounded-pill border px-3 py-1 text-[13px] font-bold ${
                policyDraft.enabled
                  ? 'border-mint-line bg-mint-soft text-green-600'
                  : 'border-line bg-paper-2 text-muted'
              }`}
            >
              <span
                aria-hidden="true"
                className={`h-2 w-2 rounded-full ${
                  policyDraft.enabled ? 'bg-green' : 'bg-line-strong'
                }`}
              />
              <span>
                {policyDraft.enabled
                  ? t('statusEnforcing', { count: data.activeRuleCount })
                  : t('statusOff')}
              </span>
            </span>

            <ToggleSwitch
              checked={policyDraft.enabled}
              disabled={!canManage || policySaving}
              ariaLabel={t('masterSwitchLabel')}
              onToggle={() => {
                const next = { ...policyDraft, enabled: !policyDraft.enabled };
                setPolicyDraft(next);
                void savePolicy(next);
              }}
            />
          </div>
        </div>

        {!canManage ? <Alert tone="info">{t('readOnlyNotice')}</Alert> : null}

        {policyErrorKey ? (
          <Alert tone="warning">{tErrors(policyErrorKey)}</Alert>
        ) : null}
        {policyBanner ? <Alert tone="success">{policyBanner}</Alert> : null}

        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
          <div className="flex flex-col gap-2 rounded-md border border-line bg-paper-2 p-4">
            <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('appliedToLabel')}
            </span>
            <p className="text-[14px] font-bold text-ink">
              {t('appliedToValue')}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {data.coveredProviders.map((provider) => (
                <span
                  key={provider}
                  className="rounded-xs border border-line bg-paper px-2 py-0.5 font-mono text-[12px] text-ink-2"
                >
                  {provider}
                </span>
              ))}
            </div>
          </div>

          <div className="flex flex-col gap-2 rounded-md border border-line bg-paper-2 p-4">
            <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('evaluationsLabel')}
            </span>
            <div className="flex items-baseline gap-2">
              <span className="text-[28px] font-black leading-none text-ink">
                {data.summary24h.totalEvaluations.toLocaleString()}
              </span>
              <span className="text-[13px] text-muted">
                {t('evaluationsUnit')}
              </span>
            </div>
            <div className="flex flex-wrap gap-2">
              <span className="rounded-pill border border-blue-line bg-blue-tint px-2.5 py-0.5 text-[12px] font-bold text-blue">
                {t('redactedCount', { count: data.summary24h.redactedCount })}
              </span>
              <span className="rounded-pill border border-dashed border-amber-line bg-amber-tint px-2.5 py-0.5 text-[12px] font-bold text-amber">
                {t('blockedCount', { count: data.summary24h.blockedCount })}
              </span>
            </div>
          </div>

          <div className="flex flex-col gap-2 rounded-md border border-line bg-paper-2 p-4">
            <span className="text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('failureModeLabel')}
            </span>
            <SegmentedControl<GuardrailFailureMode>
              ariaLabel={t('failureModeLabel')}
              size="sm"
              value={policyDraft.failureMode}
              onChange={(mode) => {
                if (!canManage) return;
                updateDraft((prev) => ({ ...prev, failureMode: mode }));
              }}
              options={[
                { value: 'fail_closed', label: t('failureClosed') },
                { value: 'fail_open', label: t('failureOpen') },
              ]}
            />
            <p className="text-[12px] text-muted">{t('failureModeHint')}</p>
          </div>
        </div>
      </Card>

      <Card className="flex flex-col gap-5">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('detectorsEyebrow')}</Eyebrow>
            <h2 className="text-[22px] font-black leading-[1.3] tracking-[-0.01em] text-ink">
              {t('detectorsTitle')}
            </h2>
            <p className="text-[14px] leading-[1.6] text-ink-2">
              {t('detectorsLead')}
            </p>
          </div>
          {canManage ? (
            <Button
              variant="primary"
              disabled={policySaving}
              onClick={() => void savePolicy(policyDraft)}
            >
              {policySaving ? t('savingPolicy') : t('savePolicy')}
            </Button>
          ) : null}
        </div>

        <div className="flex flex-col divide-y divide-line">
          <div className="flex flex-col gap-3 py-4 first:pt-0">
            <div className="flex items-start gap-3.5">
              <ToggleSwitch
                checked={policyDraft.secrets.enabled}
                disabled={!canManage}
                ariaLabel={t('secretsTitle')}
                onToggle={() =>
                  updateDraft((prev) => ({
                    ...prev,
                    secrets: {
                      ...prev.secrets,
                      enabled: !prev.secrets.enabled,
                    },
                  }))
                }
              />
              <div className="min-w-0 flex-1">
                <p className="text-[16px] font-bold text-ink">
                  {t('secretsTitle')}
                </p>
                <p className="text-[14px] leading-[1.5] text-ink-2">
                  {t('secretsDesc')}
                </p>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-3 pl-[66px]">
              <DetectorDirectionControls
                value={policyDraft.secrets.direction}
                disabled={!canManage}
                ariaLabel={`${t('secretsTitle')} ${t('directionLabel')}`}
                labels={{
                  input: t('dirInput'),
                  output: t('dirOutput'),
                  both: t('dirBoth'),
                }}
                onChange={(next) =>
                  updateDraft((prev) => ({
                    ...prev,
                    secrets: { ...prev.secrets, direction: next },
                  }))
                }
              />
              <DetectorActionControls
                value={policyDraft.secrets.action}
                disabled={!canManage}
                ariaLabel={`${t('secretsTitle')} ${t('actionLabel')}`}
                labels={{
                  block: t('actionBlock'),
                  redact: t('actionRedact'),
                  audit: t('actionAudit'),
                }}
                onChange={(next) =>
                  updateDraft((prev) => ({
                    ...prev,
                    secrets: { ...prev.secrets, action: next },
                  }))
                }
              />
            </div>
          </div>

          <div className="flex flex-col gap-3 py-4">
            <div className="flex items-start gap-3.5">
              <ToggleSwitch
                checked={policyDraft.pii.enabled}
                disabled={!canManage}
                ariaLabel={t('piiTitle')}
                onToggle={() =>
                  updateDraft((prev) => ({
                    ...prev,
                    pii: { ...prev.pii, enabled: !prev.pii.enabled },
                  }))
                }
              />
              <div className="min-w-0 flex-1">
                <p className="text-[16px] font-bold text-ink">
                  {t('piiTitle')}
                </p>
                <p className="text-[14px] leading-[1.5] text-ink-2">
                  {t('piiDesc')}
                </p>
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {ALL_PII_ENTITIES.map((entity) => {
                    const selected = policyDraft.pii.entities.includes(entity);
                    const entityLabel =
                      entity === 'email'
                        ? t('entityEmail')
                        : entity === 'iban'
                          ? t('entityIban')
                          : entity === 'card'
                            ? t('entityCard')
                            : t('entityPhone');
                    return (
                      <button
                        key={entity}
                        type="button"
                        disabled={!canManage}
                        aria-pressed={selected}
                        onClick={() => togglePIIEntity(entity)}
                        className={`min-h-[34px] rounded-xs border px-2.5 font-mono text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
                          selected
                            ? 'border-blue-line bg-blue-tint text-blue'
                            : 'border-line bg-paper-2 text-muted hover:text-ink'
                        }`}
                      >
                        {entityLabel}
                      </button>
                    );
                  })}
                </div>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-3 pl-[66px]">
              <DetectorDirectionControls
                value={policyDraft.pii.direction}
                disabled={!canManage}
                ariaLabel={`${t('piiTitle')} ${t('directionLabel')}`}
                labels={{
                  input: t('dirInput'),
                  output: t('dirOutput'),
                  both: t('dirBoth'),
                }}
                onChange={(next) =>
                  updateDraft((prev) => ({
                    ...prev,
                    pii: { ...prev.pii, direction: next },
                  }))
                }
              />
              <DetectorActionControls
                value={policyDraft.pii.action}
                disabled={!canManage}
                ariaLabel={`${t('piiTitle')} ${t('actionLabel')}`}
                labels={{
                  block: t('actionBlock'),
                  redact: t('actionRedact'),
                  audit: t('actionAudit'),
                }}
                onChange={(next) =>
                  updateDraft((prev) => ({
                    ...prev,
                    pii: { ...prev.pii, action: next },
                  }))
                }
              />
            </div>
          </div>

          <div className="flex flex-col gap-3 py-4 last:pb-0">
            <div className="flex items-start gap-3.5">
              <ToggleSwitch
                checked={policyDraft.promptInjection.enabled}
                disabled={!canManage}
                ariaLabel={t('injectionTitle')}
                onToggle={() =>
                  updateDraft((prev) => ({
                    ...prev,
                    promptInjection: {
                      ...prev.promptInjection,
                      enabled: !prev.promptInjection.enabled,
                    },
                  }))
                }
              />
              <div className="min-w-0 flex-1">
                <p className="text-[16px] font-bold text-ink">
                  {t('injectionTitle')}
                </p>
                <p className="text-[14px] leading-[1.5] text-ink-2">
                  {t('injectionDesc')}
                </p>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-3 pl-[66px]">
              <DetectorDirectionControls
                value={policyDraft.promptInjection.direction}
                disabled={!canManage}
                ariaLabel={`${t('injectionTitle')} ${t('directionLabel')}`}
                labels={{
                  input: t('dirInput'),
                  output: t('dirOutput'),
                  both: t('dirBoth'),
                }}
                onChange={(next) =>
                  updateDraft((prev) => ({
                    ...prev,
                    promptInjection: {
                      ...prev.promptInjection,
                      direction: next,
                    },
                  }))
                }
              />
              <DetectorActionControls
                value={policyDraft.promptInjection.action}
                disabled={!canManage}
                ariaLabel={`${t('injectionTitle')} ${t('actionLabel')}`}
                labels={{
                  block: t('actionBlock'),
                  redact: t('actionRedact'),
                  audit: t('actionAudit'),
                }}
                onChange={(next) =>
                  updateDraft((prev) => ({
                    ...prev,
                    promptInjection: { ...prev.promptInjection, action: next },
                  }))
                }
              />
            </div>
          </div>
        </div>
      </Card>

      <Card className="flex flex-col gap-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <Eyebrow>{t('customEyebrow')}</Eyebrow>
            <h2 className="text-[22px] font-black leading-[1.3] tracking-[-0.01em] text-ink">
              {t('customTitle')}
            </h2>
          </div>
          <span className="rounded-pill border border-mint-line bg-mint-soft px-3 py-1 text-[13px] font-bold text-green-600">
            {t('customCountBadge', { count: rules.length })}
          </span>
        </div>

        {ruleErrorKey ? (
          <Alert tone="warning">{tErrors(ruleErrorKey)}</Alert>
        ) : null}
        {ruleBanner ? <Alert tone="success">{ruleBanner}</Alert> : null}

        {rules.length === 0 ? (
          <p className="rounded-sm border border-line bg-paper-2 px-4 py-3 text-[14px] text-muted">
            {t('customEmpty')}
          </p>
        ) : (
          <div className="overflow-x-auto rounded-md border border-line">
            <table className="w-full border-collapse text-left text-[13px]">
              <thead>
                <tr className="border-b border-line bg-paper-2 text-[11px] font-bold uppercase tracking-[0.06em] text-muted">
                  <th className="px-3 py-2.5">{t('colRule')}</th>
                  <th className="px-3 py-2.5">{t('colMode')}</th>
                  <th className="px-3 py-2.5">{t('colPattern')}</th>
                  <th className="px-3 py-2.5">{t('colDirection')}</th>
                  <th className="px-3 py-2.5">{t('colAction')}</th>
                  <th className="px-3 py-2.5">{t('colStatus')}</th>
                  {canManage ? (
                    <th className="px-3 py-2.5 text-right">
                      {t('colActions')}
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {rules.map((rule) => {
                  const isBusy = busyRuleId === rule.id;
                  const confirming = confirmDeleteId === rule.id;
                  return (
                    <tr key={rule.id} className="align-middle">
                      <td className="px-3 py-3">
                        <div className="font-bold text-ink">{rule.name}</div>
                        <div className="font-mono text-[12px] text-muted">
                          {rule.slug}
                        </div>
                      </td>
                      <td className="px-3 py-3">
                        <span className="rounded-xs border border-line bg-paper-2 px-2 py-0.5 font-mono text-[12px] text-ink-2">
                          {rule.matchMode === 'regex'
                            ? t('modeRegex')
                            : t('modeKeywords')}
                        </span>
                      </td>
                      <td className="max-w-[260px] px-3 py-3">
                        <code className="break-all rounded-xs bg-paper-2 px-2 py-1 font-mono text-[12px] text-ink">
                          {rule.pattern}
                        </code>
                      </td>
                      <td className="px-3 py-3 font-bold text-ink-2">
                        {rule.direction === 'input'
                          ? t('dirInput')
                          : rule.direction === 'output'
                            ? t('dirOutput')
                            : t('dirBoth')}
                      </td>
                      <td className="px-3 py-3">
                        <span
                          className={`inline-flex rounded-pill border px-2.5 py-0.5 text-[12px] font-bold ${actionPillClass(rule.action)}`}
                        >
                          {rule.action === 'redact'
                            ? `${t('actionRedact')} → ${rule.replacementToken}`
                            : rule.action === 'block'
                              ? t('actionBlock')
                              : t('actionAudit')}
                        </span>
                      </td>
                      <td className="px-3 py-3">
                        <ToggleSwitch
                          checked={rule.enabled}
                          disabled={!canManage || isBusy}
                          ariaLabel={t('toggleRuleAria', { name: rule.name })}
                          onToggle={() => void handleToggleRule(rule)}
                        />
                      </td>
                      {canManage ? (
                        <td className="px-3 py-3 text-right">
                          <div className="inline-flex flex-wrap items-center justify-end gap-2">
                            <Button
                              variant="secondary"
                              disabled={isBusy}
                              onClick={() => startEditRule(rule)}
                            >
                              <Pencil
                                className="h-3.5 w-3.5"
                                aria-hidden="true"
                              />
                              <span>{t('editAction')}</span>
                            </Button>
                            {confirming ? (
                              <Button
                                variant="secondary"
                                disabled={isBusy}
                                className="border-dashed border-amber-line bg-amber-tint text-amber"
                                onClick={() => void handleDeleteRule(rule.id)}
                              >
                                <Trash2
                                  className="h-3.5 w-3.5"
                                  aria-hidden="true"
                                />
                                <span>{t('confirmRemoveAction')}</span>
                              </Button>
                            ) : (
                              <Button
                                variant="secondary"
                                disabled={isBusy}
                                className="border-amber-line text-amber hover:bg-amber-tint"
                                onClick={() => setConfirmDeleteId(rule.id)}
                              >
                                <span>{t('removeAction')}</span>
                              </Button>
                            )}
                          </div>
                        </td>
                      ) : null}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {canManage ? (
          <form
            onSubmit={(event) => void handleRuleSubmit(event)}
            className="flex flex-col gap-4 border-t border-line pt-5"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="flex items-center gap-2 text-[16px] font-black text-ink">
                <Shield className="h-4 w-4 text-green" aria-hidden="true" />
                <span>
                  {editingRule
                    ? t('formEditTitle', { name: editingRule.name })
                    : t('formAddTitle')}
                </span>
              </h3>
              {editingRule ? (
                <Button variant="secondary" onClick={resetRuleForm}>
                  {t('cancelEditAction')}
                </Button>
              ) : null}
            </div>

            <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
              <Input
                label={t('nameLabel')}
                placeholder={t('namePlaceholder')}
                value={ruleName}
                onChange={(event) => {
                  const val = event.target.value;
                  setRuleName(val);
                  if (!editingRule && !slugTouched) {
                    setRuleSlug(slugifyRuleName(val));
                  }
                }}
                required
              />
              <Input
                label={t('slugLabel')}
                helperText={t('slugHint')}
                placeholder={t('slugPlaceholder')}
                value={ruleSlug}
                readOnly={Boolean(editingRule)}
                mono
                onChange={(event) => {
                  setSlugTouched(true);
                  setRuleSlug(event.target.value);
                }}
                required
              />
              <div className="flex flex-col gap-1.5">
                <span className="text-[14px] font-bold text-ink">
                  {t('matchModeLabel')}
                </span>
                <SegmentedControl<GuardrailMatchMode>
                  ariaLabel={t('matchModeLabel')}
                  value={matchMode}
                  onChange={(next) => {
                    setMatchMode(next);
                    setTestResult(null);
                  }}
                  options={[
                    { value: 'regex', label: t('modeRegex') },
                    { value: 'keywords', label: t('modeKeywords') },
                  ]}
                />
              </div>
            </div>

            <Input
              label={t('patternLabel')}
              helperText={
                matchMode === 'keywords' ? t('patternHintKeywords') : undefined
              }
              placeholder={
                matchMode === 'regex'
                  ? t('patternPlaceholderRegex')
                  : t('patternPlaceholderKeywords')
              }
              value={pattern}
              onChange={(event) => {
                setPattern(event.target.value);
                setTestResult(null);
              }}
              mono
              required
            />

            <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
              <div className="flex flex-col gap-1.5">
                <span className="text-[14px] font-bold text-ink">
                  {t('directionLabel')}
                </span>
                <DetectorDirectionControls
                  value={direction}
                  disabled={false}
                  ariaLabel={t('directionLabel')}
                  labels={{
                    input: t('dirInput'),
                    output: t('dirOutput'),
                    both: t('dirBoth'),
                  }}
                  onChange={setDirection}
                />
              </div>

              <div className="flex flex-col gap-1.5">
                <span className="text-[14px] font-bold text-ink">
                  {t('actionLabel')}
                </span>
                <DetectorActionControls
                  value={action}
                  disabled={false}
                  ariaLabel={t('actionLabel')}
                  labels={{
                    block: t('actionBlock'),
                    redact: t('actionRedact'),
                    audit: t('actionAudit'),
                  }}
                  onChange={setAction}
                />
              </div>

              {action === 'redact' ? (
                <Input
                  label={t('replacementLabel')}
                  placeholder={t('replacementPlaceholder')}
                  value={replacementToken}
                  onChange={(event) => {
                    setReplacementToken(event.target.value);
                    setTestResult(null);
                  }}
                  mono
                />
              ) : null}
            </div>

            <div className="flex flex-col gap-3 rounded-md border border-line bg-paper-2 p-4">
              <div className="flex flex-wrap items-end gap-3">
                <div className="min-w-[240px] flex-1">
                  <Input
                    label={t('quickTestLabel')}
                    placeholder={t('quickTestPlaceholder')}
                    value={sampleText}
                    onChange={(event) => setSampleText(event.target.value)}
                    mono
                  />
                </div>
                <Button
                  variant="secondary"
                  disabled={testBusy || !pattern.trim()}
                  onClick={() => void handleQuickTest()}
                >
                  {testBusy ? t('quickTesting') : t('quickTestButton')}
                </Button>
              </div>

              {testResult ? (
                <div
                  role="status"
                  className={`rounded-sm border px-3 py-2 font-mono text-[13px] ${
                    !testResult.valid
                      ? 'border-dashed border-amber-line bg-amber-tint text-amber'
                      : testResult.matchCount > 0
                        ? 'border-mint-line bg-mint-soft text-green-600'
                        : 'border-line bg-paper text-muted'
                  }`}
                >
                  {!testResult.valid
                    ? t('quickTestInvalid', {
                        error: testResult.error ?? '',
                      })
                    : testResult.matchCount > 0
                      ? t('quickTestMatches', {
                          count: testResult.matchCount,
                          preview: testResult.preview,
                        })
                      : t('quickTestNoMatch')}
                </div>
              ) : null}
            </div>

            <div className="flex justify-end">
              <Button type="submit" disabled={ruleSubmitting} arrow>
                {ruleSubmitting
                  ? t('savingRule')
                  : editingRule
                    ? t('updateSubmit')
                    : t('createSubmit')}
              </Button>
            </div>
          </form>
        ) : null}
      </Card>

      <section
        aria-label={t('activityTitle')}
        className="flex flex-col gap-4 rounded-lg border border-dark bg-terminal p-6 text-on-dark shadow-card"
      >
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <p className="text-[12px] font-bold uppercase tracking-[0.06em] text-green-light">
              <span className="font-black text-green-light">{'// '}</span>
              {t('activityEyebrow')}
            </p>
            <h2 className="text-[20px] font-black text-paper">
              {t('activityTitle')}
            </h2>
            <p className="text-[13px] text-on-dark-muted">
              {t('activityLead')}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            {(
              [
                ['', t('filterAll')],
                ['blocked', t('filterBlocked')],
                ['redacted', t('filterRedacted')],
                ['audited', t('filterAudited')],
              ] as const
            ).map(([val, label]) => {
              const active = actionFilter === val;
              return (
                <button
                  key={val || 'all'}
                  type="button"
                  aria-pressed={active}
                  onClick={() => setActionFilter(val)}
                  className={`min-h-[38px] rounded-xs border px-3 font-mono text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light ${
                    active
                      ? 'border-green-light bg-green-light text-terminal'
                      : 'border-dark bg-dark-2 text-on-dark-muted hover:text-paper'
                  }`}
                >
                  {label}
                </button>
              );
            })}

            {(
              [
                ['', t('filterAnyDir')],
                ['input', t('dirInput')],
                ['output', t('dirOutput')],
              ] as const
            ).map(([val, label]) => {
              const active = directionFilter === val;
              return (
                <button
                  key={val || 'any-dir'}
                  type="button"
                  aria-pressed={active}
                  onClick={() => setDirectionFilter(val)}
                  className={`min-h-[38px] rounded-xs border px-3 font-mono text-[12px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light ${
                    active
                      ? 'border-hex-1 bg-dark text-paper'
                      : 'border-dark bg-dark-2 text-on-dark-muted hover:text-paper'
                  }`}
                >
                  {label}
                </button>
              );
            })}
          </div>
        </div>

        {eventsLoading ? (
          <p className="font-mono text-[13px] text-on-dark-muted">
            {t('loading')}
          </p>
        ) : events.length === 0 ? (
          <p className="rounded-sm border border-dark bg-dark-2 px-4 py-3 font-mono text-[13px] text-on-dark-muted">
            {t('activityEmpty')}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left font-mono text-[12px]">
              <thead>
                <tr className="border-b border-dark text-[11px] uppercase tracking-[0.06em] text-on-dark-muted">
                  <th className="px-2.5 py-2">{t('logColTime')}</th>
                  <th className="px-2.5 py-2">{t('logColBot')}</th>
                  <th className="px-2.5 py-2">{t('logColModel')}</th>
                  <th className="px-2.5 py-2">{t('logColDir')}</th>
                  <th className="px-2.5 py-2">{t('logColRule')}</th>
                  <th className="px-2.5 py-2">{t('logColAction')}</th>
                  <th className="px-2.5 py-2">{t('logColSummary')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-dark">
                {events.map((ev) => {
                  const ts = new Date(ev.createdAt);
                  const timeStr = Number.isNaN(ts.getTime())
                    ? ev.createdAt
                    : ts.toISOString().slice(11, 19);
                  const actionClass =
                    ev.action === 'blocked'
                      ? 'border-dashed border-amber-on-dark text-amber-on-dark'
                      : ev.action === 'redacted'
                        ? 'border-hex-1 text-hex-1'
                        : ev.action === 'audited'
                          ? 'border-green-light text-green-light'
                          : 'border-dark text-on-dark-muted';
                  const actionLabel =
                    ev.action === 'blocked'
                      ? t('badgeBlocked')
                      : ev.action === 'redacted'
                        ? t('badgeRedacted')
                        : ev.action === 'audited'
                          ? t('badgeAudited')
                          : t('badgeAllowed');
                  return (
                    <tr key={ev.id}>
                      <td className="whitespace-nowrap px-2.5 py-2.5 text-on-dark-muted">
                        {timeStr}
                      </td>
                      <td className="px-2.5 py-2.5 font-bold text-paper">
                        {ev.botName}
                      </td>
                      <td className="px-2.5 py-2.5 text-on-dark">
                        {ev.model
                          ? `${ev.provider} · ${ev.model}`
                          : ev.provider}
                      </td>
                      <td className="px-2.5 py-2.5 uppercase text-hex-1">
                        {ev.direction}
                      </td>
                      <td className="px-2.5 py-2.5 text-on-dark">
                        {ev.ruleSlugs.join(', ') || '—'}
                      </td>
                      <td className="px-2.5 py-2.5">
                        <span
                          className={`inline-flex rounded-xs border px-2 py-0.5 text-[11px] font-bold ${actionClass}`}
                        >
                          {actionLabel}
                        </span>
                      </td>
                      <td className="px-2.5 py-2.5 text-on-dark-muted">
                        {ev.summary || '—'}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        <p className="flex items-center gap-2 border-t border-dark pt-3 text-[12px] text-on-dark-muted">
          <Lock
            className="h-3.5 w-3.5 shrink-0 text-green-light"
            aria-hidden="true"
          />
          <span>{t('privacyInvariant')}</span>
        </p>
      </section>
    </div>
  );
}
