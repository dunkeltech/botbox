// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { Check, Plus, X } from 'lucide-react';
import { useState, type KeyboardEvent } from 'react';
import { useTranslations } from 'next-intl';
import { Button, Input } from '@/components/ui';
import type { components } from '@/types/openapi.gen';

export type AskBeforeRule = components['schemas']['AskBeforeRule'];

const MAX_RULES = 20;

function ruleLabel(
  rule: AskBeforeRule,
  t: (key: 'askNewHost' | 'askBudget' | 'askPush' | 'askBefore') => string,
): string {
  if (rule.kind === 'new_host') return t('askNewHost');
  if (rule.kind === 'raise_budget') return t('askBudget');
  if (rule.kind === 'push_protected') {
    const branch = rule.label?.trim();
    return branch ? `${t('askPush')} · ${branch}` : t('askPush');
  }
  return rule.label?.trim() || t('askBefore');
}

/**
 * Editable or read-only ask-before policy picker shared by group creation,
 * group details, bot settings, and DM details.
 */
export function AskBeforeEditor({
  value,
  onChange,
  allowRaiseBudget = false,
  canEdit = true,
  disabled = false,
  hint,
}: {
  value: readonly AskBeforeRule[];
  onChange: (next: AskBeforeRule[]) => void;
  allowRaiseBudget?: boolean;
  canEdit?: boolean;
  disabled?: boolean;
  hint?: string;
}) {
  const t = useTranslations('team');
  const [customDraft, setCustomDraft] = useState('');
  const pushRule = value.find((rule) => rule.kind === 'push_protected');
  const [branchDraft, setBranchDraft] = useState(pushRule?.label ?? '');
  const [syncedPushLabel, setSyncedPushLabel] = useState(pushRule?.label ?? '');

  // Keep the local branch input aligned when the parent replaces rules (e.g. switching bots/groups).
  if ((pushRule?.label ?? '') !== syncedPushLabel) {
    const nextLabel = pushRule?.label ?? '';
    setSyncedPushLabel(nextLabel);
    setBranchDraft(nextLabel);
  }

  const hasNewHost = value.some((rule) => rule.kind === 'new_host');
  const hasRaiseBudget = value.some((rule) => rule.kind === 'raise_budget');
  const hasPush = Boolean(pushRule);
  const customRules = value.filter((rule) => rule.kind === 'custom');
  const atLimit = value.length >= MAX_RULES;

  if (!canEdit) {
    return (
      <div className="flex flex-col gap-1.5">
        <span className="text-[14px] font-bold">{t('askBefore')}</span>
        {value.length > 0 ? (
          <ul className="flex flex-wrap gap-1.5">
            {value.map((rule, index) => (
              <li
                key={`${rule.kind}:${rule.label ?? ''}:${index}`}
                className="rounded-pill border border-line bg-paper px-2.5 py-1 text-[13px]"
              >
                {ruleLabel(rule, t)}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-[14px] text-muted">{t('notSet')}</p>
        )}
        {hint ? <p className="text-[13px] text-muted">{hint}</p> : null}
      </div>
    );
  }

  function toggleKind(kind: 'new_host' | 'raise_budget' | 'push_protected') {
    if (disabled) return;
    const exists = value.some((rule) => rule.kind === kind);
    if (exists) {
      onChange(value.filter((rule) => rule.kind !== kind));
      return;
    }
    if (atLimit) return;
    if (kind === 'push_protected') {
      const trimmed = branchDraft.trim();
      onChange([...value, trimmed ? { kind, label: trimmed } : { kind }]);
      return;
    }
    onChange([...value, { kind }]);
  }

  function commitPushBranch() {
    if (disabled || !hasPush) return;
    const trimmed = branchDraft.trim();
    const current = pushRule?.label?.trim() ?? '';
    if (trimmed === current) return;
    onChange(
      value.map((rule) =>
        rule.kind === 'push_protected'
          ? trimmed
            ? { kind: 'push_protected', label: trimmed }
            : { kind: 'push_protected' }
          : rule,
      ),
    );
  }

  function addCustomRule() {
    if (disabled || atLimit) return;
    const trimmed = customDraft.trim();
    if (!trimmed) return;
    const duplicate = customRules.some(
      (rule) =>
        (rule.label?.trim().toLowerCase() ?? '') === trimmed.toLowerCase(),
    );
    if (duplicate) {
      setCustomDraft('');
      return;
    }
    onChange([...value, { kind: 'custom', label: trimmed }]);
    setCustomDraft('');
  }

  function removeCustomRule(label: string) {
    if (disabled) return;
    onChange(
      value.filter(
        (rule) =>
          !(rule.kind === 'custom' && (rule.label?.trim() ?? '') === label),
      ),
    );
  }

  function onCustomKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!event.nativeEvent.isComposing && event.key === 'Enter') {
      event.preventDefault();
      addCustomRule();
    }
  }

  function onBranchKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!event.nativeEvent.isComposing && event.key === 'Enter') {
      event.preventDefault();
      commitPushBranch();
    }
  }

  const toggles: {
    kind: 'new_host' | 'push_protected' | 'raise_budget';
    label: string;
    pressed: boolean;
  }[] = [
    { kind: 'new_host', label: t('askNewHost'), pressed: hasNewHost },
    { kind: 'push_protected', label: t('askPush'), pressed: hasPush },
    ...(allowRaiseBudget
      ? [
          {
            kind: 'raise_budget' as const,
            label: t('askBudget'),
            pressed: hasRaiseBudget,
          },
        ]
      : []),
  ];

  return (
    <fieldset className="flex flex-col gap-2.5">
      <legend className="text-[14px] font-bold text-ink">
        {t('askBefore')}
      </legend>
      {hint ? <p className="text-[13px] text-muted">{hint}</p> : null}

      <div className="flex flex-col gap-2">
        {toggles.map((item) => (
          <button
            key={item.kind}
            type="button"
            aria-pressed={item.pressed}
            disabled={disabled || (!item.pressed && atLimit)}
            onClick={() => toggleKind(item.kind)}
            className={`flex min-h-11 items-center justify-between gap-2.5 rounded-sm border-[1.5px] px-3 py-2 text-left text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
              item.pressed
                ? 'border-green bg-mint-soft text-ink'
                : 'border-line bg-paper text-ink hover:border-green-light'
            }`}
          >
            <span>{item.label}</span>
            <span
              aria-hidden="true"
              className={`grid h-[22px] w-[22px] shrink-0 place-items-center rounded-xs border ${
                item.pressed
                  ? 'border-green-600 bg-green-600 text-paper'
                  : 'border-line-strong bg-paper'
              }`}
            >
              {item.pressed ? <Check className="h-3.5 w-3.5" /> : null}
            </span>
          </button>
        ))}
      </div>

      {hasPush ? (
        <div className="rounded-sm border border-line bg-paper p-2.5">
          <Input
            label={t('askPushBranchLabel')}
            placeholder={t('askPushBranchPlaceholder')}
            maxLength={100}
            value={branchDraft}
            disabled={disabled}
            onChange={(event) => setBranchDraft(event.target.value)}
            onBlur={commitPushBranch}
            onKeyDown={onBranchKeyDown}
          />
        </div>
      ) : null}

      <div className="flex flex-col gap-2">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
          <div className="min-w-0 flex-1">
            <Input
              label={t('askCustomLabel')}
              placeholder={t('askCustomPlaceholder')}
              maxLength={100}
              value={customDraft}
              disabled={disabled || atLimit}
              onChange={(event) => setCustomDraft(event.target.value)}
              onKeyDown={onCustomKeyDown}
            />
          </div>
          <Button
            type="button"
            variant="secondary"
            disabled={disabled || atLimit || !customDraft.trim()}
            onClick={addCustomRule}
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            <span>{t('askCustomAdd')}</span>
          </Button>
        </div>

        {customRules.length > 0 ? (
          <ul className="flex flex-wrap gap-1.5">
            {customRules.map((rule, index) => {
              const label = rule.label?.trim() ?? '';
              return (
                <li
                  key={`${label}:${index}`}
                  className="inline-flex min-h-11 items-center gap-1 rounded-pill border border-line bg-paper pl-3 pr-1 text-[13px] font-bold text-ink"
                >
                  <span>{label}</span>
                  <button
                    type="button"
                    disabled={disabled}
                    aria-label={t('askRemoveRule', { rule: label })}
                    onClick={() => removeCustomRule(label)}
                    className="inline-flex h-9 w-9 items-center justify-center rounded-pill text-muted transition hover:bg-mint-soft hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    <X className="h-3.5 w-3.5" aria-hidden="true" />
                  </button>
                </li>
              );
            })}
          </ul>
        ) : null}

        {atLimit ? (
          <p className="text-[12px] text-amber">{t('askRuleLimit')}</p>
        ) : null}
      </div>
    </fieldset>
  );
}
