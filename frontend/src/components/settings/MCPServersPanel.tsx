'use client';

import { type FormEvent, useCallback, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  Select,
  StatusChip,
} from '@/components/ui';
import { ApiClientError, requestJson } from '@/lib/api-client';
import { resolveErrorKey } from '@/lib/errors';
import type { WorkspaceRole } from '@/types/api';
import type { components } from '@/types/openapi.gen';

type MCPServer = components['schemas']['MCPServer'];
type MCPServerList = components['schemas']['MCPServerList'];
type MCPAuthScheme = components['schemas']['MCPAuthScheme'];
type CreateMCPServerRequest = components['schemas']['CreateMCPServerRequest'];

export interface MCPServersPanelProps {
  workspaceId: string;
  role: WorkspaceRole;
  /** Render nothing instead of an alert when older harnesses leave MCP routes unmocked (404). */
  ignoreMissing?: boolean;
}

/**
 * Workspace-level registry for remote HTTPS MCP servers, encrypted credentials,
 * and discovered command catalogs.
 */
export function MCPServersPanel({
  workspaceId,
  role,
  ignoreMissing = false,
}: MCPServersPanelProps) {
  const t = useTranslations('mcpProxy');
  const tErrors = useTranslations('errors');
  const canManage = role === 'owner' || role === 'admin';

  const [servers, setServers] = useState<MCPServer[]>([]);
  const [loading, setLoading] = useState(true);
  const [hidden, setHidden] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  const [endpointUrl, setEndpointUrl] = useState('');
  const [authScheme, setAuthScheme] = useState<MCPAuthScheme>('bearer');
  const [authHeaderName, setAuthHeaderName] = useState('X-API-Key');
  const [authUsername, setAuthUsername] = useState('');
  const [secretValue, setSecretValue] = useState('');
  const [initialTools, setInitialTools] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const loadServers = useCallback(async () => {
    setLoading(true);
    try {
      const res = await requestJson<MCPServerList>(
        `/api/workspaces/${workspaceId}/mcp-servers`,
      );
      setServers(res.servers ?? []);
      setHidden(false);
      setErrorKey(null);
    } catch (err) {
      if (
        ignoreMissing &&
        err instanceof ApiClientError &&
        err.status === 404
      ) {
        setHidden(true);
        setErrorKey(null);
      } else {
        setErrorKey(resolveErrorKey(err));
      }
    } finally {
      setLoading(false);
    }
  }, [ignoreMissing, workspaceId]);

  useEffect(() => {
    void loadServers();
  }, [loadServers]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canManage || submitting) {
      return;
    }

    setSubmitting(true);
    setErrorKey(null);

    const parsedTools = initialTools
      .split(',')
      .map((entry) => entry.trim())
      .filter((entry) => entry.length > 0)
      .map((toolName) => ({ toolName }));

    const payload: CreateMCPServerRequest = {
      name: name.trim(),
      slug: slug.trim(),
      endpointUrl: endpointUrl.trim(),
      authScheme,
      ...(authScheme === 'header'
        ? { authHeaderName: authHeaderName.trim() }
        : {}),
      ...(authScheme === 'basic' ? { authUsername: authUsername.trim() } : {}),
      ...(authScheme !== 'none' ? { secretValue } : {}),
      ...(parsedTools.length > 0 ? { tools: parsedTools } : {}),
    };

    try {
      const created = await requestJson<MCPServer>(
        `/api/workspaces/${workspaceId}/mcp-servers`,
        {
          method: 'POST',
          body: payload,
        },
      );
      setServers((prev) => [...prev, created]);
      setName('');
      setSlug('');
      setEndpointUrl('');
      setAuthUsername('');
      setSecretValue('');
      setInitialTools('');
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setSubmitting(false);
    }
  }

  async function handleSync(serverId: string) {
    if (!canManage || busyId) {
      return;
    }
    setBusyId(serverId);
    setErrorKey(null);
    try {
      const updated = await requestJson<MCPServer>(
        `/api/workspaces/${workspaceId}/mcp-servers/${serverId}/sync`,
        { method: 'POST' },
      );
      setServers((prev) =>
        prev.map((srv) => (srv.id === updated.id ? updated : srv)),
      );
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusyId(null);
    }
  }

  async function handleRemove(serverId: string) {
    if (!canManage || busyId) {
      return;
    }
    setBusyId(serverId);
    setErrorKey(null);
    try {
      await requestJson<void>(
        `/api/workspaces/${workspaceId}/mcp-servers/${serverId}`,
        { method: 'DELETE' },
      );
      setServers((prev) => prev.filter((srv) => srv.id !== serverId));
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
    } finally {
      setBusyId(null);
    }
  }

  if (hidden) {
    return null;
  }

  return (
    <Card className="flex flex-col gap-5">
      <div className="flex flex-col gap-1">
        <Eyebrow>{t('registryEyebrow')}</Eyebrow>
        <h2 className="text-[20px] font-black tracking-[-0.01em] text-ink">
          {t('registryTitle')}
        </h2>
        <p className="text-[14px] leading-[1.6] text-muted">
          {t('registryDescription')}
        </p>
      </div>

      {errorKey ? (
        <Alert tone="warning">{tErrors(errorKey as never)}</Alert>
      ) : null}

      {loading ? (
        <p className="text-[14px] text-muted">{t('registryLoading')}</p>
      ) : servers.length === 0 ? (
        <p className="rounded-sm border border-line bg-paper-2 px-4 py-3 text-[14px] text-muted">
          {t('registryEmpty')}
        </p>
      ) : (
        <ul className="flex flex-col gap-3">
          {servers.map((srv) => {
            const isBusy = busyId === srv.id;
            const toolCount = srv.tools?.length ?? 0;

            return (
              <li
                key={srv.id}
                className="flex flex-wrap items-center justify-between gap-4 rounded-md border border-line bg-paper-2 p-4"
              >
                <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                  <div className="flex flex-wrap items-center gap-2.5">
                    <span className="text-[16px] font-bold text-ink">
                      {srv.name}
                    </span>
                    <span className="rounded-pill border border-line bg-paper px-2.5 py-0.5 font-mono text-[12px] font-bold text-ink-2">
                      {srv.slug}
                    </span>
                    <StatusChip
                      status={srv.lastSyncError ? 'budgetWarning' : 'idle'}
                      label={t('registryToolsCount', { count: toolCount })}
                    />
                  </div>
                  <p className="truncate font-mono text-[13px] text-ink-2">
                    {srv.endpointUrl}
                  </p>
                  <div className="flex flex-wrap items-center gap-3 text-[13px] text-muted">
                    <span>
                      {srv.hasSecret
                        ? t('registrySecretStored', {
                            preview: srv.maskedPreview || '•••',
                          })
                        : t('registryNoAuth')}
                    </span>
                    {srv.lastSyncedAt ? (
                      <span>
                        {t('registrySyncedAt')}:{' '}
                        <span className="font-mono">{srv.lastSyncedAt}</span>
                      </span>
                    ) : null}
                  </div>
                  {srv.lastSyncError ? (
                    <p
                      role="status"
                      className="rounded-xs border border-dashed border-amber-line bg-amber-tint px-3 py-1.5 font-mono text-[12px] text-amber"
                    >
                      {t('registrySyncError')}: {srv.lastSyncError}
                    </p>
                  ) : null}
                </div>

                {canManage ? (
                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      variant="secondary"
                      disabled={busyId !== null}
                      onClick={() => void handleSync(srv.id)}
                    >
                      {isBusy ? t('registrySyncing') : t('registrySync')}
                    </Button>
                    <Button
                      variant="secondary"
                      disabled={busyId !== null}
                      aria-label={t('registryRemoveNamed', { name: srv.name })}
                      onClick={() => void handleRemove(srv.id)}
                    >
                      {t('registryRemove')}
                    </Button>
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}

      {canManage ? (
        <form
          onSubmit={(event) => void handleSubmit(event)}
          className="flex flex-col gap-4 border-t border-line pt-4"
        >
          <h3 className="text-[16px] font-bold text-ink">
            {t('registryFormTitle')}
          </h3>

          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Input
              label={t('registryName')}
              placeholder={t('registryNamePlaceholder')}
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
            />
            <Input
              label={t('registrySlug')}
              helperText={t('registrySlugHint')}
              placeholder={t('registrySlugPlaceholder')}
              value={slug}
              onChange={(event) => setSlug(event.target.value)}
              mono
              required
            />
          </div>

          <Input
            label={t('registryEndpoint')}
            helperText={t('registryEndpointHint')}
            placeholder={t('registryEndpointPlaceholder')}
            type="url"
            value={endpointUrl}
            onChange={(event) => setEndpointUrl(event.target.value)}
            mono
            required
          />

          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Select
              label={t('registryAuthScheme')}
              value={authScheme}
              onChange={(event) =>
                setAuthScheme(event.target.value as MCPAuthScheme)
              }
            >
              <option value="none">{t('registrySchemeNone')}</option>
              <option value="bearer">{t('registrySchemeBearer')}</option>
              <option value="header">{t('registrySchemeHeader')}</option>
              <option value="basic">{t('registrySchemeBasic')}</option>
            </Select>

            {authScheme === 'header' ? (
              <Input
                label={t('registryHeaderName')}
                value={authHeaderName}
                onChange={(event) => setAuthHeaderName(event.target.value)}
                mono
                required
              />
            ) : null}

            {authScheme === 'basic' ? (
              <Input
                label={t('registryUsername')}
                value={authUsername}
                onChange={(event) => setAuthUsername(event.target.value)}
                mono
                required
              />
            ) : null}
          </div>

          {authScheme !== 'none' ? (
            <Input
              label={t('registrySecret')}
              placeholder={t('registrySecretPlaceholder')}
              type="password"
              autoComplete="new-password"
              value={secretValue}
              onChange={(event) => setSecretValue(event.target.value)}
              mono
              required
            />
          ) : null}

          <Input
            label={t('registryInitialTools')}
            helperText={t('registryInitialToolsHint')}
            value={initialTools}
            onChange={(event) => setInitialTools(event.target.value)}
            mono
          />

          <div>
            <Button type="submit" disabled={submitting} arrow>
              {submitting ? t('registrySubmitting') : t('registrySubmit')}
            </Button>
          </div>
        </form>
      ) : null}
    </Card>
  );
}
