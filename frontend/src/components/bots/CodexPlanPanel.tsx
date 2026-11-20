// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';

import { Alert, Button, Card, Input, Select } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type SecretScope = components['schemas']['SecretScope'];

/** Connects a user's ChatGPT plan to one bot and synchronizes llm-openai. */
export function CodexPlanPanel({
  workspaceId,
  botId,
  embedded = false,
  canManageWorkspaceSecrets = false,
  onSecretsChanged,
}: {
  workspaceId: string;
  botId: string;
  /** Omit the outer Card wrapper when hosted inside the unified CLI tools card. */
  embedded?: boolean;
  /** Allow workspace owners and admins to store llm-openai at workspace scope. */
  canManageWorkspaceSecrets?: boolean;
  /** Notify parent secret views when pairing check or disconnect mutates llm-openai. */
  onSecretsChanged?: () => void;
}) {
  const t = useTranslations('codexPlan');
  const client = useQueryClient();
  const [code, setCode] = useState('');
  const [secretScope, setSecretScope] = useState<SecretScope>('bot');
  const [revocationPending, setRevocationPending] = useState(false);
  const key = ['codex-plan', workspaceId, botId];
  const route = `/api/workspaces/${workspaceId}/bots/${botId}/codex-plan`;
  const status = useQuery({
    queryKey: key,
    queryFn: () => requestJson<components['schemas']['CodexPlanStatus']>(route),
    retry: false,
  });
  const value = status.data;

  useEffect(() => {
    if (value?.secretScope) {
      setSecretScope(value.secretScope);
    }
  }, [value?.secretScope]);

  const start = useMutation({
    mutationFn: (targetScope: SecretScope) =>
      requestJson<components['schemas']['CodexPlanPairing']>(
        `${route}/pairing`,
        {
          method: 'POST',
          body: { secretScope: targetScope },
        },
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
      onSecretsChanged?.();
    },
  });
  const pending = start.isPending || disconnect.isPending;
  const HeadingTag = embedded ? 'h3' : 'h2';
  const headingClassName = embedded
    ? 'text-[18px] font-black text-ink'
    : 'text-[20px] font-black text-ink';
  const statusLabel = (() => {
    if (!value) {
      return null;
    }
    if (value.status !== 'disconnected' && !value.ownedByYou) {
      return t('ownedByAnother');
    }
    if (value.status === 'connected' && value.secretScope === 'workspace') {
      return t('connectedWorkspace');
    }
    if (value.status === 'connected' && value.secretScope === 'bot') {
      return t('connectedBot');
    }
    return t(value.status);
  })();
  const canStartOrReconnect =
    value && (value.status === 'disconnected' || value.ownedByYou);
  const body = (
    <div className="flex flex-col gap-3">
      <HeadingTag className={headingClassName}>{t('title')}</HeadingTag>
      <p className="text-[14px] text-ink-2">{t('description')}</p>
      {statusLabel ? (
        <p className="text-[14px] font-bold text-ink" role="status">
          {statusLabel}
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
              onSecretsChanged?.();
            }}
          >
            {t('check')}
          </Button>
        </>
      ) : value ? (
        <div className="flex flex-col gap-3">
          {canStartOrReconnect && canManageWorkspaceSecrets ? (
            <Select
              label={t('secretScopeLabel')}
              helperText={t('secretScopeHelp')}
              value={secretScope}
              disabled={pending}
              onChange={(event) =>
                setSecretScope(
                  event.target.value === 'workspace' ? 'workspace' : 'bot',
                )
              }
            >
              <option value="bot">{t('scopeBotOption')}</option>
              <option value="workspace">{t('scopeWorkspaceOption')}</option>
            </Select>
          ) : null}
          <div className="flex flex-wrap gap-2">
            {canStartOrReconnect ? (
              <Button
                variant="secondary"
                disabled={pending}
                onClick={() => start.mutate(secretScope)}
              >
                {t(
                  value.status === 'disconnected'
                    ? 'connect'
                    : 'reconnectButton',
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
        </div>
      ) : null}
    </div>
  );

  if (embedded) {
    return body;
  }

  return <Card className="p-6">{body}</Card>;
}
