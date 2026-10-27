/**
 * TypeScript definitions matching backend/api/openapi.yaml for Phase 1 endpoints.
 */

export type WorkspaceRole = 'owner' | 'admin' | 'member' | 'viewer';
export type DriverType = 'docker' | 'kubernetes';
export type LlmMode = 'cli' | 'api';
export type BotStatus = 'idle' | 'thinking' | 'action' | 'stopped';
export type AvatarMood = 'idle' | 'thinking' | 'action' | 'offline';

export interface ErrorBody {
  code: string;
  message: string;
}

export interface ErrorEnvelope {
  error: ErrorBody;
}

export interface HealthResponse {
  status: string;
}

export interface SetupStatusResponse {
  setupRequired: boolean;
}

export interface SetupCompleteRequest {
  setupSecret: string;
  displayName: string;
  email: string;
  password: string;
  workspaceName: string;
  workspaceSlug: string;
  defaultDriver?: DriverType;
  defaultLocale?: string;
}

export interface LoginRequest {
  email: string;
  password: string;
}

export interface RefreshRequest {
  refreshToken?: string;
}

export interface User {
  id: string;
  email: string;
  displayName: string;
  locale: string;
  createdAt: string;
}

export interface WorkspaceSummary {
  id: string;
  name: string;
  slug: string;
  defaultDriver: DriverType;
  defaultLocale: string;
  role: WorkspaceRole;
  createdAt: string;
  screenshotRetentionDays?: number;
}

export interface UpdateWorkspaceRequest {
  screenshotRetentionDays: number;
}

export interface DesktopAction {
  id: string;
  botId: string;
  turnId?: string;
  sessionId?: string;
  seq?: number;
  kind: string;
  result: string;
  errorCode: string;
  hasScreenshot: boolean;
  screenshotMime?: string;
  coordX?: number;
  coordY?: number;
  hasCoord: boolean;
  createdAt: string;
}

export interface DesktopActionListResponse {
  actions: DesktopAction[];
  screenshotRetentionDays: number;
}

export interface WorkspaceListResponse {
  workspaces: WorkspaceSummary[];
}

export interface CreateWorkspaceRequest {
  name: string;
  slug: string;
  defaultDriver?: DriverType;
  defaultLocale?: string;
}

export interface AuthSessionResponse {
  user: User;
  workspaces: WorkspaceSummary[];
  activeWorkspace?: WorkspaceSummary;
  accessToken: string;
}

export interface MeResponse {
  user: User;
  workspaces: WorkspaceSummary[];
}

export interface WorkspaceMember {
  workspaceId: string;
  userId: string;
  email: string;
  displayName: string;
  locale: string;
  role: WorkspaceRole;
  joinedAt: string;
}

export interface WorkspaceMemberListResponse {
  members: WorkspaceMember[];
}

export interface UpdateMemberRoleRequest {
  role: WorkspaceRole;
}

export interface InvitationSummary {
  id: string;
  workspaceId: string;
  email?: string | null;
  role: WorkspaceRole;
  expiresAt: string;
  acceptedAt?: string | null;
  createdAt: string;
}

export interface InvitationListResponse {
  invitations: InvitationSummary[];
}

export interface CreateInvitationRequest {
  email?: string | null;
  role: WorkspaceRole;
}

export interface CreatedInvitationResponse {
  invitation: InvitationSummary;
  token: string;
  inviteUrl: string;
}

export interface InvitationPreviewResponse {
  id: string;
  workspaceId: string;
  workspaceName: string;
  workspaceSlug: string;
  email?: string | null;
  role: WorkspaceRole;
  expiresAt: string;
}

export interface AcceptInvitationRequest {
  email?: string;
  displayName?: string;
  password?: string;
}

export interface Bot {
  id: string;
  workspaceId: string;
  ownerUserId?: string | null;
  name: string;
  description: string;
  avatarSeed: string;
  llmMode: LlmMode;
  llmProvider: string;
  llmModel: string;
  driverType: DriverType;
  replyLanguage: string;
  status: BotStatus;
  mood: AvatarMood;
  createdAt: string;
  updatedAt: string;
}

export interface BotListResponse {
  bots: Bot[];
}

