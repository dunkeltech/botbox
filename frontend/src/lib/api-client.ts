import type {
  AcceptInvitationRequest,
  AuthSessionResponse,
  Bot,
  BotListResponse,
  CreateBotRequest,
  CreateSecretRequest,
  CreatedInvitationResponse,
  CreateInvitationRequest,
  CreateWorkspaceRequest,
  DesktopActionListResponse,
  EgressLogAction,
  EgressLogListResponse,
  EgressPolicyResponse,
  EnvironmentStateResponse,
  ErrorEnvelope,
  FileContentResponse,
  FileListResponse,
  InvitationListResponse,
  InvitationPreviewResponse,
  LoginRequest,
  MeResponse,
  ModelPriceListResponse,
  ModelPriceRequest,
  ReleaseControlRequest,
  SecretListResponse,
  SecretMetadata,
  SetupCompleteRequest,
  SetupStatusResponse,
  StreamTicketResponse,
  TerminalExecRequest,
  TerminalExecResponse,
  UpdateBotRequest,
  UpdateEgressPolicyRequest,
  UpdateWorkspaceRequest,
  UpdateMemberRoleRequest,
  UpdateSecretRequest,
  WorkspaceListResponse,
  WorkspaceMember,
  WorkspaceMemberListResponse,
  WorkspaceSummary,
  WriteFileContentRequest,
} from '@/types/api';
import type { components } from '@/types/openapi.gen';

/**
 * Error raised when an API request fails with a structured backend error or HTTP failure.
 */
export class ApiClientError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiClientError';
    this.status = status;
    this.code = code;
  }
}

interface SessionCallbacks {
  onRefreshed?: (session: AuthSessionResponse) => void;
  onCleared?: () => void;
}

/**
 * In-memory access token only (ADR-0001 / AGENTS.md §7).
 * Never persisted to localStorage or sessionStorage.
 */
let accessToken: string | null = null;
let refreshPromise: Promise<AuthSessionResponse> | null = null;
let sessionCallbacks: SessionCallbacks = {};

/** Returns the current in-memory access token, or null if signed out. */
export function getAccessToken(): string | null {
  return accessToken;
}

/** Sets or clears the in-memory access token. */
export function setAccessToken(token: string | null): void {
  accessToken = token;
}

/** Registers lifecycle hooks invoked when silent refresh updates or clears a session. */
export function setSessionCallbacks(callbacks: SessionCallbacks): void {
  sessionCallbacks = callbacks;
}

function isMutatingMethod(method: string): boolean {
  const upper = method.toUpperCase();
  return (
    upper === 'POST' ||
    upper === 'PATCH' ||
    upper === 'PUT' ||
    upper === 'DELETE'
  );
}

function parseErrorEnvelope(raw: unknown): { code?: string; message?: string } {
  if (typeof raw !== 'object' || raw === null) {
    return {};
  }
  const maybeEnvelope = raw as Partial<ErrorEnvelope>;
  if (typeof maybeEnvelope.error === 'object' && maybeEnvelope.error !== null) {
    const code =
      typeof maybeEnvelope.error.code === 'string'
        ? maybeEnvelope.error.code
        : undefined;
    const message =
      typeof maybeEnvelope.error.message === 'string'
        ? maybeEnvelope.error.message
        : undefined;
    return { code, message };
  }
  return {};
}

async function readError(response: Response): Promise<ApiClientError> {
  const fallbackCode =
    response.status === 429 ? 'rate_limited' : 'unknown_error';
  let code = fallbackCode;
  let message = response.statusText || 'Request failed';

  try {
    const text = await response.text();
    if (text.length > 0) {
      const parsed: unknown = JSON.parse(text);
      const envelope = parseErrorEnvelope(parsed);
      if (envelope.code) {
        code = envelope.code;
      }
      if (envelope.message) {
        message = envelope.message;
      }
    }
  } catch {
    // Non-JSON error bodies fall back to HTTP status text and fallback code.
  }

  return new ApiClientError(response.status, code, message);
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  body?: unknown;
  includeAuth?: boolean;
  retryOnUnauthorized?: boolean;
}

async function executeFetch(
  path: string,
  options: RequestOptions,
): Promise<Response> {
  const method = options.method ?? 'GET';
  const headers = new Headers();

  if (options.body !== undefined) {
    headers.set('Content-Type', 'application/json');
  }
  if (isMutatingMethod(method)) {
    headers.set('X-BotBox-CSRF', '1');
  }
  if (options.includeAuth !== false && accessToken) {
    headers.set('Authorization', `Bearer ${accessToken}`);
  }

  return fetch(path, {
    method,
    credentials: 'include',
    headers,
    body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
  });
}

