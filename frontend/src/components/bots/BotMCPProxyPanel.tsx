'use client';

import { useCallback, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  Button,
  Card,
  Eyebrow,
  Input,
  StatusChip,
} from '@/components/ui';
import { ApiClientError, requestJson } from '@/lib/api-client';
import { resolveErrorKey } from '@/lib/errors';
import type { components } from '@/types/openapi.gen';

type BotMCPConfigResponse = components['schemas']['BotMCPConfigResponse'];
type BotMCPServerPolicy = components['schemas']['BotMCPServerPolicy'];
type MCPToolPolicyMode = components['schemas']['MCPToolPolicyMode'];
type UpdateBotMCPServerPolicyRequest =
  components['schemas']['UpdateBotMCPServerPolicyRequest'];
type MCPCallLogListResponse = components['schemas']['MCPCallLogListResponse'];
type MCPCallLogEntry = components['schemas']['MCPCallLogEntry'];
type MCPCallLogAction = components['schemas']['MCPCallLogAction'];

export interface BotMCPProxyPanelProps {
  workspaceId: string;
  botId: string;
  botName: string;
  canManage: boolean;
  /** Render nothing instead of an alert when older harnesses leave MCP routes unmocked (404). */
  ignoreMissing?: boolean;
}

const LOG_ACTION_KEY_MAP: Record<MCPCallLogAction, string> = {
  allowed: 'logActionAllowed',
  blocked: 'logActionBlocked',
  ask_pending: 'logActionAskPending',
  approved_by_human: 'logActionApproved',
  denied_by_human: 'logActionDenied',
  upstream_error: 'logActionUpstreamError',
};

const POLICY_ORDER: MCPToolPolicyMode[] = ['allow', 'ask', 'deny'];

function initDrafts(servers: BotMCPServerPolicy[]) {
  const enabled: Record<string, boolean> = {};
  const policies: Record<string, Record<string, MCPToolPolicyMode>> = {};

  for (const srv of servers) {
    enabled[srv.serverId] = srv.enabled;
    const toolMap: Record<string, MCPToolPolicyMode> = {
      ...(srv.toolPolicies ?? {}),
    };
    for (const tool of srv.tools ?? []) {
      if (!toolMap[tool.toolName]) {
        toolMap[tool.toolName] = srv.defaultPolicy ?? 'deny';
      }
    }
    policies[srv.serverId] = toolMap;
  }

  return { enabled, policies };
}

/**
 * Per-bot MCP proxy policy editor and decision log panel:
 * assignment switch, 3-state (Allow / Ask approval / Block) command matrix,
 * dual-mode endpoint wiring, and one-click activity triage.
 */