export interface CreateBotRequest {
  name: string;
  description?: string;
  avatarSeed: string;
  llmMode: LlmMode;
  llmProvider: string;
  llmModel: string;
  driverType?: DriverType;
  replyLanguage?: string;
}

export interface UpdateBotRequest {
  name?: string;
  description?: string;
  avatarSeed?: string;
  llmMode?: LlmMode;
  llmProvider?: string;
  llmModel?: string;
  driverType?: DriverType;
  replyLanguage?: string;
}

export type EnvironmentStatus = 'creating' | 'running' | 'stopped' | 'error';

export interface EnvironmentStats {
  cpuPercent: number;
  memoryUsedMiB: number;
  memoryLimitMiB: number;
  volumeUsedMiB: number;
  volumeLimitMiB: number;
}

export interface ControlHolder {
  userId: string;
  displayName: string;
  email: string;
}

export interface WatcherUser {
  userId: string;
  displayName: string;
}

export interface EnvironmentStateResponse {
  botId: string;
  workspaceId: string;
  driver: string;
  envId: string;
  status: EnvironmentStatus;
  image: string;
  cpuMillis: number;
  memoryMiB: number;
  volumeRef: string;
  controlHolder: ControlHolder | null;
  controlNote: string;
  stats: EnvironmentStats;
  watchers: WatcherUser[];
  startedAt: string | null;
  updatedAt: string;
}

export interface ReleaseControlRequest {
  note?: string;
}

export interface StreamTicketResponse {
  ticket: string;
  wsPath: string;
  canControl: boolean;
  expiresAt: string;
}

export interface TerminalExecRequest {
  command: string;
  workdir?: string;
}

export interface TerminalExecResponse {
  stdout: string;
  stderr: string;
  exitCode: number;
  cwd: string;
}

export interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  sizeBytes: number;
  updatedAt: string;
}

export interface FileListResponse {
  path: string;
  entries: FileEntry[];
}

export interface FileContentResponse {
  path: string;
  content: string;
  sizeBytes: number;
}

export interface WriteFileContentRequest {
  path: string;
  content: string;
}

export type SecretScope = 'bot' | 'workspace';

export type SecretAuthScheme = 'bearer' | 'header' | 'basic';

export interface SecretMetadata {
  id: string;
  workspaceId: string;
  botId: string | null;
  scope: SecretScope;
  name: string;
  domainMatch: string;
  matchSubdomains: boolean;
  pathPrefix: string;
  authScheme: SecretAuthScheme;
  headerName: string;
  username: string;
  maskedPreview: string;
  usedCount: number;
  lastUsedAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface SecretListResponse {
  secrets: SecretMetadata[];
}

export interface CreateSecretRequest {
  name: string;
  domainMatch: string;
  matchSubdomains?: boolean;
  pathPrefix?: string;
  authScheme: SecretAuthScheme;
  headerName?: string;
  username?: string;
  value: string;
}

export interface UpdateSecretRequest {
  name: string;
  domainMatch: string;
  matchSubdomains?: boolean;
  pathPrefix?: string;
  authScheme: SecretAuthScheme;
  headerName?: string;
  username?: string;
  value?: string;
}

export type EgressPolicyMode = 'allowlist' | 'open';

export type EgressEnforcementStatus = 'applied' | 'pending' | 'not_running';

export interface EgressPolicyResponse {
  botId: string;
  workspaceId: string;
  policy: EgressPolicyMode;
  allowedHosts: string[];
  blockedHosts: string[];
  effectiveSecretHosts: string[];
  revision: number;
  acknowledgedRevision: number;
  enforcementStatus: EgressEnforcementStatus;
  updatedAt: string;
}

export interface UpdateEgressPolicyRequest {
  policy: EgressPolicyMode;
  allowedHosts: string[];
  blockedHosts?: string[];
  expectedRevision: number;
}

export type EgressLogAction =
  'blocked_host' | 'tunnel_passthrough' | 'mitm_token_added';

export interface EgressLogEntry {
  id: string;
  botId: string;
  workspaceId: string;
  method: string;
  host: string;
  port: string;
  path: string;
  action: EgressLogAction;
  statusCode: number;
  secretId: string | null;
  secretScope: string;
  secretName: string;
  bytesTransferred: number;
  latencyMs: number;
  createdAt: string;
}

export interface EgressLogListResponse {
  logs: EgressLogEntry[];
}