/** Sends a typed same-origin request with session refresh and CSRF protection. */
export async function requestJson<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  let response = await executeFetch(path, options);

  if (response.status === 401 && options.retryOnUnauthorized !== false) {
    try {
      const refreshed = await refreshSession();
      sessionCallbacks.onRefreshed?.(refreshed);
      response = await executeFetch(path, options);
    } catch {
      accessToken = null;
      sessionCallbacks.onCleared?.();
      throw await readError(response);
    }
  }

  if (!response.ok) {
    throw await readError(response);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return (await response.json()) as T;
}

/** Fetches binary responses, including desktop screenshots, with the same session. */
export async function requestBlob(
  path: string,
  options: RequestOptions = {},
): Promise<Blob> {
  let response = await executeFetch(path, options);

  if (response.status === 401 && options.retryOnUnauthorized !== false) {
    try {
      const refreshed = await refreshSession();
      sessionCallbacks.onRefreshed?.(refreshed);
      response = await executeFetch(path, options);
    } catch {
      accessToken = null;
      sessionCallbacks.onCleared?.();
      throw await readError(response);
    }
  }

  if (!response.ok) {
    throw await readError(response);
  }

  return response.blob();
}

/**
 * Checks whether first-run setup is still pending.
 * Returns { setupRequired: false } when setup is already completed (HTTP 404).
 */
export async function getSetupStatus(): Promise<SetupStatusResponse> {
  try {
    return await requestJson<SetupStatusResponse>('/api/setup/status', {
      includeAuth: false,
      retryOnUnauthorized: false,
    });
  } catch (err) {
    if (
      err instanceof ApiClientError &&
      err.status === 404 &&
      err.code === 'setup.already_completed'
    ) {
      return { setupRequired: false };
    }
    throw err;
  }
}

/** Completes first-run setup using the server-generated bootstrap secret. */
export async function completeSetup(
  body: SetupCompleteRequest,
): Promise<AuthSessionResponse> {
  const session = await requestJson<AuthSessionResponse>(
    '/api/setup/complete',
    {
      method: 'POST',
      body,
      includeAuth: false,
      retryOnUnauthorized: false,
    },
  );
  accessToken = session.accessToken;
  return session;
}

/** Authenticates an existing user and starts an in-memory access + cookie refresh session. */
export async function login(body: LoginRequest): Promise<AuthSessionResponse> {
  const session = await requestJson<AuthSessionResponse>('/api/auth/login', {
    method: 'POST',
    body,
    includeAuth: false,
    retryOnUnauthorized: false,
  });
  accessToken = session.accessToken;
  return session;
}

/**
 * Coalesced refresh-token rotation using the HttpOnly refresh cookie.
 * Concurrent 401 retries share a single network request so rotation never triggers false reuse alerts.
 */
export async function refreshSession(): Promise<AuthSessionResponse> {
  if (refreshPromise) {
    return refreshPromise;
  }

  refreshPromise = (async () => {
    const response = await executeFetch('/api/auth/refresh', {
      method: 'POST',
      body: {},
      includeAuth: false,
      retryOnUnauthorized: false,
    });
    if (!response.ok) {
      accessToken = null;
      throw await readError(response);
    }
    const session = (await response.json()) as AuthSessionResponse;
    accessToken = session.accessToken;
    return session;
  })().finally(() => {
    refreshPromise = null;
  });

  return refreshPromise;
}

/** Revokes the active refresh session cookie and clears the in-memory access token. */
export async function logout(): Promise<void> {
  try {
    await requestJson<void>('/api/auth/logout', {
      method: 'POST',
      retryOnUnauthorized: false,
    });
  } finally {
    accessToken = null;
  }
}

/** Returns the signed-in user profile and accessible workspaces. */
export function getMe(): Promise<MeResponse> {
  return requestJson<MeResponse>('/api/auth/me');
}

/** Lists all workspaces the current user belongs to. */
export function listWorkspaces(): Promise<WorkspaceListResponse> {
  return requestJson<WorkspaceListResponse>('/api/workspaces');
}

