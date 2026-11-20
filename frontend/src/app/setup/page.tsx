// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { Check, Lock } from 'lucide-react';
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
 * First-run setup wizard (design/screens/setup-wizard.png).
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

  return (
    <div className="flex min-h-screen flex-col bg-paper-2">
      <header className="border-b border-line bg-paper">
        <div className="mx-auto flex min-h-16 max-w-[1360px] flex-wrap items-center justify-between gap-3 px-4 py-2 sm:px-6">
          <div className="flex flex-wrap items-center gap-3">
            <BrandLogo size={34} />
            <span className="inline-flex items-center gap-1.5 rounded-pill border border-amber-line bg-amber-tint px-3 py-1 text-[12px] font-bold text-amber">
              <span
                aria-hidden="true"
                className="h-2 w-2 rounded-pill bg-amber"
              />
              <span>{t('uninitializedBadge')}</span>
            </span>
          </div>
          <ol
            aria-label={t('stepsAria')}
            className="hidden flex-wrap items-center gap-2 text-[13px] font-bold lg:flex"
          >
            <li className="rounded-pill border border-mint-line bg-mint-soft px-3 py-1 text-green-600">
              {t('stepPill1')}
            </li>
            <li aria-hidden="true" className="text-muted">
              →
            </li>
            <li className="rounded-pill border border-line bg-paper px-3 py-1 text-ink-2">
              {t('stepPill2')}
            </li>
            <li aria-hidden="true" className="text-muted">
              →
            </li>
            <li className="rounded-pill border border-line bg-paper px-3 py-1 text-ink-2">
              {t('stepPill3')}
            </li>
          </ol>
        </div>
      </header>

      <div className="mx-auto w-full max-w-[1360px] flex-1 px-4 py-8 sm:px-6">
        <div className="mb-6 flex flex-col gap-1.5">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h1 className="text-[34px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[42px]">
            {t('title')}
          </h1>
          <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
            {t('lead')}
          </p>
        </div>

        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_380px] lg:items-start">
          <main>
            <Card className="p-6 sm:p-8">
              <form
                onSubmit={(event) => void handleSubmit(event)}
                className="flex flex-col gap-7"
              >
                {errorKey ? (
                  <Alert tone="warning">{tErrors(errorKey)}</Alert>
                ) : null}

                <section className="flex flex-col gap-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <h2 className="text-[19px] font-black text-ink">
                      {t('sectionSecret')}
                    </h2>
                  </div>
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
                      className="sm:mb-6"
                    >
                      {showSecret ? t('hideSecret') : t('showSecret')}
                    </Button>
                  </div>
                  <div className="flex items-center gap-2 rounded-sm border border-dashed border-amber-line bg-amber-tint px-3.5 py-2.5 text-[13px] font-bold text-amber">
                    <Lock className="h-4 w-4 shrink-0" aria-hidden="true" />
                    <span>{t('rateLimitNotice')}</span>
                  </div>
                </section>

                <section className="flex flex-col gap-4 border-t border-line pt-6">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <h2 className="text-[19px] font-black text-ink">
                      {t('sectionOwner')}
                    </h2>
                    <span className="rounded-pill border border-mint-line bg-mint-soft px-2.5 py-0.5 text-[12px] font-bold text-green-600">
                      {t('ownerRoleBadge')}
                    </span>
                  </div>
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
                  <h2 className="text-[19px] font-black text-ink">
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

                <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line pt-6">
                  <span className="font-mono text-[12px] text-muted">
                    {t('submitMeta')}
                  </span>
                  <Button type="submit" arrow disabled={submitting}>
                    {submitting ? t('submitting') : t('submit')}
                  </Button>
                </div>
              </form>
            </Card>
          </main>

          <aside className="flex flex-col gap-5">
            <div className="flex flex-col gap-2.5 rounded-lg bg-terminal p-5 font-mono text-[12px] leading-[1.6] text-on-dark shadow-card">
              <div className="flex items-center justify-between text-on-dark-muted">
                <span className="font-sans text-[11px] font-bold uppercase tracking-[0.06em]">
                  {`// ${t('terminalEyebrow')}`}
                </span>
                <span className="rounded-xs bg-dark px-2 py-0.5 text-[11px] text-green-light">
                  {t('terminalTitle')}
                </span>
              </div>
              <p className="text-green-light">{t('terminalCmd')}</p>
              <p className="text-on-dark-muted">{t('terminalLine1')}</p>
              <p className="text-on-dark">{t('terminalLine2')}</p>
              <div className="mt-1 rounded-sm border border-green/40 bg-dark-2 p-3">
                <p className="text-[11px] uppercase tracking-[0.06em] text-green-light">
                  {t('terminalSecretLabel')}
                </p>
                <p className="mt-1 font-bold text-paper">
                  {t('terminalSecretSample')}
                </p>
                <p className="mt-0.5 text-[11px] text-on-dark-muted">
                  {t('terminalHint')}
                </p>
              </div>
            </div>

            <Card className="flex flex-col gap-3 p-5">
              <Eyebrow>{t('invariantsEyebrow')}</Eyebrow>
              <h2 className="text-[18px] font-black text-ink">
                {t('invariantsTitle')}
              </h2>
              <ul className="flex flex-col gap-2.5 text-[13px] leading-[1.5] text-ink-2">
                {[
                  t('invariant1'),
                  t('invariant2'),
                  t('invariant3'),
                  t('invariant4'),
                ].map((item) => (
                  <li key={item} className="flex items-start gap-2">
                    <Check
                      className="mt-0.5 h-4 w-4 shrink-0 text-green-600"
                      aria-hidden="true"
                    />
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            </Card>
          </aside>
        </div>
      </div>
    </div>
  );
}
