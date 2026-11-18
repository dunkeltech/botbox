// SPDX-License-Identifier: AGPL-3.0-only

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
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { login } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';

/**
 * Control-plane login screen (redirects to /setup when deployment setup is pending).
 */
export default function LoginPage() {
  const t = useTranslations('auth');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');

  const router = useRouter();
  const { user, setupRequired, isLoading, applySession } = useAuth();

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);

  useEffect(() => {
    if (isLoading) {
      return;
    }
    if (setupRequired) {
      router.replace('/setup');
      return;
    }
    if (user) {
      router.replace('/');
    }
  }, [isLoading, setupRequired, user, router]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setErrorKey(null);
    setSubmitting(true);

    try {
      const session = await login({
        email: email.trim(),
        password,
      });
      applySession(session);
      router.push('/');
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
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

  return (
    <div className="flex min-h-screen flex-col bg-paper-2">
      <header className="border-b border-line bg-paper">
        <div className="mx-auto flex min-h-16 max-w-[1360px] items-center justify-between gap-4 px-4 sm:px-6">
          <BrandLogo size={34} />
        </div>
      </header>
      <main className="flex flex-1 items-center justify-center px-4 py-10">
        <Card className="flex w-full max-w-[480px] flex-col gap-6 p-7 sm:p-8">
          <div className="flex flex-col gap-2">
            <Eyebrow>{t('eyebrow')}</Eyebrow>
            <h1 className="text-[30px] font-black leading-[1.2] tracking-[-0.01em] text-ink">
              {t('title')}
            </h1>
            <p className="text-[15px] leading-[1.6] text-ink-2">{t('lead')}</p>
          </div>

          {setupRequired ? (
            <Alert
              tone="info"
              title={t('setupBannerTitle')}
              action={
                <Link
                  href="/setup"
                  className="inline-flex min-h-[44px] items-center rounded-sm bg-green-600 px-4 text-[14px] font-bold text-paper transition duration-150 hover:bg-dark"
                >
                  {t('setupBannerAction')}
                </Link>
              }
            >
              {t('setupBannerBody')}
            </Alert>
          ) : null}

          <form
            onSubmit={(event) => void handleSubmit(event)}
            className="flex flex-col gap-5"
          >
            {errorKey ? (
              <Alert tone="warning">{tErrors(errorKey)}</Alert>
            ) : null}

            <Input
              label={t('emailLabel')}
              placeholder={t('emailPlaceholder')}
              type="email"
              autoComplete="email"
              value={email}
              required
              onChange={(event) => setEmail(event.target.value)}
            />

            <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
              <div className="flex-1">
                <Input
                  label={t('passwordLabel')}
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="current-password"
                  value={password}
                  required
                  onChange={(event) => setPassword(event.target.value)}
                />
              </div>
              <Button
                variant="secondary"
                type="button"
                onClick={() => setShowPassword((prev) => !prev)}
              >
                {showPassword ? t('hidePassword') : t('showPassword')}
              </Button>
            </div>

            <Button
              type="submit"
              arrow
              disabled={submitting}
              className="w-full"
            >
              {submitting ? t('submitting') : t('submit')}
            </Button>
          </form>

          <p className="rounded-sm border border-line bg-paper-2 px-3.5 py-2.5 text-center font-mono text-[12px] text-muted">
            {t('securityFooter')}
          </p>
        </Card>
      </main>
    </div>
  );
}