/** Creates a new workspace with the caller as owner. */
export function createWorkspace(
  body: CreateWorkspaceRequest,
): Promise<WorkspaceSummary> {
  return requestJson<WorkspaceSummary>('/api/workspaces', {
    method: 'POST',
    body,
  });
}

/** Fetches a single workspace summary scoped to membership. */
export function getWorkspace(workspaceId: string): Promise<WorkspaceSummary> {
  return requestJson<WorkspaceSummary>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}`,
  );
}

/** Updates owner-only workspace profile, compute driver, locale, and screenshot retention. */
export function updateWorkspace(
  workspaceId: string,
  body: UpdateWorkspaceRequest,
): Promise<WorkspaceSummary> {
  return requestJson<WorkspaceSummary>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}`,
    { method: 'PATCH', body },
  );
}

/** Permanently deletes an idle workspace when the caller belongs to at least two workspaces. */
export function deleteWorkspace(workspaceId: string): Promise<void> {
  return requestJson<void>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}`,
    { method: 'DELETE' },
  );
}

/** Lists configured model pricing rates for the workspace. */
export function listModelPrices(
  workspaceId: string,
): Promise<ModelPriceListResponse> {
  return requestJson<ModelPriceListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/model-prices`,
  );
}

/** Creates or updates a model pricing rate row for the workspace. */
export function setModelPrice(
  workspaceId: string,
  body: ModelPriceRequest,
): Promise<{ saved: boolean }> {
  return requestJson<{ saved: boolean }>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/model-prices`,
    { method: 'PUT', body },
  );
}

/** Lists all members of a workspace. */
export function listMembers(
  workspaceId: string,
): Promise<WorkspaceMemberListResponse> {
  return requestJson<WorkspaceMemberListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/members`,
  );
}

/** Updates a member's workspace role (owner/admin restricted). */
export function updateMemberRole(
  workspaceId: string,
  userId: string,
  body: UpdateMemberRoleRequest,
): Promise<WorkspaceMember> {
  return requestJson<WorkspaceMember>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(userId)}`,
    {
      method: 'PATCH',
      body,
    },
  );
}

/** Removes a member from a workspace. */
export function removeMember(
  workspaceId: string,
  userId: string,
): Promise<void> {
  return requestJson<void>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/members/${encodeURIComponent(userId)}`,
    {
      method: 'DELETE',
    },
  );
}

/** Lists pending invitations in a workspace. */
export function listInvitations(
  workspaceId: string,
): Promise<InvitationListResponse> {
  return requestJson<InvitationListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/invitations`,
  );
}

/** Creates a workspace invitation token and shareable URL. */
export function createInvitation(
  workspaceId: string,
  body: CreateInvitationRequest,
): Promise<CreatedInvitationResponse> {
  return requestJson<CreatedInvitationResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/invitations`,
    {
      method: 'POST',
      body,
    },
  );
}

/** Revokes a pending invitation in a workspace. */
export function revokeInvitation(
  workspaceId: string,
  invitationId: string,
): Promise<void> {
  return requestJson<void>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/invitations/${encodeURIComponent(invitationId)}`,
    {
      method: 'DELETE',
    },
  );
}

/** Retrieves public metadata about an invitation token before acceptance. */
export function getInvitationPreview(
  token: string,
): Promise<InvitationPreviewResponse> {
  return requestJson<InvitationPreviewResponse>(
    `/api/invitations/${encodeURIComponent(token)}`,
    {
      includeAuth: false,
      retryOnUnauthorized: false,
    },
  );
}

/** Accepts an invitation either as an authenticated user or by creating a new account. */
export async function acceptInvitation(
  token: string,
  body?: AcceptInvitationRequest,
): Promise<AuthSessionResponse> {
  const session = await requestJson<AuthSessionResponse>(
    `/api/invitations/${encodeURIComponent(token)}/accept`,
    {
      method: 'POST',
      body: body ?? {},
      retryOnUnauthorized: false,
    },
  );
  accessToken = session.accessToken;
  return session;
}

/** Lists bots configured in a workspace. */
export function listBots(workspaceId: string): Promise<BotListResponse> {
  return requestJson<BotListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots`,
  );
}

/** Creates a new bot in a workspace. */
export function createBot(
  workspaceId: string,
  body: CreateBotRequest,
): Promise<Bot> {
  return requestJson<Bot>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots`,
    {
      method: 'POST',
      body,
    },
  );
}

/** Fetches a single bot in a workspace. */
export function getBot(workspaceId: string, botId: string): Promise<Bot> {
  return requestJson<Bot>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}`,
  );
}