export function BotMCPProxyPanel({
  workspaceId,
  botId,
  botName,
  canManage,
  ignoreMissing = false,
}: BotMCPProxyPanelProps) {
  const t = useTranslations('mcpProxy');
  const tErrors = useTranslations('errors');

  const [servers, setServers] = useState<BotMCPServerPolicy[]>([]);
  const [logs, setLogs] = useState<MCPCallLogEntry[]>([]);
  const [draftEnabled, setDraftEnabled] = useState<Record<string, boolean>>({});
  const [draftPolicies, setDraftPolicies] = useState<
    Record<string, Record<string, MCPToolPolicyMode>>
  >({});
  const [searchByServer, setSearchByServer] = useState<Record<string, string>>(
    {},
  );
  const [loading, setLoading] = useState(true);
  const [hidden, setHidden] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [savingServerId, setSavingServerId] = useState<string | null>(null);
  const [savedServerId, setSavedServerId] = useState<string | null>(null);

  const loadData = useCallback(
    async (preserveError = false) => {
      setLoading(true);
      try {
        const [configRes, logsRes] = await Promise.allSettled([
          requestJson<BotMCPConfigResponse>(
            `/api/workspaces/${workspaceId}/bots/${botId}/mcp`,
          ),
          requestJson<MCPCallLogListResponse>(
            `/api/workspaces/${workspaceId}/bots/${botId}/mcp/logs`,
          ),
        ]);

        if (configRes.status === 'rejected') {
          const err: unknown = configRes.reason;
          if (
            ignoreMissing &&
            err instanceof ApiClientError &&
            err.status === 404
          ) {
            setHidden(true);
            if (!preserveError) {
              setErrorKey(null);
            }
            return;
          }
          setErrorKey(resolveErrorKey(err));
          return;
        }

        const loadedServers = configRes.value.servers ?? [];
        const initial = initDrafts(loadedServers);
        setServers(loadedServers);
        setDraftEnabled(initial.enabled);
        setDraftPolicies(initial.policies);
        setHidden(false);
        if (!preserveError) {
          setErrorKey(null);
        }

        if (logsRes.status === 'fulfilled') {
          setLogs(logsRes.value.logs ?? []);
        } else {
          setLogs([]);
        }
      } finally {
        setLoading(false);
      }
    },
    [botId, ignoreMissing, workspaceId],
  );

  useEffect(() => {
    void loadData(false);
  }, [loadData]);

  function getEffectivePolicy(
    server: BotMCPServerPolicy,
    toolName: string,
  ): MCPToolPolicyMode {
    return (
      draftPolicies[server.serverId]?.[toolName] ??
      server.toolPolicies?.[toolName] ??
      server.defaultPolicy ??
      'deny'
    );
  }

  function handleToggleAssigned(serverId: string) {
    if (!canManage || savingServerId) {
      return;
    }
    setSavedServerId(null);
    setDraftEnabled((prev) => ({
      ...prev,
      [serverId]: !prev[serverId],
    }));
  }

  function handleSetToolPolicy(
    serverId: string,
    toolName: string,
    mode: MCPToolPolicyMode,
  ) {
    if (!canManage || savingServerId) {
      return;
    }
    setSavedServerId(null);
    setDraftPolicies((prev) => ({
      ...prev,
      [serverId]: {
        ...(prev[serverId] ?? {}),
        [toolName]: mode,
      },
    }));
    if (mode === 'allow' || mode === 'ask') {
      // Enabling an allow/ask command on an unassigned server enables the server too.
      setDraftEnabled((prev) =>
        prev[serverId] ? prev : { ...prev, [serverId]: true },
      );
    }
  }

  async function saveServerPolicy(
    server: BotMCPServerPolicy,
    nextEnabled: boolean,
    nextToolPolicies: Record<string, MCPToolPolicyMode>,
  ) {
    if (!canManage || savingServerId) {
      return;
    }
    setSavingServerId(server.serverId);
    setSavedServerId(null);
    setErrorKey(null);

    const payload: UpdateBotMCPServerPolicyRequest = {
      enabled: nextEnabled,
      defaultPolicy: server.defaultPolicy ?? 'deny',
      toolPolicies: nextToolPolicies,
      expectedRevision: server.revision,
    };

    try {
      const updated = await requestJson<BotMCPServerPolicy>(
        `/api/workspaces/${workspaceId}/bots/${botId}/mcp/${server.serverId}`,
        {
          method: 'PUT',
          body: payload,
        },
      );
      setServers((prev) =>
        prev.map((item) =>
          item.serverId === updated.serverId ? updated : item,
        ),
      );
      setDraftEnabled((prev) => ({
        ...prev,
        [updated.serverId]: updated.enabled,
      }));
      setDraftPolicies((prev) => {
        const refreshed = initDrafts([updated]);
        return {
          ...prev,
          [updated.serverId]: refreshed.policies[updated.serverId] ?? {},
        };
      });
      setSavedServerId(updated.serverId);
    } catch (err) {
      setErrorKey(resolveErrorKey(err));
      if (err instanceof ApiClientError && err.status === 409) {
        await loadData(true);
      }
    } finally {
      setSavingServerId(null);
    }
  }

  async function handleSaveServer(server: BotMCPServerPolicy) {
    const enabled = draftEnabled[server.serverId] ?? server.enabled;
    const mergedPolicies: Record<string, MCPToolPolicyMode> = {
      ...(draftPolicies[server.serverId] ?? {}),
    };
    for (const tool of server.tools ?? []) {
      mergedPolicies[tool.toolName] = getEffectivePolicy(server, tool.toolName);
    }
    await saveServerPolicy(server, enabled, mergedPolicies);
  }

  async function handleTriageLog(
    entry: MCPCallLogEntry,
    mode: MCPToolPolicyMode,
  ) {
    const server = servers.find(
      (srv) =>
        (entry.serverId && srv.serverId === entry.serverId) ||
        srv.serverSlug === entry.serverSlug,
    );
    if (!server) {
      return;
    }
    const basePolicies: Record<string, MCPToolPolicyMode> = {
      ...(draftPolicies[server.serverId] ?? {}),
    };
    for (const tool of server.tools ?? []) {
      basePolicies[tool.toolName] = getEffectivePolicy(server, tool.toolName);
    }
    basePolicies[entry.toolName] = mode;
    await saveServerPolicy(server, true, basePolicies);
  }

  if (hidden) {
    return null;
  }

  const exposedToolNames = servers.flatMap((srv) => {
    const isAssigned = draftEnabled[srv.serverId] ?? srv.enabled;
    if (!isAssigned) {
      return [];
    }
    return (srv.tools ?? [])
      .filter((tool) => {
        const mode = getEffectivePolicy(srv, tool.toolName);
        return mode === 'allow' || mode === 'ask';
      })
      .map((tool) => `mcp__${srv.serverSlug}__${tool.toolName}`);
  });

  const activeServers = servers.filter(
    (srv) => draftEnabled[srv.serverId] ?? srv.enabled,
  );

  return (
    <Card className="flex flex-col gap-5">
      <div className="flex flex-col gap-1">
        <Eyebrow>{t('botEyebrow')}</Eyebrow>
        <h2 className="text-[20px] font-black tracking-[-0.01em] text-ink">
          {t('botTitle')}
        </h2>
        <p className="text-[14px] leading-[1.6] text-muted">{t('botLead')}</p>
      </div>

      <div className="rounded-md border border-mint-line bg-mint-soft px-4 py-3 text-[14px] leading-[1.5] text-ink">
        {t('securityCallout', { bot: botName })}
      </div>

      {!canManage ? <Alert tone="info">{t('botViewerNote')}</Alert> : null}

      {errorKey ? (
        <Alert tone="warning">{tErrors(errorKey as never)}</Alert>
      ) : null}

      {loading ? (
        <p className="text-[14px] text-muted">{t('botLoading')}</p>
      ) : servers.length === 0 ? (
        <p className="rounded-sm border border-line bg-paper-2 px-4 py-3 text-[14px] text-muted">
          {t('botEmpty')}
        </p>
      ) : (
        <div className="flex flex-col gap-5">
          {servers.map((srv) => {
            const assigned = draftEnabled[srv.serverId] ?? srv.enabled;
            const searchQuery = (searchByServer[srv.serverId] ?? '')
              .trim()
              .toLowerCase();
            const tools = srv.tools ?? [];
            const filteredTools = tools.filter((tool) => {
              if (!searchQuery) {
                return true;
              }
              return (
                tool.toolName.toLowerCase().includes(searchQuery) ||
                tool.description.toLowerCase().includes(searchQuery)
              );
            });

            let allowedCount = 0;
            let askCount = 0;
            let blockedCount = 0;
            for (const tool of tools) {
              const mode = getEffectivePolicy(srv, tool.toolName);
              if (mode === 'allow') {
                allowedCount += 1;
              } else if (mode === 'ask') {
                askCount += 1;
              } else {
                blockedCount += 1;
              }
            }

            const statusChip =
              srv.enforcementStatus === 'applied' ? (
                <StatusChip
                  status="budgetOk"
                  label={t('statusApplied', { revision: srv.revision })}
                />
              ) : srv.enforcementStatus === 'pending' ? (
                <StatusChip
                  status="budgetWarning"
                  label={t('statusPending', { revision: srv.revision })}
                />
              ) : (
                <StatusChip
                  status="idle"
                  label={t('statusNotRunning', { revision: srv.revision })}
                />
              );

            const isSavingThis = savingServerId === srv.serverId;

            return (
              <section
                key={srv.serverId}
                className="flex flex-col gap-4 rounded-md border border-line bg-paper-2 p-4"
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <div className="flex flex-wrap items-center gap-2.5">
                      <h3 className="text-[17px] font-bold text-ink">
                        {srv.serverName}
                      </h3>
                      <span className="rounded-pill border border-line bg-paper px-2.5 py-0.5 font-mono text-[12px] font-bold text-ink-2">
                        {srv.serverSlug}
                      </span>
                      {statusChip}
                    </div>
                    <p className="truncate font-mono text-[13px] text-ink-2">
                      {srv.endpointUrl}
                    </p>
                    <p className="text-[12px] font-bold text-muted">
                      {t('defaultFailClosed')}
                    </p>
                  </div>

                  <button
                    type="button"
                    role="switch"
                    aria-checked={assigned}
                    disabled={!canManage || savingServerId !== null}
                    onClick={() => handleToggleAssigned(srv.serverId)}
                    className={`inline-flex min-h-[44px] items-center gap-2.5 rounded-pill border px-4 py-2 text-[13px] font-bold transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
                      assigned
                        ? 'border-mint-line bg-mint-soft text-green-600'
                        : 'border-line bg-paper text-muted'
                    }`}
                  >
                    <span
                      aria-hidden="true"
                      className={`h-2.5 w-2.5 rounded-pill ${assigned ? 'bg-green' : 'bg-line-strong'}`}
                    />
                    <span>{t('assignedLabel', { bot: botName })}</span>
                  </button>
                </div>

                <div className="flex flex-wrap items-end justify-between gap-3">
                  <div className="min-w-[240px] flex-1">
                    <Input
                      label={t('searchPlaceholder')}
                      placeholder={t('searchPlaceholder')}
                      value={searchByServer[srv.serverId] ?? ''}
                      onChange={(event) =>
                        setSearchByServer((prev) => ({
                          ...prev,
                          [srv.serverId]: event.target.value,
                        }))
                      }
                    />
                  </div>
                  <p className="text-[13px] font-bold tabular-nums text-ink-2">
                    {t('counters', {
                      allowed: allowedCount,
                      ask: askCount,
                      blocked: blockedCount,
                    })}
                  </p>
                </div>

                {tools.length === 0 ? (
                  <p className="rounded-sm border border-line bg-paper px-3.5 py-2.5 text-[14px] text-muted">
                    {t('noToolsDiscovered')}
                  </p>
                ) : filteredTools.length === 0 ? (
                  <p className="rounded-sm border border-line bg-paper px-3.5 py-2.5 text-[14px] text-muted">
                    {t('noToolsMatch')}
                  </p>
                ) : (
                  <div className="overflow-x-auto">
                    <table className="w-full border-separate border-spacing-y-2 text-left">
                      <thead>
                        <tr className="text-[12px] font-bold uppercase tracking-[0.06em] text-muted">
                          <th className="px-3 py-1">{t('colCommand')}</th>
                          <th className="px-3 py-1">{t('colDescription')}</th>
                          <th className="px-3 py-1">{t('colPolicy')}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {filteredTools.map((tool) => {
                          const currentMode = getEffectivePolicy(
                            srv,
                            tool.toolName,
                          );
                          const rowClasses =
                            currentMode === 'deny'
                              ? 'border border-dashed border-amber-line bg-amber-tint'
                              : currentMode === 'ask'
                                ? 'border border-blue-line bg-blue-tint'
                                : 'border border-line bg-paper';

                          return (
                            <tr key={tool.toolName} className={rowClasses}>
                              <td className="rounded-l-sm px-3 py-2.5 align-top">
                                <div className="font-mono text-[13px] font-bold text-ink">
                                  {tool.toolName}
                                </div>
                                {currentMode === 'ask' ? (
                                  <div className="mt-1 text-[12px] font-bold text-blue">
                                    {t('askHint')}
                                  </div>
                                ) : currentMode === 'deny' ? (
                                  <div className="mt-1 text-[12px] font-bold text-amber">
                                    {t('blockHint')}
                                  </div>
                                ) : null}
                              </td>
                              <td className="px-3 py-2.5 align-top text-[13px] leading-[1.5] text-ink-2">
                                {tool.description || '—'}
                              </td>
                              <td className="rounded-r-sm px-3 py-2.5 align-top">
                                <div
                                  role="group"
                                  aria-label={tool.toolName}
                                  className="inline-flex flex-wrap gap-1.5"
                                >
                                  {POLICY_ORDER.map((mode) => {
                                    const active = currentMode === mode;
                                    const label =
                                      mode === 'allow'
                                        ? t('policyAllow')
                                        : mode === 'ask'
                                          ? t('policyAsk')
                                          : t('policyBlock');
                                    const activeClasses =
                                      mode === 'allow'
                                        ? 'border-green-600 bg-green-600 text-paper'
                                        : mode === 'ask'
                                          ? 'border-blue bg-blue text-paper'
                                          : 'border-dashed border-amber-line bg-paper text-amber';
                                    const inactiveClasses =
                                      'border-line bg-paper text-ink-2 hover:bg-paper-2';

                                    return (
                                      <button
                                        key={mode}
                                        type="button"
                                        aria-pressed={active}
                                        disabled={
                                          !canManage || savingServerId !== null
                                        }
                                        onClick={() =>
                                          handleSetToolPolicy(
                                            srv.serverId,
                                            tool.toolName,
                                            mode,
                                          )
                                        }
                                        className={`inline-flex min-h-[44px] items-center justify-center rounded-sm border px-3 text-[13px] font-bold transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:opacity-60 ${
                                          active
                                            ? activeClasses
                                            : inactiveClasses
                                        }`}
                                      >
                                        {label}
                                      </button>
                                    );
                                  })}
                                </div>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                )}

                {canManage ? (
                  <div className="flex flex-wrap items-center gap-3">
                    <Button
                      disabled={savingServerId !== null}
                      onClick={() => void handleSaveServer(srv)}
                    >
                      {isSavingThis ? t('savingPolicy') : t('savePolicy')}
                    </Button>
                    {savedServerId === srv.serverId ? (
                      <StatusChip status="working" label={t('savedPolicy')} />
                    ) : null}
                  </div>
                ) : null}
              </section>
            );
          })}
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="flex flex-col gap-3 rounded-lg bg-terminal p-5 text-on-dark">
          <div>
            <p className="text-[11px] font-bold uppercase tracking-[0.08em] text-green-light">
              {t('wiringEyebrow')}
            </p>
            <h3 className="text-[17px] font-bold text-paper">
              {t('wiringTitle')}
            </h3>
          </div>

          {activeServers.length === 0 ? (
            <p className="text-[13px] text-on-dark-muted">
              {t('wiringNone', { bot: botName })}
            </p>
          ) : (
            <div className="flex flex-col gap-3 text-[13px]">
              <div>
                <p className="font-bold text-on-dark-muted">
                  {t('wiringCliLabel')}
                </p>
                <ul className="mt-1 flex flex-col gap-1 font-mono text-green-light">
                  {activeServers.map((srv) => (
                    <li key={srv.serverId}>
                      {`http://egress:8081/mcp/${srv.serverSlug}`}
                    </li>
                  ))}
                </ul>
              </div>
              <div>
                <p className="font-bold text-on-dark-muted">
                  {t('wiringApiLabel')}
                </p>
                {exposedToolNames.length === 0 ? (
                  <p className="mt-1 text-on-dark-muted">
                    {t('wiringNone', { bot: botName })}
                  </p>
                ) : (
                  <ul className="mt-1 flex flex-wrap gap-1.5 font-mono text-[12px]">
                    {exposedToolNames.map((prefixed) => (
                      <li
                        key={prefixed}
                        className="rounded-xs bg-dark-2 px-2 py-1 text-on-dark"
                      >
                        {prefixed}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          )}
        </div>

        <div className="flex flex-col gap-3 rounded-lg bg-terminal p-5 text-on-dark">
          <div>
            <p className="text-[11px] font-bold uppercase tracking-[0.08em] text-green-light">
              {t('logsEyebrow')}
            </p>
            <h3 className="text-[17px] font-bold text-paper">
              {t('logsTitle')}
            </h3>
          </div>

          {logs.length === 0 ? (
            <p className="text-[13px] text-on-dark-muted">{t('logsEmpty')}</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-left text-[12px]">
                <thead>
                  <tr className="border-b border-dark-2 text-on-dark-muted">
                    <th className="py-1.5 pr-3">{t('logColTime')}</th>
                    <th className="py-1.5 pr-3">{t('logColTool')}</th>
                    <th className="py-1.5 pr-3">{t('logColAction')}</th>
                    <th className="py-1.5">{t('logColLatency')}</th>
                  </tr>
                </thead>
                <tbody>
                  {logs.map((entry) => {
                    const isWarning =
                      entry.action === 'blocked' ||
                      entry.action === 'denied_by_human' ||
                      entry.action === 'upstream_error';
                    const actionKey =
                      LOG_ACTION_KEY_MAP[entry.action] ?? 'logActionAllowed';
                    const showTriage =
                      canManage &&
                      (entry.action === 'blocked' ||
                        entry.action === 'denied_by_human') &&
                      servers.some(
                        (srv) =>
                          (entry.serverId && srv.serverId === entry.serverId) ||
                          srv.serverSlug === entry.serverSlug,
                      );

                    return (
                      <tr
                        key={entry.id}
                        className="border-b border-dark-2/70 align-middle"
                      >
                        <td className="py-2 pr-3 font-mono tabular-nums text-on-dark-muted">
                          {entry.createdAt}
                        </td>
                        <td className="py-2 pr-3 font-mono text-paper">
                          {entry.serverSlug}/{entry.toolName}
                        </td>
                        <td className="py-2 pr-3">
                          <div className="flex flex-wrap items-center gap-2">
                            <span
                              className={
                                isWarning
                                  ? 'font-bold text-amber-on-dark'
                                  : 'font-bold text-green-light'
                              }
                            >
                              {t(actionKey as never)}
                            </span>
                            {showTriage ? (
                              <div className="inline-flex gap-1.5">
                                <button
                                  type="button"
                                  disabled={savingServerId !== null}
                                  onClick={() =>
                                    void handleTriageLog(entry, 'allow')
                                  }
                                  className="inline-flex min-h-[44px] items-center rounded-xs bg-green-600 px-2.5 text-[12px] font-bold text-paper transition hover:bg-green focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                                >
                                  {t('triageAllow')}
                                </button>
                                <button
                                  type="button"
                                  disabled={savingServerId !== null}
                                  onClick={() =>
                                    void handleTriageLog(entry, 'ask')
                                  }
                                  className="inline-flex min-h-[44px] items-center rounded-xs bg-blue px-2.5 text-[12px] font-bold text-paper transition hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                                >
                                  {t('triageAsk')}
                                </button>
                              </div>
                            ) : null}
                          </div>
                        </td>
                        <td className="py-2 font-mono tabular-nums text-on-dark-muted">
                          {`${entry.latencyMs}ms`}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </Card>
  );
}
