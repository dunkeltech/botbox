'use client';

import Link from 'next/link';
import { useParams, useRouter } from 'next/navigation';
import { type FormEvent, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  BrandLogo,
  Button,
  Card,
  Eyebrow,
  Input,
  RoleBadge,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { acceptInvitation, getInvitationPreview } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { InvitationPreviewResponse } from '@/types/api';

/**
 * Single-use workspace invitation acceptance screen.
 * Supports one-click join when already authenticated, or new-account creation when signed out.
 */
export default function InvitePage() {
  const t = useTranslations('invite');
  const tErrors = useTranslations('errors');

  const params = useParams<{ token: string }>();
  const token = typeof params?.token === 'string' ? params.token : '';
  const router = useRouter();
  const { user, isLoading: authLoading, applySession, signOut } = useAuth();

  const [preview, setPreview] = useState<InvitationPreviewResponse | null>(
    null,
  );
  const [loadingPreview, setLoadingPreview] = useState(true);
  const [loadErrorKey, setLoadErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );

  const [email, setEmail] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitErrorKey, setSubmitErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  useEffect(() => {
    if (!token) {
      setLoadErrorKey('invitationNotFound');
      setLoadingPreview(false);
      return;
    }

    let cancelled = false;
    setLoadingPreview(true);
    setLoadErrorKey(null);

    void (async () => {
      try {
        const data = await getInvitationPreview(token);
        if (cancelled) {
          return;
        }
        setPreview(data);
        if (data.email) {
          setEmail(data.email);
        }
      } catch (err) {
        if (!cancelled) {
          setLoadErrorKey(resolveErrorKey(err));
        }
      } finally {
        if (!cancelled) {
          setLoadingPreview(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [token]);

  async function handleAcceptAuthenticated() {
    setSubmitErrorKey(null);
    setSubmitting(true);
    try {
      const session = await acceptInvitation(token, {});
      applySession(session);
      router.push('/');
    } catch (err) {
      setSubmitErrorKey(resolveErrorKey(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function handleAcceptNewAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitErrorKey(null);
    setSubmitting(true);
    try {
      const session = await acceptInvitation(token, {
        email: (preview?.email ?? email).trim(),
        displayName: displayName.trim(),
        password,
      });
      applySession(session);
      router.push('/');
    } catch (err) {
      setSubmitErrorKey(resolveErrorKey(err));
    } finally {
      setSubmitting(false);
    }
  }

  if (loadingPreview || authLoading) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-paper-2 p-6">
        <p role="status" className="text-[15px] font-bold text-ink-2">
          {t('loadingTitle')}
        </p>
      </main>
    );
  }

  if (loadErrorKey || !preview) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-paper-2 px-4 py-12">
        <Card className="flex w-full max-w-[480px] flex-col gap-5 p-8">
          <BrandLogo />
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <Alert tone="warning" title={t('invalidTitle')}>
            {tErrors(loadErrorKey ?? 'invitationNotFound')}
          </Alert>
          <Link
            href="/"
            className="inline-flex min-h-[44px] items-center justify-center rounded-sm border-[1.5px] border-line bg-paper px-5 text-[15px] font-bold text-green-600 transition duration-150 hover:border-green-light hover:bg-mint-soft"
          >
            {t('backToFleet')}
          </Link>
        </Card>
      </main>
    );
  }

  const formattedExpiry = new Date(preview.expiresAt).toLocaleDateString();

  return (
    <main className="flex min-h-screen items-center justify-center bg-paper-2 px-4 py-12">
      <Card className="flex w-full max-w-[540px] flex-col gap-6 p-7 sm:p-8">
        <BrandLogo />

        <div className="flex flex-col gap-2">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h1 className="text-[30px] font-black leading-[1.2] text-ink">
            {t('title', { workspaceName: preview.workspaceName })}
          </h1>
          <p className="text-[15px] leading-[1.6] text-ink-2">
            {t('lead', { workspaceSlug: preview.workspaceSlug })}
          </p>
        </div>

        <dl className="grid gap-3 rounded-sm border border-line bg-paper-2 p-4 sm:grid-cols-3">
          <div className="flex flex-col gap-1">
            <dt className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('roleLabel')}
            </dt>
            <dd>
              <RoleBadge role={preview.role} />
            </dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('invitedEmailLabel')}
            </dt>
            <dd className="truncate font-mono text-[13px] text-ink">
              {preview.email ?? t('anyEmailAllowed')}
            </dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
              {t('expiresLabel')}
            </dt>
            <dd className="text-[14px] font-bold text-ink">
              {formattedExpiry}
            </dd>
          </div>
        </dl>

        {submitErrorKey ? (
          <Alert tone="warning">{tErrors(submitErrorKey)}</Alert>
        ) : null}

        {user ? (
          <div className="flex flex-col gap-4 border-t border-line pt-5">
            <p className="text-[15px] font-bold text-ink">
              {t('signedInAsTitle', { email: user.email })}
            </p>
            <Button
              type="button"
              arrow
              disabled={submitting}
              onClick={() => void handleAcceptAuthenticated()}
            >
              {submitting
                ? t('signedInAccepting')
                : t('signedInAcceptButton', {
                    workspaceName: preview.workspaceName,
                  })}
            </Button>
            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <p className="text-[13px] text-muted">{t('switchAccountHint')}</p>
              <Button
                variant="secondary"
                type="button"
                disabled={submitting}
                onClick={() => void signOut()}
              >
                {t('signOutButton')}
              </Button>
            </div>
          </div>
        ) : (
          <form
            onSubmit={(event) => void handleAcceptNewAccount(event)}
            className="flex flex-col gap-4 border-t border-line pt-5"
          >
            <h2 className="text-[18px] font-black text-ink">
              {t('newAccountHeading')}
            </h2>

            <Input
              label={t('emailLabel')}
              type="email"
              autoComplete="email"
              value={preview.email ?? email}
              readOnly={Boolean(preview.email)}
              required
              onChange={(event) => setEmail(event.target.value)}
            />

            <Input
              label={t('displayNameLabel')}
              placeholder={t('displayNamePlaceholder')}
              autoComplete="name"
              value={displayName}
              required
              onChange={(event) => setDisplayName(event.target.value)}
            />

            <Input
              label={t('passwordLabel')}
              type="password"
              autoComplete="new-password"
              value={password}
              required
              onChange={(event) => setPassword(event.target.value)}
            />

            <Button type="submit" arrow disabled={submitting} className="mt-1">
              {submitting ? t('submitting') : t('submitNewAccount')}
            </Button>

            <div className="flex flex-wrap items-center gap-2 pt-2 text-[14px] text-ink-2">
              <span>{t('haveAccountText')}</span>
              <Link
                href="/login"
                className="font-bold text-green-600 underline-offset-4 hover:underline"
              >
                {t('signInLink')}
              </Link>
            </div>
          </form>
        )}
      </Card>
    </main>
  );
}
