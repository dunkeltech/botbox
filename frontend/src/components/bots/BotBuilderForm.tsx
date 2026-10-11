'use client';

import { ArrowLeft, Dices } from 'lucide-react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { type FormEvent, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  BotAvatar,
  Button,
  Card,
  Eyebrow,
  Input,
  RadioCardGroup,
  Select,
  Textarea,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { createBot, deleteBot, updateBot } from '@/lib/api-client';
import { generateRandomAvatarSeed } from '@/lib/avatar';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canCreateBot, canManageBot } from '@/lib/rbac';
import type {
  AvatarMood,
  Bot,
  DriverType,
  LlmMode,
  WorkspaceSummary,
} from '@/types/api';

const MOOD_OPTIONS: AvatarMood[] = ['idle', 'thinking', 'action', 'offline'];
const STANDARD_PROVIDERS = ['anthropic', 'openai', 'google'] as const;
const STANDARD_LANGUAGES = ['en', 'de', 'fr'] as const;

export interface BotBuilderFormProps {
  mode: 'create' | 'edit';
  workspace: WorkspaceSummary;
  bot?: Bot;
}

/**
 * Shared form for creating or configuring a workspace bot with live deterministic avatar preview
 * and RBAC-enforced edit/delete controls.
 */
export function BotBuilderForm({ mode, workspace, bot }: BotBuilderFormProps) {
  const t = useTranslations('builder');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const router = useRouter();
  const { user } = useAuth();

  const canEdit =
    mode === 'create'
      ? canCreateBot(workspace.role)
      : canManageBot(workspace.role, user?.id, bot);

  const [name, setName] = useState(bot?.name ?? '');
  const [description, setDescription] = useState(bot?.description ?? '');
  const [avatarSeed, setAvatarSeed] = useState(bot?.avatarSeed ?? 'atlas-005a');
  const [previewMood, setPreviewMood] = useState<AvatarMood>(
    bot?.mood ?? 'idle',
  );
  const [llmMode, setLlmMode] = useState<LlmMode>(bot?.llmMode ?? 'cli');
  const [llmProvider, setLlmProvider] = useState(
    bot?.llmProvider ?? 'anthropic',
  );
  const [llmModel, setLlmModel] = useState(
    bot?.llmModel ?? 'claude-sonnet-4-20250514',
  );
  const [driverType, setDriverType] = useState<DriverType>(
    bot?.driverType ?? workspace.defaultDriver,
  );
  const [replyLanguage, setReplyLanguage] = useState(
    bot?.replyLanguage ?? 'en',
  );

  const [submitting, setSubmitting] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);
  const [savedBanner, setSavedBanner] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const hasCustomProvider = !STANDARD_PROVIDERS.includes(
    llmProvider as (typeof STANDARD_PROVIDERS)[number],
  );
  const hasCustomLanguage = !STANDARD_LANGUAGES.includes(
    replyLanguage as (typeof STANDARD_LANGUAGES)[number],
  );

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canEdit) {
      return;
    }
    setErrorKey(null);
    setSavedBanner(false);
    setSubmitting(true);

    try {
      if (mode === 'create') {
        const created = await createBot(workspace.id, {
          name: name.trim(),
          description: description.trim(),
          avatarSeed: avatarSeed.trim(),
          llmMode,
          llmProvider,
          llmModel: llmModel.trim(),
          driverType,
          replyLanguage,
        });
        router.push(`/bots/${created.id}`);
      } else if (bot) {
        const updated = await updateBot(workspace.id, bot.id, {
          name: name.trim(),
          description: description.trim(),
          avatarSeed: avatarSeed.trim(),
          llmMode,
          llmProvider,
          llmModel: llmModel.trim(),
          driverType,
          replyLanguage,
        });
        setName(updated.name);
        setDescription(updated.description);
        setAvatarSeed(updated.avatarSeed);
        setSavedBanner(true);
      }
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function handleDelete() {
    if (!bot || !canEdit) {
      return;
    }
    setErrorKey(null);
    setDeleting(true);
    try {
      await deleteBot(workspace.id, bot.id);
      router.push('/');
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
      setDeleting(false);
    }
  }

  const effectiveSeed = avatarSeed.trim() || 'botbox-001a';

  const backHref = mode === 'edit' && bot ? `/bots/${bot.id}` : '/';
  const backLabel =
    mode === 'edit' && bot ? t('backToWorkspace') : t('backToFleet');

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-3">
        <Link
          href={backHref}
          className="inline-flex min-h-[44px] w-fit items-center gap-2 rounded-sm px-2 text-[14px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
        >
          <ArrowLeft className="h-4 w-4" aria-hidden="true" />
          <span>{backLabel}</span>
        </Link>

        <Eyebrow>
          {mode === 'create' ? t('createEyebrow') : t('editEyebrow')}
        </Eyebrow>
        <h1 className="text-[34px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[42px]">
          {mode === 'create'
            ? t('createTitle')
            : t('editTitle', { name: bot?.name ?? name })}
        </h1>
        <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
          {t('lead')}
        </p>

        {mode === 'edit' && bot ? (
          <nav
            aria-label={t('tabs.ariaLabel')}
            className="mt-1 flex flex-wrap gap-2 border-b border-line pb-3"
          >
            <Link
              href={`/bots/${bot.id}/settings`}
              aria-current="page"
              className="inline-flex min-h-[44px] items-center rounded-sm border border-green bg-mint-soft px-4 text-[14px] font-bold text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              {t('tabs.general')}
            </Link>
            <Link
              href={`/bots/${bot.id}/secrets`}
              className="inline-flex min-h-[44px] items-center rounded-sm border border-line bg-paper px-4 text-[14px] font-bold text-ink-2 transition duration-150 hover:border-green-light hover:bg-mint-soft hover:text-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              {t('tabs.secrets')}
            </Link>
          </nav>
        ) : null}
      </div>

      {!canEdit ? <Alert tone="info">{tErrors('authForbidden')}</Alert> : null}

      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      {savedBanner ? <Alert tone="success">{t('savedBanner')}</Alert> : null}

      <div className="grid gap-6 lg:grid-cols-[340px_1fr] lg:items-start">
        <Card className="flex flex-col gap-5 p-6">
          <h2 className="text-[18px] font-black text-ink">
            {t('avatarCardTitle')}
          </h2>

          <div className="flex items-center justify-center rounded-sm border border-line bg-paper-2 p-6">
            <BotAvatar
              seed={effectiveSeed}
              mood={previewMood}
              size={112}
              tile
              showStatusDot
            />
          </div>

          <div className="flex flex-col gap-3">
            <Input
              label={t('avatarSeedLabel')}
              helperText={t('avatarSeedHelper')}
              mono
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              value={avatarSeed}
              disabled={!canEdit}
              required
              onChange={(event) => setAvatarSeed(event.target.value)}
            />

            {canEdit ? (
              <Button
                variant="secondary"
                type="button"
                onClick={() =>
                  setAvatarSeed(generateRandomAvatarSeed(name || 'bot'))
                }
              >
                <Dices className="h-4 w-4" aria-hidden="true" />
                <span>{t('randomizeSeed')}</span>
              </Button>
            ) : null}
          </div>

          <div className="flex flex-col gap-2 border-t border-line pt-4">
            <span className="text-[13px] font-bold text-muted">
              {t('moodPreviewLabel')}
            </span>
            <div className="grid grid-cols-2 gap-2">
              {MOOD_OPTIONS.map((mood) => {
                const active = previewMood === mood;
                return (
                  <button
                    key={mood}
                    type="button"
                    aria-pressed={active}
                    onClick={() => setPreviewMood(mood)}
                    className={`min-h-[44px] rounded-sm border px-3 text-[13px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      active
                        ? 'border-green bg-mint-soft text-green-600'
                        : 'border-line-strong bg-paper text-ink-2 hover:border-green-light'
                    }`}
                  >
                    {t(`moods.${mood}`)}
                  </button>
                );
              })}
            </div>
          </div>
        </Card>

        <div className="flex flex-col gap-6">
          <Card className="p-6 sm:p-7">
            <form
              onSubmit={(event) => void handleSubmit(event)}
              className="flex flex-col gap-5"
            >
              <Input
                label={t('nameLabel')}
                placeholder={t('namePlaceholder')}
                value={name}
                disabled={!canEdit}
                required
                onChange={(event) => setName(event.target.value)}
              />

              <Textarea
                label={t('descriptionLabel')}
                placeholder={t('descriptionPlaceholder')}
                value={description}
                disabled={!canEdit}
                onChange={(event) => setDescription(event.target.value)}
              />

              <RadioCardGroup<LlmMode>
                legend={t('llmModeLegend')}
                name="bot-llm-mode"
                value={llmMode}
                onChange={setLlmMode}
                disabled={!canEdit}
                options={[
                  {
                    value: 'cli',
                    title: t('llmModeCliTitle'),
                    description: t('llmModeCliDesc'),
                  },
                  {
                    value: 'api',
                    title: t('llmModeApiTitle'),
                    description: t('llmModeApiDesc'),
                  },
                ]}
              />

              <div className="grid gap-4 sm:grid-cols-2">
                <Select
                  label={t('providerLabel')}
                  value={llmProvider}
                  disabled={!canEdit}
                  onChange={(event) => setLlmProvider(event.target.value)}
                >
                  {STANDARD_PROVIDERS.map((provider) => (
                    <option key={provider} value={provider}>
                      {t(`providers.${provider}`)}
                    </option>
                  ))}
                  {hasCustomProvider ? (
                    <option value={llmProvider}>{llmProvider}</option>
                  ) : null}
                </Select>

                <Input
                  label={t('modelLabel')}
                  placeholder={t('modelPlaceholder')}
                  mono
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={llmModel}
                  disabled={!canEdit}
                  required
                  onChange={(event) => setLlmModel(event.target.value)}
                />
              </div>

              <RadioCardGroup<DriverType>
                legend={t('driverLegend')}
                name="bot-driver-type"
                value={driverType}
                onChange={setDriverType}
                disabled={!canEdit}
                options={[
                  { value: 'docker', title: t('driverDocker') },
                  { value: 'kubernetes', title: t('driverK8s') },
                ]}
              />

              <Select
                label={t('replyLanguageLabel')}
                value={replyLanguage}
                disabled={!canEdit}
                onChange={(event) => setReplyLanguage(event.target.value)}
              >
                {STANDARD_LANGUAGES.map((lang) => (
                  <option key={lang} value={lang}>
                    {t(`languages.${lang}`)}
                  </option>
                ))}
                {hasCustomLanguage ? (
                  <option value={replyLanguage}>{replyLanguage}</option>
                ) : null}
              </Select>

              {canEdit ? (
                <div className="flex justify-end border-t border-line pt-4">
                  <Button type="submit" arrow disabled={submitting}>
                    {submitting
                      ? mode === 'create'
                        ? t('creating')
                        : t('saving')
                      : mode === 'create'
                        ? t('createSubmit')
                        : t('saveSubmit')}
                  </Button>
                </div>
              ) : null}
            </form>
          </Card>

          {mode === 'edit' && bot && canEdit ? (
            <Card className="flex flex-col gap-3 border-dashed border-amber-line bg-amber-tint/40 p-6">
              <h2 className="text-[18px] font-black text-ink">
                {t('deleteSectionTitle')}
              </h2>
              <p className="text-[14px] leading-[1.5] text-ink-2">
                {t('deleteSectionBody')}
              </p>
              <div className="flex flex-wrap gap-3 pt-1">
                {confirmingDelete ? (
                  <>
                    <Button
                      variant="primary"
                      type="button"
                      disabled={deleting}
                      onClick={() => void handleDelete()}
                      className="bg-amber hover:bg-dark"
                    >
                      {deleting ? t('deleting') : t('deleteConfirm')}
                    </Button>
                    <Button
                      variant="secondary"
                      type="button"
                      disabled={deleting}
                      onClick={() => setConfirmingDelete(false)}
                    >
                      {tActions('cancel')}
                    </Button>
                  </>
                ) : (
                  <Button
                    variant="secondary"
                    type="button"
                    onClick={() => setConfirmingDelete(true)}
                  >
                    {t('deleteButton')}
                  </Button>
                )}
              </div>
            </Card>
          ) : null}
        </div>
      </div>
    </div>
  );
}
