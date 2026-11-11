'use client';

import {
  ArrowLeft,
  Folder,
  FileText,
  Maximize2,
  MessageSquare,
  Minimize2,
  Play,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  SlidersHorizontal,
  Square,
  Terminal as TerminalIcon,
  Monitor,
  ListChecks,
  Wrench,
  X,
} from 'lucide-react';
import Link from 'next/link';
import { useParams } from 'next/navigation';
import {
  type FormEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react';
import { useTranslations } from 'next-intl';

import {
  Alert,
  BotAvatar,
  Button,
  Card,
  type ChipStatus,
  Eyebrow,
  Input,
  StatusChip,
  Textarea,
} from '@/components/ui';
import { ActionTimeline } from '@/components/workspace/ActionTimeline';
import { BotWorkspaceSkeleton } from '@/components/workspace/BotWorkspaceSkeleton';
import { VncViewport } from '@/components/workspace/VncViewport';
import { RunnerControl } from '@/components/chat/RunnerControl';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { ToolCallsSidebar } from '@/components/chat/ToolCallsSidebar';
import type { ToolTraceSummary } from '@/components/chat/toolTrace';
import { useAuth } from '@/context/AuthContext';
import {
  ApiClientError,
  createBotVncTicket,
  execBotTerminal,
  getBot,
  getBotEnvironment,
  listBotFiles,
  getWorkspaceGuardrails,
  listBotSecrets,
  listDesktopActions,
  readBotFile,
  releaseBotControl,
  restartBotEnvironment,
  startBotEnvironment,
  stopBotEnvironment,
  takeBotControl,
  writeBotFile,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import { canManageBot, canTakeDesktopControl } from '@/lib/rbac';
import type {
  AvatarMood,
  Bot,
  DesktopAction,
  EnvironmentStateResponse,
  EnvironmentStatus,
  FileEntry,
  StreamTicketResponse,
} from '@/types/api';

type WorkspaceTab = 'desktop' | 'terminal' | 'files' | 'actions' | 'toolCalls';

interface TerminalEntry {
  id: string;
  command: string;
  cwd: string;
  stdout: string;
  stderr: string;
  exitCode: number;
}

function mapEnvStatusToChip(
  envStatus: EnvironmentStatus,
  botStatus: Bot['status'],
): ChipStatus {
  if (envStatus === 'running') {
    return botStatus === 'action' ? 'working' : botStatus;
  }
  if (envStatus === 'creating') {
    return 'thinking';
  }
  if (envStatus === 'error') {
    return 'budgetWarning';
  }
  return 'stopped';
}

function mapEnvMood(
  envStatus: EnvironmentStatus,
  botMood: AvatarMood,
): AvatarMood {
  if (envStatus === 'running') {
    return botMood === 'offline' ? 'idle' : botMood;
  }
  if (envStatus === 'creating') {
    return 'thinking';
  }
  return 'offline';
}

/**
 * Viewport retries reuse the current ticket. A new ticket aborts that
 * socket, so refresh slowly. 15s stays inside the 60s HMAC lifetime
 * (defaultVNCTicketTTL) and still replaces a ticket that would expire
 * while websockify is booting.
 */
export const VNC_TICKET_REFRESH_MS = 15_000;

function buildVncWsUrl(wsPath: string): string {
  if (typeof window === 'undefined' || !wsPath) {
    return '';
  }
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${wsPath}`;
}

function parentHomePath(path: string): string {
  if (!path || path === '/home/bot') {
    return '/home/bot';
  }
  const trimmed = path.replace(/\/+$/, '');
  const idx = trimmed.lastIndexOf('/');
  if (idx <= '/home/bot'.length) {
    return '/home/bot';
  }
  return trimmed.slice(0, idx) || '/home/bot';
}

function percentOf(used: number, limit: number): number {
  if (limit <= 0) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round((used / limit) * 100)));
}

/**
 * Interactive bot workspace presenting session/control lock status,
 * tabbed computer views (noVNC desktop, gRPC terminal, /home/bot file browser),
 * sandbox lifecycle controls, and live resource meters.
 */
export function BotWorkspaceView() {
  const t = useTranslations('workspace');
  const tErrors = useTranslations('errors');
  const tBuilder = useTranslations('builder');
  const params = useParams<{ botId: string }>();
  const botId = typeof params?.botId === 'string' ? params.botId : '';
  const { user, activeWorkspace } = useAuth();

  const [bot, setBot] = useState<Bot | null>(null);
  const [env, setEnv] = useState<EnvironmentStateResponse | null>(null);
  const [secretCount, setSecretCount] = useState<number | null>(null);
  const [guardrailSummary, setGuardrailSummary] = useState<{
    enabled: boolean;
    activeRuleCount: number;
  } | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadErrorKey, setLoadErrorKey] = useState<ErrorTranslationKey | null>(
    null,
  );
  const [actionErrorKey, setActionErrorKey] =
    useState<ErrorTranslationKey | null>(null);

  const [activeTab, setActiveTab] = useState<WorkspaceTab>('desktop');
  const [mobileChatOpen, setMobileChatOpen] = useState(false);
  const [desktopFullscreen, setDesktopFullscreen] = useState(false);
  const [fullscreenChatOpen, setFullscreenChatOpen] = useState(false);
  const [browserFullscreen, setBrowserFullscreen] = useState(false);
  const computerPanelRef = useRef<HTMLDivElement | null>(null);
  const fullscreenTriggerRef = useRef<HTMLButtonElement | null>(null);
  const wasFullscreenRef = useRef(false);
  const [toolTrace, setToolTrace] = useState<ToolTraceSummary | null>(null);
  const [selectedToolTurnKey, setSelectedToolTurnKey] = useState<string | null>(
    null,
  );
  const [scrollRequest, setScrollRequest] = useState<{
    messageId: string;
    nonce: number;
  } | null>(null);
  const [envBusy, setEnvBusy] = useState<'start' | 'stop' | 'restart' | null>(
    null,
  );
  const [controlBusy, setControlBusy] = useState(false);
  const [handbackNote, setHandbackNote] = useState('');

  const [ticket, setTicket] = useState<StreamTicketResponse | null>(null);
  const [ticketNonce, setTicketNonce] = useState(0);
  const [vncConnected, setVncConnected] = useState(false);
  const ticketRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearTicketRefresh = useCallback(() => {
    if (ticketRefreshTimer.current !== null) {
      clearTimeout(ticketRefreshTimer.current);
      ticketRefreshTimer.current = null;
    }
  }, []);

  const handleConnectFailed = useCallback(() => {
    if (ticketRefreshTimer.current !== null) return;
    ticketRefreshTimer.current = setTimeout(() => {
      ticketRefreshTimer.current = null;
      setTicketNonce((nonce) => nonce + 1);
    }, VNC_TICKET_REFRESH_MS);
  }, []);

  const [terminalEntries, setTerminalEntries] = useState<TerminalEntry[]>([]);
  const [terminalCommand, setTerminalCommand] = useState('');
  const [terminalWorkdir, setTerminalWorkdir] = useState('/home/bot');
  const [terminalBusy, setTerminalBusy] = useState(false);

  const [filesPath, setFilesPath] = useState('/home/bot');
  const [fileEntries, setFileEntries] = useState<FileEntry[]>([]);
  const [filesLoading, setFilesLoading] = useState(false);
  const [pathInput, setPathInput] = useState('/home/bot/notes.txt');
  const [selectedFilePath, setSelectedFilePath] = useState<string | null>(null);
  const [editorContent, setEditorContent] = useState('');
  const [fileBusy, setFileBusy] = useState<'read' | 'write' | null>(null);
  const [fileSavedPath, setFileSavedPath] = useState<string | null>(null);
  const [desktopActions, setDesktopActions] = useState<DesktopAction[]>([]);
  const [retentionDays, setRetentionDays] = useState(7);

  useEffect(() => {
    if (!activeWorkspace || !botId) {
      setBot(null);
      setEnv(null);
      setLoading(false);
      return;
    }

    let cancelled = false;
    setLoading(true);
    setLoadErrorKey(null);
    setActionErrorKey(null);
    setTicket(null);

    void (async () => {
      try {
        const [fetchedBot, fetchedEnv, fetchedSecrets, fetchedGuardrails] =
          await Promise.all([
            getBot(activeWorkspace.id, botId),
            getBotEnvironment(activeWorkspace.id, botId),
            listBotSecrets(activeWorkspace.id, botId).catch(() => null),
            Promise.resolve(getWorkspaceGuardrails(activeWorkspace.id)).catch(
              () => null,
            ),
          ]);
        if (!cancelled) {
          setBot(fetchedBot);
          setEnv(fetchedEnv);
          setSecretCount(fetchedSecrets ? fetchedSecrets.secrets.length : null);
          setGuardrailSummary(
            fetchedGuardrails?.policy
              ? {
                  enabled: fetchedGuardrails.policy.enabled,
                  activeRuleCount: fetchedGuardrails.activeRuleCount ?? 0,
                }
              : null,
          );
        }
      } catch (err) {
        if (!cancelled) {
          setBot(null);
          setEnv(null);
          setLoadErrorKey(resolveErrorKey(err));
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
  }, [activeWorkspace, botId]);

  const envStatus = env?.status ?? 'stopped';
  const envId = env?.envId ?? '';
  const controlHolderId = env?.controlHolder?.userId ?? '';

  // A recovered desktop, a stopped machine, or another tab must not mint
  // a ticket that would tear down the socket the viewport just opened.
  useEffect(() => {
    if (vncConnected || envStatus !== 'running' || activeTab !== 'desktop') {
      clearTicketRefresh();
    }
  }, [vncConnected, envStatus, activeTab, clearTicketRefresh]);

  useEffect(() => clearTicketRefresh, [clearTicketRefresh]);

  useEffect(() => {
    if (
      !activeWorkspace ||
      !bot ||
      envStatus !== 'running' ||
      activeTab !== 'desktop'
    ) {
      if (envStatus !== 'running') {
        setTicket(null);
        setVncConnected(false);
      }
      return;
    }

    let cancelled = false;
    void (async () => {
      try {
        const issued = await createBotVncTicket(activeWorkspace.id, bot.id);
        if (!cancelled) {
          setTicket(issued);
        }
      } catch (err) {
        if (!cancelled) {
          setActionErrorKey(resolveErrorKey(err));
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    activeWorkspace,
    bot,
    envStatus,
    envId,
    controlHolderId,
    activeTab,
    ticketNonce,
  ]);

  const loadDirectory = useCallback(
    async (targetPath: string) => {
      if (!activeWorkspace || !bot || envStatus !== 'running') {
        return;
      }
      setFilesLoading(true);
      setActionErrorKey(null);
      try {
        const res = await listBotFiles(activeWorkspace.id, bot.id, targetPath);
        setFilesPath(res.path);
        setFileEntries(res.entries);
      } catch (err) {
        setActionErrorKey(resolveErrorKey(err));
      } finally {
        setFilesLoading(false);
      }
    },
    [activeWorkspace, bot, envStatus],
  );

  useEffect(() => {
    if (activeTab === 'files' && envStatus === 'running') {
      void loadDirectory(filesPath);
    }
  }, [activeTab, envStatus, filesPath, loadDirectory]);

  useEffect(() => {
    if (
      !activeWorkspace ||
      !bot ||
      envStatus !== 'running' ||
      (activeTab !== 'desktop' && activeTab !== 'actions')
    ) {
      return;
    }
    let cancelled = false;
    void (async () => {
      try {
        const listed = await listDesktopActions(activeWorkspace.id, bot.id);
        if (!cancelled) {
          setDesktopActions(listed.actions);
          setRetentionDays(listed.screenshotRetentionDays || 7);
        }
      } catch (err) {
        // Phase 2 fixtures answer unknown URLs with 404. A missing timeline
        // must not surface as an action error on the desktop tab.
        if (
          !cancelled &&
          !(err instanceof ApiClientError && err.status === 404)
        ) {
          setActionErrorKey(resolveErrorKey(err));
        } else if (!cancelled) {
          setDesktopActions([]);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [activeWorkspace, bot, envStatus, activeTab, controlHolderId]);

  const isFullscreen =
    desktopFullscreen && envStatus === 'running' && activeTab === 'desktop';

  useEffect(() => {
    if (
      desktopFullscreen &&
      (envStatus !== 'running' || activeTab !== 'desktop')
    ) {
      setDesktopFullscreen(false);
      setFullscreenChatOpen(false);
    }
  }, [desktopFullscreen, envStatus, activeTab]);

  useEffect(() => {
    if (typeof document === 'undefined') {
      return;
    }
    const handleFullscreenChange = () => {
      setBrowserFullscreen(
        Boolean(
          document.fullscreenElement &&
          document.fullscreenElement === computerPanelRef.current,
        ),
      );
    };
    document.addEventListener('fullscreenchange', handleFullscreenChange);
    return () => {
      document.removeEventListener('fullscreenchange', handleFullscreenChange);
    };
  }, []);

  useEffect(() => {
    if (!isFullscreen) {
      if (wasFullscreenRef.current) {
        wasFullscreenRef.current = false;
        fullscreenTriggerRef.current?.focus();
      }
      return;
    }

    wasFullscreenRef.current = true;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') {
        return;
      }
      if (document.querySelector('dialog[open]')) {
        return;
      }
      event.preventDefault();
      if (document.fullscreenElement) {
        void document.exitFullscreen?.().catch(() => {});
      }
      setFullscreenChatOpen(false);
      setDesktopFullscreen(false);
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [isFullscreen]);

  if (!activeWorkspace) {
    return null;
  }

  if (loading) {
    return <BotWorkspaceSkeleton />;
  }

  if (loadErrorKey || !bot) {
    return (
      <div className="flex flex-col gap-4">
        <Alert tone="warning">{tErrors(loadErrorKey ?? 'botNotFound')}</Alert>
        <Link
          href="/"
          className="inline-flex min-h-[44px] w-fit items-center rounded-sm border-[1.5px] border-line bg-paper px-4 text-[14px] font-bold text-green-600 hover:bg-mint-soft"
        >
          {tBuilder('backToFleet')}
        </Link>
      </div>
    );
  }

  const workspaceId = activeWorkspace.id;
  const canManage = canManageBot(activeWorkspace.role, user?.id, bot);
  const canControl = canTakeDesktopControl(activeWorkspace.role);
  const controlHolder = env?.controlHolder ?? null;
  const userHoldsControl = Boolean(
    controlHolder && user && controlHolder.userId === user.id,
  );
  const lockedByOther = Boolean(controlHolder && !userHoldsControl);
  const cursorPoint = desktopActions.find(
    (action) =>
      action.hasCoord && action.coordX != null && action.coordY != null,
  );
  const canToggleControl =
    canControl && envStatus === 'running' && !lockedByOther && !controlBusy;

  async function handleLifecycle(action: 'start' | 'stop' | 'restart') {
    if (!bot || !canManage) {
      return;
    }
    setActionErrorKey(null);
    setEnvBusy(action);
    try {
      const updated =
        action === 'start'
          ? await startBotEnvironment(workspaceId, bot.id)
          : action === 'stop'
            ? await stopBotEnvironment(workspaceId, bot.id)
            : await restartBotEnvironment(workspaceId, bot.id);
      setEnv(updated);
      if (updated.status !== 'running') {
        setTicket(null);
        setVncConnected(false);
      }
    } catch (err) {
      setActionErrorKey(resolveErrorKey(err));
    } finally {
      setEnvBusy(null);
    }
  }

  async function handleTakeControl() {
    if (!bot || !canToggleControl) {
      return;
    }
    setActionErrorKey(null);
    setControlBusy(true);
    try {
      const updated = await takeBotControl(workspaceId, bot.id);
      setEnv(updated);
    } catch (err) {
      setActionErrorKey(resolveErrorKey(err));
    } finally {
      setControlBusy(false);
    }
  }

  async function handleReleaseControl() {
    if (!bot || !userHoldsControl || controlBusy) {
      return;
    }
    setActionErrorKey(null);
    setControlBusy(true);
    try {
      const note = handbackNote.trim();
      const updated = await releaseBotControl(
        workspaceId,
        bot.id,
        note ? { note } : undefined,
      );
      setEnv(updated);
      setHandbackNote('');
    } catch (err) {
      setActionErrorKey(resolveErrorKey(err));
    } finally {
      setControlBusy(false);
    }
  }

  async function handleTerminalSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!bot || !canManage || envStatus !== 'running') {
      return;
    }
    const cmd = terminalCommand.trim();
    if (!cmd) {
      return;
    }

    setActionErrorKey(null);
    setTerminalBusy(true);
    try {
      const res = await execBotTerminal(workspaceId, bot.id, {
        command: cmd,
        workdir: terminalWorkdir,
      });
      setTerminalWorkdir(res.cwd);
      setTerminalEntries((prev) => [
        ...prev,
        {
          id: `${Date.now()}-${prev.length}`,
          command: cmd,
          cwd: res.cwd,
          stdout: res.stdout,
          stderr: res.stderr,
          exitCode: res.exitCode,
        },
      ]);
      setTerminalCommand('');
    } catch (err) {
      setActionErrorKey(resolveErrorKey(err));
    } finally {
      setTerminalBusy(false);
    }
  }

  async function handleOpenFile(targetPath: string) {
    if (!bot || envStatus !== 'running') {
      return;
    }
    const normalized = targetPath.trim();
    if (!normalized) {
      return;
    }
    setActionErrorKey(null);
    setFileSavedPath(null);
    setFileBusy('read');
    try {
      const res = await readBotFile(workspaceId, bot.id, normalized);
      setSelectedFilePath(res.path);
      setPathInput(res.path);
      setEditorContent(res.content);
    } catch (err) {
      if (err instanceof ApiClientError && err.code === 'file.not_found') {
        setSelectedFilePath(normalized);
        setPathInput(normalized);
        setEditorContent('');
      } else {
        setActionErrorKey(resolveErrorKey(err));
      }
    } finally {
      setFileBusy(null);
    }
  }

  async function handleOpenPathForm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await handleOpenFile(pathInput);
  }

  async function handleSaveFile() {
    if (!bot || !canManage || !selectedFilePath || envStatus !== 'running') {
      return;
    }
    setActionErrorKey(null);
    setFileSavedPath(null);
    setFileBusy('write');
    try {
      const saved = await writeBotFile(workspaceId, bot.id, {
        path: selectedFilePath,
        content: editorContent,
      });
      setSelectedFilePath(saved.path);
      setEditorContent(saved.content);
      setFileSavedPath(saved.path);
      await loadDirectory(filesPath);
    } catch (err) {
      setActionErrorKey(resolveErrorKey(err));
    } finally {
      setFileBusy(null);
    }
  }

  const stats = env?.stats ?? {
    cpuPercent: 0,
    memoryUsedMiB: 0,
    memoryLimitMiB: env?.memoryMiB ?? 4096,
    volumeUsedMiB: 0,
    volumeLimitMiB: 10240,
  };
  const cpuVcpus = Math.max(1, Math.round((env?.cpuMillis ?? 2000) / 1000));
  const cpuPercentRounded = Math.min(
    100,
    Math.max(0, Math.round(stats.cpuPercent)),
  );
  const memPercent = percentOf(stats.memoryUsedMiB, stats.memoryLimitMiB);
  const volPercent = percentOf(stats.volumeUsedMiB, stats.volumeLimitMiB);

  const wsUrl = ticket ? buildVncWsUrl(ticket.wsPath) : '';

  function exitDesktopFullscreen() {
    if (typeof document !== 'undefined' && document.fullscreenElement) {
      void document.exitFullscreen?.().catch(() => {});
    }
    setFullscreenChatOpen(false);
    setDesktopFullscreen(false);
  }

  function toggleBrowserFullscreen() {
    if (typeof document === 'undefined') {
      return;
    }
    if (document.fullscreenElement) {
      void document.exitFullscreen?.().catch(() => {});
      return;
    }
    void computerPanelRef.current?.requestFullscreen?.().catch(() => {});
  }

  const vncStatusChip = !ticket
    ? { status: 'stopped' as const, label: t('vnc.disconnected') }
    : !vncConnected
      ? { status: 'thinking' as const, label: t('vnc.connecting') }
      : userHoldsControl
        ? {
            status: 'budgetWarning' as const,
            label: t('vnc.connectedInControl'),
          }
        : { status: 'working' as const, label: t('vnc.connectedViewOnly') };

  const sessionStateLabel =
    envStatus === 'running'
      ? t('session.stateRunning')
      : envStatus === 'creating'
        ? t('session.stateCreating')
        : envStatus === 'error'
          ? t('session.stateError')
          : t('session.stateStopped');

  const sessionPanelContent = (
    <>
      <div className="flex items-center justify-between gap-2 border-b border-line pb-3">
        <div className="min-w-0">
          <h2 className="text-[18px] font-black leading-[1.25] text-ink">
            {t('session.title')}
          </h2>
          <p className="text-[12px] text-muted">{sessionStateLabel}</p>
        </div>
        {isFullscreen ? (
          <button
            type="button"
            aria-label={t('computer.fullscreenChatClose')}
            onClick={() => setFullscreenChatOpen(false)}
            className="grid h-11 w-11 shrink-0 place-items-center rounded-sm border border-line bg-paper text-ink transition duration-150 hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        ) : null}
      </div>

      <div
        className={`rounded-sm border px-3 py-2 text-[13px] leading-[1.45] ${
          lockedByOther
            ? 'border-dashed border-amber-line bg-amber-tint text-amber'
            : userHoldsControl
              ? 'border-mint-line bg-mint-soft text-green-600'
              : 'border-line bg-paper-2 text-ink-2'
        }`}
      >
        {lockedByOther && controlHolder
          ? t('session.controlOther', {
              holder: controlHolder.displayName,
              email: controlHolder.email,
            })
          : userHoldsControl
            ? t('session.controlYours')
            : t('session.controlIdle', { name: bot.name })}
      </div>

      {env?.controlNote ? (
        <div className="rounded-sm border border-mint-line bg-mint-soft px-3 py-2.5">
          <Eyebrow>{t('session.handbackNoteEyebrow')}</Eyebrow>
          <p className="mt-1 text-[13px] font-bold text-ink">
            {env.controlNote}
          </p>
        </div>
      ) : null}

      <ChatPanel
        key={`${workspaceId}/${user?.id}/${bot.id}`}
        workspaceId={workspaceId}
        userId={user?.id ?? ''}
        botId={bot.id}
        name={bot.name}
        avatarSeed={bot.avatarSeed}
        canChat={activeWorkspace.role !== 'viewer'}
        canManage={canManage}
        role={activeWorkspace.role}
        variant="session"
        hideHeader
        selectedToolTurnKey={selectedToolTurnKey}
        onToolTraceChange={setToolTrace}
        scrollRequest={scrollRequest}
        onInspectToolTurn={(turnKey) => {
          setSelectedToolTurnKey(turnKey);
          setActiveTab('toolCalls');
          setMobileChatOpen(false);
          if (isFullscreen) {
            exitDesktopFullscreen();
          }
        }}
      />

      <p className="border-t border-line pt-2.5 text-[12px] text-muted">
        {t('session.footerHint')}
      </p>
    </>
  );

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Link
            href="/"
            className="inline-flex min-h-[44px] w-fit items-center gap-2 rounded-sm px-1 text-[14px] font-bold text-green-600 transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            <ArrowLeft className="h-4 w-4" aria-hidden="true" />
            <span>{t('backToFleet')}</span>
          </Link>
          <span aria-hidden="true" className="text-line-strong">
            /
          </span>
          <Eyebrow>{t('eyebrow')}</Eyebrow>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex min-w-0 flex-1 items-center gap-4">
            <BotAvatar
              seed={bot.avatarSeed}
              mood={mapEnvMood(envStatus, bot.mood)}
              size={56}
              tile
              decorative
            />
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <div className="flex flex-wrap items-center gap-3">
                <h1 className="text-[30px] font-black leading-[1.15] tracking-[-0.01em] text-ink">
                  {bot.name}
                </h1>
                <StatusChip
                  status={mapEnvStatusToChip(envStatus, bot.status)}
                  label={t(`status.${envStatus}`)}
                />
              </div>

              <div className="flex flex-wrap items-center gap-2 text-[13px]">
                <span className="rounded-xs border border-line bg-paper px-2.5 py-0.5 font-mono text-ink-2">
                  {t('llmChip', { mode: bot.llmMode, model: bot.llmModel })}
                </span>
                {env ? (
                  <span className="rounded-xs border border-line bg-paper px-2.5 py-0.5 font-mono text-ink-2">
                    {t('driverChip', {
                      driver: env.driver,
                      volumeRef: env.volumeRef,
                    })}
                  </span>
                ) : null}
                {secretCount !== null ? (
                  <Link
                    href={`/bots/${bot.id}/secrets`}
                    className="inline-flex items-center gap-1.5 rounded-xs border border-mint-line bg-mint-soft px-2.5 py-0.5 font-bold text-green-600 transition duration-150 hover:border-green hover:bg-mint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                  >
                    <ShieldCheck className="h-3.5 w-3.5" aria-hidden="true" />
                    <span>{t('egressChip', { count: secretCount })}</span>
                  </Link>
                ) : null}
                {guardrailSummary !== null ? (
                  <Link
                    href="/settings/guardrails"
                    className={
                      guardrailSummary.enabled
                        ? 'inline-flex items-center gap-1.5 rounded-xs border border-mint-line bg-mint-soft px-2.5 py-0.5 font-bold text-green-600 transition duration-150 hover:border-green hover:bg-mint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green'
                        : 'inline-flex items-center gap-1.5 rounded-xs border border-line bg-paper-2 px-2.5 py-0.5 font-bold text-muted transition duration-150 hover:border-line-strong focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green'
                    }
                  >
                    <ShieldCheck className="h-3.5 w-3.5" aria-hidden="true" />
                    <span>
                      {guardrailSummary.enabled
                        ? t('guardrailsChip', {
                            count: guardrailSummary.activeRuleCount,
                          })
                        : t('guardrailsPausedChip')}
                    </span>
                  </Link>
                ) : null}
                <span className="inline-flex items-center gap-1.5 rounded-xs border border-blue-line bg-blue-tint px-2.5 py-0.5 font-bold text-blue">
                  <span
                    aria-hidden="true"
                    className="h-2 w-2 rounded-full bg-blue"
                  />
                  <span>
                    {t('watchersCount', {
                      count: env?.watchers.length ?? 0,
                    })}
                  </span>
                </span>
              </div>
            </div>
          </div>

          <div
            id="computer-controls"
            className="flex flex-wrap items-center gap-2.5"
          >
            <RunnerControl
              key={`${workspaceId}:${bot.id}:${user?.id}`}
              workspaceId={workspaceId}
              botId={bot.id}
              userId={user?.id ?? ''}
              canManage={canManage}
            />
            {envStatus === 'running' ? (
              <>
                <Button
                  variant="secondary"
                  disabled={!canManage || envBusy !== null}
                  onClick={() => void handleLifecycle('restart')}
                >
                  <RotateCcw className="h-4 w-4" aria-hidden="true" />
                  <span>
                    {envBusy === 'restart'
                      ? t('restartingSandbox')
                      : t('restartSandbox')}
                  </span>
                </Button>
                <Button
                  variant="secondary"
                  disabled={!canManage || envBusy !== null}
                  onClick={() => void handleLifecycle('stop')}
                >
                  <Square className="h-4 w-4" aria-hidden="true" />
                  <span>
                    {envBusy === 'stop'
                      ? t('stoppingSandbox')
                      : t('stopSandbox')}
                  </span>
                </Button>
              </>
            ) : (
              <Button
                variant="primary"
                disabled={
                  !canManage || envBusy !== null || envStatus === 'creating'
                }
                onClick={() => void handleLifecycle('start')}
              >
                <Play className="h-4 w-4" aria-hidden="true" />
                <span>
                  {envBusy === 'start'
                    ? t('startingSandbox')
                    : t('startSandbox')}
                </span>
              </Button>
            )}

            <Link
              href={`/bots/${bot.id}/settings`}
              className="inline-flex min-h-[44px] items-center gap-2 rounded-sm border-[1.5px] border-line bg-paper px-4 text-[15px] font-bold text-ink transition duration-150 hover:border-green-light hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
            >
              <SlidersHorizontal className="h-4 w-4" aria-hidden="true" />
              <span>{t('settingsLink')}</span>
            </Link>
          </div>
        </div>
      </div>

      {actionErrorKey ? (
        <Alert tone="warning">{tErrors(actionErrorKey)}</Alert>
      ) : null}

      {!isFullscreen ? (
        <div className="flex gap-1 rounded-md border border-line bg-paper p-1 lg:hidden">
          <button
            type="button"
            aria-pressed={mobileChatOpen}
            onClick={() => setMobileChatOpen(true)}
            className={`inline-flex min-h-[44px] flex-1 items-center justify-center rounded-sm px-3 text-[14px] font-bold ${
              mobileChatOpen ? 'bg-dark text-paper' : 'text-ink-2'
            }`}
          >
            {t('computer.tabs.chat')}
          </button>
          <button
            type="button"
            aria-pressed={!mobileChatOpen}
            onClick={() => setMobileChatOpen(false)}
            className={`inline-flex min-h-[44px] flex-1 items-center justify-center rounded-sm px-3 text-[14px] font-bold ${
              !mobileChatOpen ? 'bg-dark text-paper' : 'text-ink-2'
            }`}
          >
            {t('computer.tabs.computer')}
          </button>
        </div>
      ) : null}

      <div className="grid gap-5 lg:min-h-[600px] lg:grid-cols-[400px_minmax(0,1fr)] lg:items-stretch">
        {!isFullscreen ? (
          <Card
            className={`min-h-0 flex-col gap-3 p-4 lg:max-h-[780px] ${
              mobileChatOpen ? 'flex' : 'hidden lg:flex'
            }`}
          >
            {sessionPanelContent}
          </Card>
        ) : null}

        <section
          aria-label={t('computer.regionLabel', { name: bot.name })}
          className={`min-w-0 flex-col gap-4 ${
            isFullscreen ? 'flex' : mobileChatOpen ? 'hidden lg:flex' : 'flex'
          }`}
        >
          <div
            ref={computerPanelRef}
            role={isFullscreen ? 'dialog' : undefined}
            aria-modal={isFullscreen ? true : undefined}
            aria-label={
              isFullscreen
                ? t('computer.fullscreenRegionLabel', { name: bot.name })
                : undefined
            }
            className={
              isFullscreen
                ? 'fixed inset-0 z-50 flex flex-col overflow-hidden bg-terminal text-on-dark'
                : 'flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-line bg-paper shadow-card'
            }
          >
            {isFullscreen ? (
              <div className="flex min-h-[56px] flex-wrap items-center justify-between gap-3 border-b border-dark bg-dark-2 px-4 py-2">
                <div className="flex min-w-0 flex-wrap items-center gap-3">
                  <BotAvatar
                    seed={bot.avatarSeed}
                    mood={mapEnvMood(envStatus, bot.mood)}
                    size={30}
                    tile
                    decorative
                  />
                  <span className="whitespace-nowrap text-[16px] font-black text-paper">
                    {bot.name}
                  </span>
                  <StatusChip
                    status={vncStatusChip.status}
                    label={vncStatusChip.label}
                  />
                  <span className="hidden whitespace-nowrap font-mono text-[12px] text-on-dark-muted sm:inline">
                    {t('vnc.resolution')}
                  </span>
                  <span className="hidden whitespace-nowrap rounded-xs border border-dark bg-terminal px-2.5 py-1 font-mono text-[12px] text-on-dark md:inline-flex">
                    {t('computer.fullscreenTelemetry', {
                      cpu: cpuPercentRounded,
                      memUsed: stats.memoryUsedMiB,
                      memLimit: stats.memoryLimitMiB,
                    })}
                  </span>
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  {userHoldsControl ? (
                    <Button
                      variant="primary"
                      aria-pressed={true}
                      disabled={controlBusy}
                      className="whitespace-nowrap"
                      onClick={() => void handleReleaseControl()}
                    >
                      {t('computer.releaseControl')}
                    </Button>
                  ) : (
                    <Button
                      variant="onDark"
                      aria-pressed={false}
                      disabled={!canToggleControl}
                      onClick={() => void handleTakeControl()}
                      className="whitespace-nowrap"
                    >
                      {!canControl
                        ? t('computer.viewerReadonlyControl')
                        : lockedByOther && controlHolder
                          ? t('computer.lockedByOther', {
                              holder: controlHolder.displayName,
                            })
                          : t('computer.takeControl')}
                    </Button>
                  )}

                  <button
                    type="button"
                    aria-expanded={fullscreenChatOpen}
                    onClick={() => setFullscreenChatOpen((open) => !open)}
                    className={`inline-flex min-h-[44px] items-center gap-2 whitespace-nowrap rounded-sm border px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light ${
                      fullscreenChatOpen
                        ? 'border-green-light bg-green-600 text-paper'
                        : 'border-dark bg-terminal text-paper hover:border-green-light'
                    }`}
                  >
                    <MessageSquare className="h-4 w-4" aria-hidden="true" />
                    <span>
                      {fullscreenChatOpen
                        ? t('computer.fullscreenChatClose')
                        : t('computer.fullscreenChatToggle')}
                    </span>
                  </button>

                  <button
                    type="button"
                    aria-label={
                      browserFullscreen
                        ? t('computer.exitBrowserFullscreen')
                        : t('computer.browserFullscreen')
                    }
                    aria-pressed={browserFullscreen}
                    onClick={toggleBrowserFullscreen}
                    className="grid h-11 w-11 shrink-0 place-items-center rounded-sm border border-dark bg-terminal text-on-dark transition duration-150 hover:border-green-light hover:text-paper focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                  >
                    {browserFullscreen ? (
                      <Minimize2 className="h-4 w-4" aria-hidden="true" />
                    ) : (
                      <Maximize2 className="h-4 w-4" aria-hidden="true" />
                    )}
                  </button>

                  <button
                    type="button"
                    onClick={exitDesktopFullscreen}
                    className="inline-flex min-h-[44px] items-center gap-2 whitespace-nowrap rounded-sm border border-line-strong/40 bg-transparent px-3.5 text-[14px] font-bold text-paper transition duration-150 hover:border-green-light hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green-light"
                  >
                    <Minimize2 className="h-4 w-4" aria-hidden="true" />
                    <span>{t('computer.exitFullscreen')}</span>
                    <kbd className="rounded-xs border border-line-strong/40 bg-terminal px-1.5 py-0.5 font-mono text-[11px] text-on-dark-muted">
                      {t('computer.exitFullscreenHint')}
                    </kbd>
                  </button>
                </div>
              </div>
            ) : (
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
                <div
                  role="group"
                  aria-label={t('computer.tabsLabel')}
                  className="flex flex-wrap gap-1 rounded-md bg-paper-2 p-1"
                >
                  <button
                    type="button"
                    aria-pressed={activeTab === 'desktop'}
                    onClick={() => setActiveTab('desktop')}
                    className={`inline-flex min-h-[40px] items-center gap-2 rounded-sm px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      activeTab === 'desktop'
                        ? 'bg-dark text-paper'
                        : 'text-ink-2 hover:bg-paper hover:text-ink'
                    }`}
                  >
                    <Monitor className="h-4 w-4" aria-hidden="true" />
                    <span>{t('computer.tabs.desktop')}</span>
                  </button>
                  <button
                    type="button"
                    aria-label={t('computer.tabs.actions')}
                    aria-pressed={activeTab === 'actions'}
                    onClick={() => setActiveTab('actions')}
                    className={`inline-flex min-h-[40px] items-center gap-2 rounded-sm px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      activeTab === 'actions'
                        ? 'bg-dark text-paper'
                        : 'text-ink-2 hover:bg-paper hover:text-ink'
                    }`}
                  >
                    <ListChecks className="h-4 w-4" aria-hidden="true" />
                    <span>{t('computer.tabs.actions')}</span>
                    {desktopActions.length > 0 ? (
                      <span
                        aria-hidden="true"
                        className={`text-[12px] ${
                          activeTab === 'actions'
                            ? 'text-on-dark'
                            : 'text-muted'
                        }`}
                      >
                        {desktopActions.length}
                      </span>
                    ) : null}
                  </button>
                  <button
                    type="button"
                    aria-pressed={activeTab === 'terminal'}
                    onClick={() => setActiveTab('terminal')}
                    className={`inline-flex min-h-[40px] items-center gap-2 rounded-sm px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      activeTab === 'terminal'
                        ? 'bg-dark text-paper'
                        : 'text-ink-2 hover:bg-paper hover:text-ink'
                    }`}
                  >
                    <TerminalIcon className="h-4 w-4" aria-hidden="true" />
                    <span>{t('computer.tabs.terminal')}</span>
                  </button>
                  <button
                    type="button"
                    aria-pressed={activeTab === 'files'}
                    onClick={() => setActiveTab('files')}
                    className={`inline-flex min-h-[40px] items-center gap-2 rounded-sm px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      activeTab === 'files'
                        ? 'bg-dark text-paper'
                        : 'text-ink-2 hover:bg-paper hover:text-ink'
                    }`}
                  >
                    <Folder className="h-4 w-4" aria-hidden="true" />
                    <span>{t('computer.tabs.files')}</span>
                  </button>
                  <button
                    type="button"
                    aria-pressed={activeTab === 'toolCalls'}
                    onClick={() => setActiveTab('toolCalls')}
                    className={`inline-flex min-h-[40px] items-center gap-2 rounded-sm px-3.5 text-[14px] font-bold transition duration-150 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
                      activeTab === 'toolCalls'
                        ? 'bg-dark text-paper'
                        : 'text-ink-2 hover:bg-paper hover:text-ink'
                    }`}
                  >
                    <Wrench className="h-4 w-4" aria-hidden="true" />
                    <span>{t('computer.tabs.toolCalls')}</span>
                    {toolTrace && toolTrace.totalSteps > 0 ? (
                      <span
                        aria-hidden="true"
                        className={`text-[12px] ${
                          activeTab === 'toolCalls'
                            ? 'text-on-dark'
                            : 'text-muted'
                        }`}
                      >
                        {toolTrace.totalSteps}
                      </span>
                    ) : null}
                  </button>
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  {activeTab === 'desktop' && envStatus === 'running' ? (
                    <StatusChip
                      status={vncStatusChip.status}
                      label={vncStatusChip.label}
                    />
                  ) : null}

                  {userHoldsControl ? (
                    <Button
                      variant="primary"
                      aria-pressed={true}
                      disabled={controlBusy}
                      onClick={() => void handleReleaseControl()}
                    >
                      {t('computer.releaseControl')}
                    </Button>
                  ) : (
                    <Button
                      variant="secondary"
                      aria-pressed={false}
                      disabled={!canToggleControl}
                      onClick={() => void handleTakeControl()}
                    >
                      {!canControl
                        ? t('computer.viewerReadonlyControl')
                        : lockedByOther && controlHolder
                          ? t('computer.lockedByOther', {
                              holder: controlHolder.displayName,
                            })
                          : t('computer.takeControl')}
                    </Button>
                  )}

                  {activeTab === 'desktop' ? (
                    <Button
                      ref={fullscreenTriggerRef}
                      variant="secondary"
                      aria-label={t('computer.fullscreenAria', {
                        name: bot.name,
                      })}
                      disabled={envStatus !== 'running'}
                      onClick={() => setDesktopFullscreen(true)}
                      className="whitespace-nowrap"
                    >
                      <Maximize2 className="h-4 w-4" aria-hidden="true" />
                      <span>{t('computer.fullscreen')}</span>
                    </Button>
                  ) : null}
                </div>
              </div>
            )}

            {userHoldsControl ? (
              <div
                role="region"
                aria-label={t('computer.takeoverBannerTitle', {
                  name: bot.name,
                })}
                className="flex flex-wrap items-center gap-3 border-b border-mint-line bg-mint-soft px-4 py-3 text-ink"
              >
                <p className="flex-1 text-[13px] font-bold text-ink">
                  {t('computer.takeoverBannerTitle', { name: bot.name })}
                </p>
                <div className="flex min-w-[240px] flex-1 items-center gap-2">
                  <label htmlFor="handback-note-input" className="sr-only">
                    {t('computer.handbackNoteLabel')}
                  </label>
                  <input
                    id="handback-note-input"
                    type="text"
                    value={handbackNote}
                    onChange={(e) => setHandbackNote(e.target.value)}
                    placeholder={t('computer.handbackNotePlaceholder')}
                    maxLength={500}
                    className="min-h-[44px] w-full rounded-sm border-[1.5px] border-mint-line bg-paper px-3 text-[13px] text-ink placeholder:text-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                  />
                  <Button
                    variant="primary"
                    arrow
                    disabled={controlBusy}
                    onClick={() => void handleReleaseControl()}
                  >
                    {t('computer.releaseWithArrow', { name: bot.name })}
                  </Button>
                </div>
              </div>
            ) : null}

            {activeTab === 'toolCalls' ? (
              <div className="max-h-[560px] flex-1 overflow-y-auto p-4">
                <ToolCallsSidebar
                  trace={
                    toolTrace ?? {
                      chatMessages: [],
                      turns: [],
                      totalSteps: 0,
                      runningSteps: 0,
                      errorSteps: 0,
                      turnsByReplyId: new Map(),
                      unattachedTurnsByAnchor: new Map(),
                      leadingUnattachedTurns: [],
                    }
                  }
                  selectedTurnKey={selectedToolTurnKey}
                  onJumpToMessage={(messageId) => {
                    setMobileChatOpen(true);
                    setScrollRequest((prev) => ({
                      messageId,
                      nonce: (prev?.nonce ?? 0) + 1,
                    }));
                  }}
                />
              </div>
            ) : envStatus !== 'running' ? (
              <div className="flex min-h-[320px] flex-1 flex-col items-start justify-center gap-3 bg-paper-2 p-8">
                <h2 className="text-[22px] font-black text-ink">
                  {t('computer.stoppedTitle')}
                </h2>
                <p className="max-w-lg text-[15px] leading-[1.6] text-ink-2">
                  {t('computer.stoppedBody')}
                </p>
                {!canManage ? (
                  <Alert tone="info">{t('computer.viewerStoppedNotice')}</Alert>
                ) : null}
              </div>
            ) : activeTab === 'desktop' ? (
              <div
                className={
                  isFullscreen
                    ? 'flex min-h-0 flex-1 items-center justify-center overflow-auto bg-terminal p-3 sm:p-5'
                    : ''
                }
              >
                <VncViewport
                  hideHeader
                  wsUrl={wsUrl}
                  ticket={ticket?.ticket ?? ''}
                  inControl={userHoldsControl}
                  canTakeControl={canToggleControl}
                  controlBusy={controlBusy}
                  stageContainerClassName={
                    isFullscreen
                      ? userHoldsControl
                        ? 'max-w-[calc((100vh-184px)*1.6)]'
                        : 'max-w-[calc((100vh-116px)*1.6)]'
                      : ''
                  }
                  stageClassName={
                    isFullscreen
                      ? 'rounded-md border border-dark shadow-card-hover'
                      : ''
                  }
                  onConnectionChange={setVncConnected}
                  onConnectFailed={handleConnectFailed}
                  showCursor={
                    !userHoldsControl &&
                    cursorPoint?.coordX != null &&
                    cursorPoint.coordY != null
                  }
                  cursor={
                    cursorPoint?.coordX != null && cursorPoint.coordY != null
                      ? {
                          x: cursorPoint.coordX,
                          y: cursorPoint.coordY,
                          label: bot.name,
                        }
                      : null
                  }
                />
              </div>
            ) : activeTab === 'terminal' ? (
              <div className="flex flex-col">
                <div className="min-h-[320px] max-h-[420px] overflow-auto bg-terminal p-4 font-mono text-[13px] leading-[1.7] text-on-dark">
                  <p className="text-on-dark-muted">
                    {t('terminal.header', { cwd: terminalWorkdir })}
                  </p>
                  {terminalEntries.length === 0 ? (
                    <p className="mt-2 text-on-dark-muted">
                      {t('terminal.emptyHint')}
                    </p>
                  ) : (
                    <div className="mt-3 flex flex-col gap-3">
                      {terminalEntries.map((entry) => (
                        <div key={entry.id} className="flex flex-col gap-1">
                          <div className="flex items-center justify-between gap-2">
                            <span className="font-bold text-green-light">
                              {`$ ${entry.command}`}
                            </span>
                            <span
                              className={`text-[12px] font-bold ${
                                entry.exitCode === 0
                                  ? 'text-green-light'
                                  : 'text-amber-on-dark'
                              }`}
                            >
                              {t('terminal.exitCodeLabel', {
                                code: entry.exitCode,
                              })}
                            </span>
                          </div>
                          {entry.stdout ? (
                            <pre className="whitespace-pre-wrap break-words text-on-dark">
                              {entry.stdout}
                            </pre>
                          ) : null}
                          {entry.stderr ? (
                            <pre className="whitespace-pre-wrap break-words text-amber-on-dark">
                              {entry.stderr}
                            </pre>
                          ) : null}
                          {!entry.stdout && !entry.stderr ? (
                            <span className="text-on-dark-muted">
                              {t('terminal.noOutput')}
                            </span>
                          ) : null}
                        </div>
                      ))}
                    </div>
                  )}
                </div>

                <form
                  onSubmit={(e) => void handleTerminalSubmit(e)}
                  className="flex flex-col gap-3 border-t border-line bg-paper p-4"
                >
                  {!canManage ? (
                    <Alert tone="info">{t('terminal.viewerNotice')}</Alert>
                  ) : null}
                  <div className="flex flex-wrap items-end gap-3">
                    <div className="min-w-[220px] flex-1">
                      <Input
                        label={t('terminal.commandLabel')}
                        mono
                        value={terminalCommand}
                        readOnly={!canManage}
                        onChange={(e) => setTerminalCommand(e.target.value)}
                        placeholder={t('terminal.commandPlaceholder')}
                      />
                    </div>
                    <Button
                      type="submit"
                      disabled={
                        !canManage || terminalBusy || !terminalCommand.trim()
                      }
                    >
                      {terminalBusy
                        ? t('terminal.runningButton')
                        : t('terminal.runButton')}
                    </Button>
                  </div>
                </form>
              </div>
            ) : activeTab === 'actions' ? (
              <ActionTimeline
                workspaceId={workspaceId}
                botId={bot.id}
                actions={desktopActions}
                retentionDays={retentionDays}
              />
            ) : (
              <div className="flex flex-col gap-4 p-4">
                {!canManage ? (
                  <Alert tone="info">{t('files.viewerReadonly')}</Alert>
                ) : null}

                {fileSavedPath ? (
                  <Alert tone="success">
                    {t('files.savedFile', { path: fileSavedPath })}
                  </Alert>
                ) : null}

                <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line pb-3">
                  <p className="font-mono text-[13px] font-bold text-ink-2">
                    {t('files.currentPath', { path: filesPath })}
                  </p>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      variant="secondary"
                      disabled={filesPath === '/home/bot' || filesLoading}
                      onClick={() => setFilesPath(parentHomePath(filesPath))}
                    >
                      {t('files.upOneLevel')}
                    </Button>
                    <Button
                      variant="secondary"
                      disabled={filesLoading}
                      onClick={() => void loadDirectory(filesPath)}
                    >
                      <RefreshCw className="h-4 w-4" aria-hidden="true" />
                      <span>{t('files.refresh')}</span>
                    </Button>
                  </div>
                </div>

                <form
                  onSubmit={(e) => void handleOpenPathForm(e)}
                  className="flex flex-wrap items-end gap-3"
                >
                  <div className="min-w-[220px] flex-1">
                    <Input
                      label={t('files.newFileLabel')}
                      mono
                      value={pathInput}
                      onChange={(e) => setPathInput(e.target.value)}
                      placeholder={t('files.newFilePlaceholder')}
                    />
                  </div>
                  <Button
                    type="submit"
                    variant="secondary"
                    disabled={fileBusy !== null || !pathInput.trim()}
                  >
                    {t('files.openPathButton')}
                  </Button>
                </form>

                <div className="grid gap-4 lg:grid-cols-[260px_1fr]">
                  <div className="flex flex-col divide-y divide-line overflow-hidden rounded-sm border border-line bg-paper-2">
                    {fileEntries.length === 0 ? (
                      <p className="p-4 text-[13px] text-muted">
                        {t('files.emptyDir')}
                      </p>
                    ) : (
                      fileEntries.map((entry) => (
                        <button
                          key={entry.path}
                          type="button"
                          onClick={() => {
                            if (entry.isDir) {
                              setFilesPath(entry.path);
                            } else {
                              void handleOpenFile(entry.path);
                            }
                          }}
                          className={`flex min-h-[44px] items-center justify-between gap-2 px-3 py-2 text-left text-[13px] transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-green ${
                            selectedFilePath === entry.path
                              ? 'bg-mint-soft font-bold text-green-600'
                              : 'text-ink'
                          }`}
                        >
                          <span className="flex min-w-0 items-center gap-2 font-mono">
                            {entry.isDir ? (
                              <Folder
                                className="h-4 w-4 shrink-0 text-green-600"
                                aria-hidden="true"
                              />
                            ) : (
                              <FileText
                                className="h-4 w-4 shrink-0 text-muted"
                                aria-hidden="true"
                              />
                            )}
                            <span className="truncate">{entry.name}</span>
                          </span>
                          <span className="shrink-0 font-mono text-[11px] text-muted">
                            {entry.isDir
                              ? t('files.dirBadge')
                              : t('files.bytesLabel', {
                                  bytes: entry.sizeBytes,
                                })}
                          </span>
                        </button>
                      ))
                    )}
                  </div>

                  <div className="flex flex-col gap-3">
                    <p className="font-mono text-[13px] font-bold text-ink">
                      {selectedFilePath
                        ? t('files.editorTitle', { path: selectedFilePath })
                        : t('files.editorSelectPrompt')}
                    </p>
                    <Textarea
                      label={t('files.editorContentLabel')}
                      value={editorContent}
                      readOnly={!canManage || !selectedFilePath}
                      rows={10}
                      className="font-mono text-[13px]"
                      onChange={(e) => setEditorContent(e.target.value)}
                    />
                    <div className="flex justify-end">
                      <Button
                        disabled={
                          !canManage || !selectedFilePath || fileBusy !== null
                        }
                        onClick={() => void handleSaveFile()}
                      >
                        {fileBusy === 'write'
                          ? t('files.savingFile')
                          : t('files.saveFile')}
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            )}

            {isFullscreen ? (
              fullscreenChatOpen ? (
                <aside
                  aria-label={t('session.title')}
                  className="fixed inset-y-0 right-0 top-[56px] z-20 flex w-full max-w-[400px] flex-col gap-3 border-l border-line bg-paper p-4 text-ink shadow-card-hover"
                >
                  {sessionPanelContent}
                </aside>
              ) : null
            ) : (
              <div className="grid gap-4 border-t border-line bg-paper px-4 py-3 sm:grid-cols-2 xl:grid-cols-4">
                <div className="flex min-w-0 flex-col justify-between gap-1">
                  <Eyebrow>{t('meters.imageLabel')}</Eyebrow>
                  <p className="break-all font-mono text-[13px] font-bold text-ink">
                    {env?.image ?? bot.driverType}
                  </p>
                </div>

                {[
                  {
                    key: 'cpu',
                    label: t('meters.cpuLabel'),
                    value: t('meters.cpuValue', { percent: cpuPercentRounded }),
                    detail: t('meters.cpuDetail', { vcpus: cpuVcpus }),
                    percent: cpuPercentRounded,
                    barClass: 'bg-green',
                  },
                  {
                    key: 'memory',
                    label: t('meters.memoryLabel'),
                    value: t('meters.memoryValue', {
                      used: stats.memoryUsedMiB,
                    }),
                    detail: t('meters.memoryDetail', {
                      limit: stats.memoryLimitMiB,
                    }),
                    percent: memPercent,
                    barClass: 'bg-blue',
                  },
                  {
                    key: 'volume',
                    label: t('meters.volumeLabel'),
                    value: t('meters.volumeValue', {
                      used: stats.volumeUsedMiB,
                    }),
                    detail: t('meters.volumeDetail', {
                      limit: stats.volumeLimitMiB,
                    }),
                    percent: volPercent,
                    barClass: 'bg-dark',
                  },
                ].map((meter) => (
                  <div
                    key={meter.key}
                    className="flex min-w-0 flex-col justify-between gap-1.5"
                  >
                    <div className="flex items-baseline justify-between gap-2">
                      <Eyebrow>{meter.label}</Eyebrow>
                      <p className="text-[13px] font-black text-ink">
                        {meter.value}{' '}
                        <span className="font-normal text-muted">
                          {meter.detail}
                        </span>
                      </p>
                    </div>
                    <div
                      role="progressbar"
                      aria-label={meter.label}
                      aria-valuemin={0}
                      aria-valuemax={100}
                      aria-valuenow={meter.percent}
                      className="h-1.5 overflow-hidden rounded-pill bg-line"
                    >
                      <div
                        className={`h-full ${meter.barClass}`}
                        style={{ width: `${meter.percent}%` }}
                      />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