/** Updates a bot configuration in a workspace. */
export function updateBot(
  workspaceId: string,
  botId: string,
  body: UpdateBotRequest,
): Promise<Bot> {
  return requestJson<Bot>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}`,
    {
      method: 'PATCH',
      body,
    },
  );
}

/** Deletes a bot from a workspace. */
export function deleteBot(workspaceId: string, botId: string): Promise<void> {
  return requestJson<void>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}`,
    {
      method: 'DELETE',
    },
  );
}

/** Fetches the sandbox environment state, resource metrics, and control lock for a bot. */
export function getBotEnvironment(
  workspaceId: string,
  botId: string,
): Promise<EnvironmentStateResponse> {
  return requestJson<EnvironmentStateResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/environment`,
  );
}

/** Starts (and creates on demand) the sandbox environment for a bot. */
export function startBotEnvironment(
  workspaceId: string,
  botId: string,
): Promise<EnvironmentStateResponse> {
  return requestJson<EnvironmentStateResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/environment/start`,
    {
      method: 'POST',
    },
  );
}

/** Stops the running sandbox environment while preserving /home/bot volume state. */
export function stopBotEnvironment(
  workspaceId: string,
  botId: string,
): Promise<EnvironmentStateResponse> {
  return requestJson<EnvironmentStateResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/environment/stop`,
    {
      method: 'POST',
    },
  );
}

/** Restarts the sandbox environment for a bot. */
export function restartBotEnvironment(
  workspaceId: string,
  botId: string,
): Promise<EnvironmentStateResponse> {
  return requestJson<EnvironmentStateResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/environment/restart`,
    {
      method: 'POST',
    },
  );
}

/** Lists recent desktop actions. Screenshot bytes are fetched separately. */
export function listDesktopActions(
  workspaceId: string,
  botId: string,
): Promise<DesktopActionListResponse> {
  return requestJson<DesktopActionListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/desktop-actions`,
  );
}

/** Loads one screenshot through the authenticated API client. */
export function fetchDesktopActionScreenshot(
  workspaceId: string,
  botId: string,
  actionId: string,
): Promise<Blob> {
  return requestBlob(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/desktop-actions/${encodeURIComponent(actionId)}/screenshot`,
  );
}

/** Claims exclusive desktop control lock for the signed-in user. */
export function takeBotControl(
  workspaceId: string,
  botId: string,
): Promise<EnvironmentStateResponse> {
  return requestJson<EnvironmentStateResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/control/take`,
    {
      method: 'POST',
    },
  );
}

/** Releases desktop control lock and optionally records a hand-back note. */
export function releaseBotControl(
  workspaceId: string,
  botId: string,
  body?: ReleaseControlRequest,
): Promise<EnvironmentStateResponse> {
  return requestJson<EnvironmentStateResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/control/release`,
    {
      method: 'POST',
      body: body ?? {},
    },
  );
}

/** Requests a short-lived signed WebSocket ticket to stream the bot desktop over VNC. */
export function createBotVncTicket(
  workspaceId: string,
  botId: string,
): Promise<StreamTicketResponse> {
  return requestJson<StreamTicketResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/vnc-ticket`,
    {
      method: 'POST',
    },
  );
}

/** Executes a shell command inside the running bot sandbox terminal. */
export function execBotTerminal(
  workspaceId: string,
  botId: string,
  body: TerminalExecRequest,
): Promise<TerminalExecResponse> {
  return requestJson<TerminalExecResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/terminal/exec`,
    {
      method: 'POST',
      body,
    },
  );
}

/** Lists files and directories inside the bot /home/bot filesystem. */
export function listBotFiles(
  workspaceId: string,
  botId: string,
  path?: string,
): Promise<FileListResponse> {
  const query = path ? `?path=${encodeURIComponent(path)}` : '';
  return requestJson<FileListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/files${query}`,
  );
}

/** Reads the UTF-8 contents of a file inside the bot /home/bot filesystem. */
export function readBotFile(
  workspaceId: string,
  botId: string,
  path: string,
): Promise<FileContentResponse> {
  return requestJson<FileContentResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/files/content?path=${encodeURIComponent(path)}`,
  );
}

