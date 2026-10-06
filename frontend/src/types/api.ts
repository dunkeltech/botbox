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
