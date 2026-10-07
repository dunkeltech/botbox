'use client';

import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { type FormEvent, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  BrandLogo,
  Button,
  Card,
  Eyebrow,
  Input,
  RadioCardGroup,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { ApiClientError, completeSetup } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { slugifyName } from '@/lib/slug';
import type { DriverType } from '@/types/api';

/**
 * First-run bootstrap wizard (Application.md §4.1 / design/screens/setup-wizard.png).
 * Verifies the server-generated setup secret, creates the first owner, and provisions
 * the initial workspace.
 */
export default function SetupPage() {
  const t = useTranslations('setup');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const router = useRouter();
  const { user, setupRequired, isLoading, applySession } = useAuth();

  const [setupSecret, setSetupSecret] = useState('');
  const [showSecret, setShowSecret] = useState(false);
  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [workspaceName, setWorkspaceName] = useState('');
  const [workspaceSlug, setWorkspaceSlug] = useState('');
  const [slugTouched, setSlugTouched] = useState(false);
  const [defaultDriver, setDefaultDriver] = useState<DriverType>('docker');

  const [submitting, setSubmitting] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);
  const [secretInvalid, setSecretInvalid] = useState(false);

  useEffect(() => {
    if (!isLoading && user && !setupRequired) {
      router.replace('/');
    }
  }, [isLoading, user, setupRequired, router]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setErrorKey(null);
    setSecretInvalid(false);
    setSubmitting(true);

    try {
      const session = await completeSetup({
        setupSecret: setupSecret.trim(),
        displayName: displayName.trim(),
        email: email.trim(),
        password,
        workspaceName: workspaceName.trim(),
        workspaceSlug: workspaceSlug.trim(),
        defaultDriver,
        defaultLocale: 'en',
      });
      applySession(session);
      router.push('/');
    } catch (err) {
      const resolved = resolveErrorKey(err);
      setErrorKey(resolved);
      if (
        err instanceof ApiClientError &&
        err.code === 'setup.invalid_secret'
      ) {
        setSecretInvalid(true);
      }
    } finally {
      setSubmitting(false);
    }
  }

  if (isLoading) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-paper-2 p-6">
        <p role="status" className="text-[15px] font-bold text-ink-2">
          {tActions('loading')}
        </p>
      </main>
    );
  }

  if (!setupRequired && !user) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-paper-2 px-4 py-12">
        <Card className="flex w-full max-w-[480px] flex-col gap-5 p-8">
          <BrandLogo />
          <div className="flex flex-col gap-2">
            <Eyebrow>{t('eyebrow')}</Eyebrow>
            <h1 className="text-[28px] font-black leading-[1.2] text-ink">
              {t('alreadyDoneTitle')}
            </h1>
            <p className="text-[15px] leading-[1.6] text-ink-2">
              {tErrors('setupAlreadyCompleted')}
            </p>
          </div>
          <Link
            href="/login"
            className="inline-flex min-h-[44px] items-center justify-center rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper transition duration-150 hover:-translate-y-px hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {t('alreadyDoneLink')}
          </Link>
        </Card>
      </main>
    );
  }

  const steps = [
    { num: t('step1Num'), title: t('step1Title'), desc: t('step1Desc') },
    { num: t('step2Num'), title: t('step2Title'), desc: t('step2Desc') },
    { num: t('step3Num'), title: t('step3Title'), desc: t('step3Desc') },
  ];

  return (
    <div className="min-h-screen bg-paper-2 px-4 py-8 sm:px-6 lg:py-12">
      <div className="mx-auto grid max-w-[1160px] gap-8 lg:grid-cols-[400px_1fr] lg:items-start">
        <aside className="flex flex-col gap-6 rounded-md bg-dark p-6 text-on-dark shadow-card sm:p-7">
          <BrandLogo dark />

          <ol aria-label={t('stepsAria')} className="flex flex-col gap-4">
            {steps.map((step) => (
              <li
                key={step.num}
                className="flex items-start gap-3.5 rounded-sm border border-on-dark-faint/30 bg-terminal/60 p-3.5"
              >
                <span className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-pill bg-green-light font-mono text-[12px] font-bold text-terminal">
                  {step.num}
                </span>
                <div className="flex flex-col gap-0.5">
                  <span className="text-[14px] font-bold text-paper">
                    {step.title}
                  </span>
                  <span className="text-[13px] leading-[1.5] text-on-dark-muted">
                    {step.desc}
                  </span>
                </div>
              </li>
            ))}
          </ol>

          <div className="flex flex-col gap-2 rounded-sm border border-on-dark-faint/30 bg-terminal p-4 font-mono text-[12px] leading-[1.6]">
            <div className="flex items-center justify-between text-on-dark-muted">
              <span>{t('terminalTitle')}</span>
              <span>{t('terminalHint')}</span>
            </div>
            <p className="text-paper">{t('terminalCmd')}</p>
            <p className="text-on-dark-muted">{t('terminalLine1')}</p>
            <p className="text-amber-on-dark">{t('terminalLine2')}</p>
            <p className="rounded-xs bg-dark-2 px-2.5 py-1.5 text-green-light">
              {t('terminalSecretSample')}
            </p>
          </div>
        </aside>

        <main>
          <Card className="p-6 sm:p-8">
            <div className="mb-6 flex flex-col gap-2">
              <Eyebrow>{t('eyebrow')}</Eyebrow>
              <h1 className="text-[32px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[38px]">
                {t('title')}
              </h1>
              <p className="text-[16px] leading-[1.6] text-ink-2">
                {t('lead')}
              </p>
            </div>

            <form
              onSubmit={(event) => void handleSubmit(event)}
              className="flex flex-col gap-7"
            >
              {errorKey ? (
                <Alert tone="warning">{tErrors(errorKey)}</Alert>
              ) : null}

              <section className="flex flex-col gap-4 border-t border-line pt-6">
                <h2 className="text-[18px] font-black text-ink">
                  {t('sectionSecret')}
                </h2>
                <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
                  <div className="flex-1">
                    <Input
                      label={t('secretLabel')}
                      helperText={t('secretHelper')}
                      placeholder={t('secretPlaceholder')}
                      warning={
                        secretInvalid
                          ? tErrors('setupInvalidSecret')
                          : undefined
                      }
                      type={showSecret ? 'text' : 'password'}
                      mono
                      autoComplete="off"
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={setupSecret}
                      required
                      onChange={(event) => {
                        setSetupSecret(event.target.value);
                        if (secretInvalid) {
                          setSecretInvalid(false);
                        }
                      }}
                    />
                  </div>
                  <Button
                    variant="secondary"
                    type="button"
                    onClick={() => setShowSecret((prev) => !prev)}
                  >
                    {showSecret ? t('hideSecret') : t('showSecret')}
                  </Button>
                </div>
              </section>

              <section className="flex flex-col gap-4 border-t border-line pt-6">
                <h2 className="text-[18px] font-black text-ink">
                  {t('sectionOwner')}
                </h2>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Input
                    label={t('displayNameLabel')}
                    placeholder={t('displayNamePlaceholder')}
                    autoComplete="name"
                    value={displayName}
                    required
                    onChange={(event) => setDisplayName(event.target.value)}
                  />
                  <Input
                    label={t('emailLabel')}
                    placeholder={t('emailPlaceholder')}
                    type="email"
                    autoComplete="email"
                    value={email}
                    required
                    onChange={(event) => setEmail(event.target.value)}
                  />
                </div>
                <Input
                  label={t('passwordLabel')}
                  helperText={t('passwordHelper')}
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  required
                  onChange={(event) => setPassword(event.target.value)}
                />
              </section>

              <section className="flex flex-col gap-4 border-t border-line pt-6">
                <h2 className="text-[18px] font-black text-ink">
                  {t('sectionWorkspace')}
                </h2>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Input
                    label={t('workspaceNameLabel')}
                    placeholder={t('workspaceNamePlaceholder')}
                    value={workspaceName}
                    required
                    onChange={(event) => {
                      const nextName = event.target.value;
                      setWorkspaceName(nextName);
                      if (!slugTouched) {
                        setWorkspaceSlug(slugifyName(nextName));
                      }
                    }}
                  />
                  <Input
                    label={t('workspaceSlugLabel')}
                    helperText={t('workspaceSlugHelper')}
                    placeholder={t('workspaceSlugPlaceholder')}
                    mono
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={workspaceSlug}
                    required
                    onChange={(event) => {
                      setSlugTouched(true);
                      setWorkspaceSlug(event.target.value);
                    }}
                  />
                </div>

                <RadioCardGroup<DriverType>
                  legend={t('driverLegend')}
                  name="setup-default-driver"
                  value={defaultDriver}
                  onChange={setDefaultDriver}
                  options={[
                    {
                      value: 'docker',
                      title: t('driverDockerTitle'),
                      description: t('driverDockerDesc'),
                    },
                    {
                      value: 'kubernetes',
                      title: t('driverK8sTitle'),
                      description: t('driverK8sDesc'),
                    },
                  ]}
                />
              </section>

              <div className="flex justify-end border-t border-line pt-6">
                <Button type="submit" arrow disabled={submitting}>
                  {submitting ? t('submitting') : t('submit')}
                </Button>
              </div>
            </form>
          </Card>
        </main>
      </div>
    </div>
  );
}