/** Writes UTF-8 contents to a file inside the bot /home/bot filesystem. */
export function writeBotFile(
  workspaceId: string,
  botId: string,
  body: WriteFileContentRequest,
): Promise<FileContentResponse> {
  return requestJson<FileContentResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/files/content`,
    {
      method: 'PUT',
      body,
    },
  );
}

/** Lists workspace-level secret metadata without plaintext values. */
export function listWorkspaceSecrets(
  workspaceId: string,
): Promise<SecretListResponse> {
  return requestJson<SecretListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/secrets`,
  );
}

/** Stores a new workspace-wide write-only secret in envelope ciphertext. */
export function createWorkspaceSecret(
  workspaceId: string,
  body: CreateSecretRequest,
): Promise<SecretMetadata> {
  return requestJson<SecretMetadata>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/secrets`,
    {
      method: 'POST',
      body,
    },
  );
}

/** Updates a workspace secret's metadata and optionally rotates its write-only value. */
export function updateWorkspaceSecret(
  workspaceId: string,
  secretId: string,
  body: UpdateSecretRequest,
): Promise<SecretMetadata> {
  return requestJson<SecretMetadata>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/secrets/${encodeURIComponent(secretId)}`,
    {
      method: 'PUT',
      body,
    },
  );
}

/** Deletes a workspace-wide secret. */
export function deleteWorkspaceSecret(
  workspaceId: string,
  secretId: string,
): Promise<void> {
  return requestJson<void>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/secrets/${encodeURIComponent(secretId)}`,
    {
      method: 'DELETE',
    },
  );
}

/** Lists bot-scoped and inherited workspace secret metadata without plaintext values. */
export function listBotSecrets(
  workspaceId: string,
  botId: string,
): Promise<SecretListResponse> {
  return requestJson<SecretListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/secrets`,
  );
}

/** Stores a new bot-scoped write-only secret. */
export function createBotSecret(
  workspaceId: string,
  botId: string,
  body: CreateSecretRequest,
): Promise<SecretMetadata> {
  return requestJson<SecretMetadata>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/secrets`,
    {
      method: 'POST',
      body,
    },
  );
}

/** Updates a bot-scoped secret's metadata and optionally replaces its write-only value. */
export function updateBotSecret(
  workspaceId: string,
  botId: string,
  secretId: string,
  body: UpdateSecretRequest,
): Promise<SecretMetadata> {
  return requestJson<SecretMetadata>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/secrets/${encodeURIComponent(secretId)}`,
    {
      method: 'PUT',
      body,
    },
  );
}

/** Deletes a bot-scoped secret. */
export function deleteBotSecret(
  workspaceId: string,
  botId: string,
  secretId: string,
): Promise<void> {
  return requestJson<void>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/secrets/${encodeURIComponent(secretId)}`,
    {
      method: 'DELETE',
    },
  );
}

/** Fetches the bot's outbound egress policy, allowlist hosts, and secret-bound hosts. */
export function getBotEgressPolicy(
  workspaceId: string,
  botId: string,
): Promise<EgressPolicyResponse> {
  return requestJson<EgressPolicyResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/egress`,
  );
}

/** Updates the bot's outbound egress policy and allowed host patterns. */
export function updateBotEgressPolicy(
  workspaceId: string,
  botId: string,
  body: UpdateEgressPolicyRequest,
): Promise<EgressPolicyResponse> {
  return requestJson<EgressPolicyResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/egress`,
    {
      method: 'PATCH',
      body,
    },
  );
}

/** Fetches non-sensitive egress proxy audit decisions with optional action filtering. */
export function listBotEgressLogs(
  workspaceId: string,
  botId: string,
  options?: { action?: EgressLogAction; limit?: number; offset?: number },
): Promise<EgressLogListResponse> {
  const params = new URLSearchParams();
  if (options?.action) {
    params.set('action', options.action);
  }
  if (options?.limit !== undefined) {
    params.set('limit', String(options.limit));
  }
  if (options?.offset !== undefined) {
    params.set('offset', String(options.offset));
  }
  const qs = params.toString();
  const suffix = qs ? `?${qs}` : '';
  return requestJson<EgressLogListResponse>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/bots/${encodeURIComponent(botId)}/egress/logs${suffix}`,
  );
}

/** Fetches the workspace LLM proxy guardrail policy and rule counts. */
export function getWorkspaceGuardrails(
  workspaceId: string,
): Promise<components['schemas']['WorkspaceGuardrailsResponse']> {
  return requestJson<components['schemas']['WorkspaceGuardrailsResponse']>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/guardrails`,
  );
}
