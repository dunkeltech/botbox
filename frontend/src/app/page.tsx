'use client';

import { Plus } from 'lucide-react';
import Link from 'next/link';
import { useEffect, useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';

import { AppShell } from '@/components/shell/AppShell';
import {
  Alert,
  BotAvatar,
  Card,
  type ChipStatus,
  Eyebrow,
  Input,
  StatusChip,
} from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { listBots } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canCreateBot, canManageBot } from '@/lib/rbac';
import type { Bot, BotStatus } from '@/types/api';

function mapBotStatusToChip(status: BotStatus): ChipStatus {
  if (status === 'action') {
    return 'working';
  }
  return status;
}

function FleetContent() {
  const t = useTranslations('fleet');
  const tActions = useTranslations('actions');
  const tErrors = useTranslations('errors');
  const { user, activeWorkspace } = useAuth();

  const [bots, setBots] = useState<Bot[]>([]);
  const [loading, setLoading] = useState(true);
  const [errorKey, setErrorKey] = useState<ErrorTranslationKey | null>(null);
  const [searchQuery, setSearchQuery] = useState('');

  useEffect(() => {
    if (!activeWorkspace) {
      setBots([]);
      setLoading(false);
      return;
    }

    let cancelled = false;
    setLoading(true);
    setErrorKey(null);

    void (async () => {
      try {
        const res = await listBots(activeWorkspace.id);
        if (!cancelled) {
          setBots(res.bots);
        }
      } catch (err) {
        if (!cancelled) {
          setErrorKey(resolveErrorKey(err));
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [activeWorkspace]);

  const filteredBots = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) {
      return bots;
    }
    return bots.filter(
      (bot) =>
        bot.name.toLowerCase().includes(q) ||
        bot.description.toLowerCase().includes(q) ||
        bot.llmModel.toLowerCase().includes(q) ||
        bot.llmProvider.toLowerCase().includes(q) ||
        bot.avatarSeed.toLowerCase().includes(q),
    );
  }, [bots, searchQuery]);

  const canCreate =
    activeWorkspace !== null && canCreateBot(activeWorkspace.role);

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex flex-col gap-2">
          <Eyebrow>{t('eyebrow')}</Eyebrow>
          <h1 className="text-[36px] font-black leading-[1.15] tracking-[-0.01em] text-ink sm:text-[44px]">
            {t('title')}
          </h1>
          <p className="max-w-2xl text-[16px] leading-[1.6] text-ink-2">
            {t('lead')}
          </p>
        </div>

        {canCreate ? (
          <Link
            href="/bots/new"
            className="inline-flex min-h-[44px] shrink-0 items-center justify-center gap-2 rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper transition duration-150 hover:-translate-y-px hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            <span>{t('createCta')}</span>
          </Link>
        ) : null}
      </div>

      {activeWorkspace?.role === 'viewer' ? (
        <Alert tone="info">{t('viewerReadonly')}</Alert>
      ) : null}

      {errorKey ? <Alert tone="warning">{tErrors(errorKey)}</Alert> : null}

      <div
        role="search"
        className="flex flex-col gap-3 rounded-md border border-line bg-paper p-4 sm:flex-row sm:items-end sm:justify-between"
      >
        <div className="flex-1">
          <Input
            label={t('searchLabel')}
            placeholder={t('searchPlaceholder')}
            type="search"
            value={searchQuery}
            onChange={(event) => setSearchQuery(event.target.value)}
          />
        </div>
        <p className="pb-2 text-[14px] font-bold text-muted">
          {t('countLabel', { count: filteredBots.length })}
        </p>
      </div>

      {loading ? (
        <Card className="p-8 text-center">
          <p role="status" className="text-[15px] font-bold text-ink-2">
            {tActions('loading')}
          </p>
        </Card>
      ) : filteredBots.length === 0 ? (
        <Card className="flex flex-col items-start gap-3 p-8">
          <h2 className="text-[22px] font-black text-ink">
            {bots.length === 0 ? t('emptyTitle') : t('emptyFiltered')}
          </h2>
          {bots.length === 0 ? (
            <p className="max-w-xl text-[15px] leading-[1.6] text-ink-2">
              {t('emptyBody')}
            </p>
          ) : null}
          {bots.length === 0 && canCreate ? (
            <Link
              href="/bots/new"
              className="mt-2 inline-flex min-h-[44px] items-center rounded-sm bg-green-600 px-5 text-[15px] font-bold text-paper transition duration-150 hover:bg-dark"
            >
              {t('createCta')}
            </Link>
          ) : null}
        </Card>
      ) : (
        <div className="grid gap-5 md:grid-cols-2">
          {filteredBots.map((bot) => (
            <Link
              key={bot.id}
              href={`/bots/${bot.id}`}
              className="block rounded-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <Card interactive className="flex h-full flex-col gap-4 p-5">
                <div className="flex items-start justify-between gap-4">
                  <div className="flex items-center gap-4">
                    <BotAvatar
                      seed={bot.avatarSeed}
                      mood={bot.mood}
                      size={64}
                      tile
                      decorative
                    />
                    <div className="flex flex-col gap-1">
                      <h2 className="text-[20px] font-black leading-[1.25] text-ink">
                        {bot.name}
                      </h2>
                      <p className="font-mono text-[12px] text-muted">
                        {`${bot.llmProvider} · ${bot.llmModel}`}
                      </p>
                    </div>
                  </div>

                  <StatusChip
                    status={mapBotStatusToChip(bot.status)}
                    label={t(`status.${bot.status}`)}
                  />
                </div>

                {bot.description ? (
                  <p className="line-clamp-2 text-[14px] leading-[1.55] text-ink-2">
                    {bot.description}
                  </p>
                ) : null}

                <div className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
                  <div className="flex flex-wrap gap-2 font-mono text-[12px] text-ink-2">
                    <span className="rounded-xs border border-line bg-paper-2 px-2 py-0.5">
                      {`${t('driverPrefix')}${bot.driverType}`}
                    </span>
                    <span className="rounded-xs border border-line bg-paper-2 px-2 py-0.5">
                      {`${t('modePrefix')}${bot.llmMode}`}
                    </span>
                    <span className="rounded-xs border border-line bg-paper-2 px-2 py-0.5">
                      {`${t('seedPrefix')}${bot.avatarSeed}`}
                    </span>
                  </div>
                  <span className="text-[13px] font-bold text-green-600">
                    {canManageBot(activeWorkspace?.role, user?.id, bot)
                      ? t('editAction')
                      : t('inspectAction')}
                  </span>
                </div>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * Authenticated workspace Fleet dashboard listing bots with deterministic avatars and filters.
 */
export default function FleetPage() {
  return (
    <AppShell>
      <FleetContent />
    </AppShell>
  );
}
