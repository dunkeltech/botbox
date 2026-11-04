'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';

import { Alert, Button, Card, Input } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

/** Connects a user's ChatGPT plan to the interactive terminal of one bot. */
export function CodexPlanPanel({
  workspaceId,
  botId,
  embedded = false,
}: {
  workspaceId: string;
  botId: string;
  /** Omit the outer Card wrapper when hosted inside the unified CLI tools card. */
  embedded?: boolean;
}) {
  const t = useTranslations('codexPlan');
  const client = useQueryClient();
  const [code, setCode] = useState('');
  const [revocationPending, setRevocationPending] = useState(false);
  const key = ['codex-plan', workspaceId, botId];
  const route = `/api/workspaces/${workspaceId}/bots/${botId}/codex-plan`;
  const status = useQuery({
    queryKey: key,
    queryFn: () => requestJson<components['schemas']['CodexPlanStatus']>(route),
    retry: false,
  });
  const start = useMutation({
    mutationFn: () =>
      requestJson<components['schemas']['CodexPlanPairing']>(
        `${route}/pairing`,
        { method: 'POST' },
      ),
    onSuccess: ({ code }) => {
      setCode(code);
      setRevocationPending(false);
    },
  });
  const disconnect = useMutation({
    mutationFn: () =>
      requestJson<components['schemas']['CodexPlanDisconnect']>(route, {
        method: 'DELETE',
      }),
    onSuccess: async ({ revocationConfirmed }) => {
      setCode('');
      setRevocationPending(!revocationConfirmed);
      await client.invalidateQueries({ queryKey: key });
    },
  });
  const value = status.data;
  const pending = start.isPending || disconnect.isPending;
  const HeadingTag = embedded ? 'h3' : 'h2';
  const headingClassName = embedded
    ? 'text-[18px] font-black text-ink'
    : 'text-[20px] font-black text-ink';
  const body = (
    <div className="flex flex-col gap-3">
      <HeadingTag className={headingClassName}>{t('title')}</HeadingTag>
      <p className="text-[14px] text-ink-2">{t('description')}</p>
      {value ? (
        <p className="text-[14px] font-bold text-ink" role="status">
          {value.status !== 'disconnected' && !value.ownedByYou
            ? t('ownedByAnother')
            : t(value.status)}
        </p>
      ) : null}
      {status.isError || start.isError || disconnect.isError ? (
        <Alert tone="warning">{t('error')}</Alert>
      ) : null}
      {revocationPending ? (
        <Alert tone="warning">{t('revocationUnconfirmed')}</Alert>
      ) : null}
      {code ? (
        <>
          <p className="text-[14px] text-ink-2">{t('pairingHelp')}</p>
          <code className="w-fit rounded-xs bg-paper-2 px-3 py-2 text-[13px]">
            {t('setupCommand')}
          </code>
          <Input
            label={t('site')}
            readOnly
            value={window.location.origin}
            mono
          />
          <Input
            label={t('code')}
            readOnly
            value={code}
            onFocus={(event) => event.target.select()}
            mono
          />
          <Button
            variant="secondary"
            onClick={() => {
              setCode('');
              void status.refetch();
            }}
          >
            {t('check')}
          </Button>
        </>
      ) : value ? (
        <div className="flex flex-wrap gap-2">
          {value.status === 'disconnected' || value.ownedByYou ? (
            <Button
              variant="secondary"
              disabled={pending}
              onClick={() => start.mutate()}
            >
              {t(
                value.status === 'disconnected' ? 'connect' : 'reconnectButton',
              )}
            </Button>
          ) : null}
          {value.status !== 'disconnected' ? (
            <Button
              variant="secondary"
              disabled={pending}
              onClick={() => disconnect.mutate()}
            >
              {t('disconnect')}
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  );

  if (embedded) {
    return body;
  }

  return <Card className="p-6">{body}</Card>;
}
