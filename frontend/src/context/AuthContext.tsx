'use client';

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';

import {
  getSetupStatus,
  listWorkspaces,
  logout,
  refreshSession,
  setAccessToken,
  setSessionCallbacks,
} from '@/lib/api-client';
import type { AuthSessionResponse, User, WorkspaceSummary } from '@/types/api';

/**
 * Authentication and workspace context value exposed via useAuth().
 */
export interface AuthContextValue {
  user: User | null;
  workspaces: WorkspaceSummary[];
  activeWorkspace: WorkspaceSummary | null;
  setupRequired: boolean;
  isLoading: boolean;
  applySession: (session: AuthSessionResponse) => void;
  selectWorkspace: (workspaceId: string) => void;
  reloadWorkspaces: (preferredId?: string) => Promise<WorkspaceSummary[]>;
  signOut: () => Promise<void>;
  refreshSetupStatus: () => Promise<boolean>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

function resolveActiveWorkspace(
  workspaces: WorkspaceSummary[],
  preferredId?: string | null,
): WorkspaceSummary | null {
  if (preferredId) {
    const match = workspaces.find((ws) => ws.id === preferredId);
    if (match) {
      return match;
    }
  }
  return workspaces[0] ?? null;
}

/**
 * Provides authentication session, setup status, and active workspace selection across the app.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [workspaces, setWorkspaces] = useState<WorkspaceSummary[]>([]);
  const [activeWorkspace, setActiveWorkspace] =
    useState<WorkspaceSummary | null>(null);
  const [setupRequired, setSetupRequired] = useState(false);
  const [isLoading, setIsLoading] = useState(true);

  const activeWorkspaceIdRef = useRef<string | null>(null);
  useEffect(() => {
    activeWorkspaceIdRef.current = activeWorkspace?.id ?? null;
  }, [activeWorkspace]);

  const applySession = useCallback((session: AuthSessionResponse) => {
    setAccessToken(session.accessToken);
    setUser(session.user);
    setWorkspaces(session.workspaces);
    const nextWorkspace = resolveActiveWorkspace(
      session.workspaces,
      session.activeWorkspace?.id ?? activeWorkspaceIdRef.current,
    );
    setActiveWorkspace(nextWorkspace);
    activeWorkspaceIdRef.current = nextWorkspace?.id ?? null;
    setSetupRequired(false);
  }, []);

  const clearSession = useCallback(() => {
    setAccessToken(null);
    setUser(null);
    setWorkspaces([]);
    setActiveWorkspace(null);
    activeWorkspaceIdRef.current = null;
  }, []);

  useEffect(() => {
    setSessionCallbacks({
      onRefreshed: applySession,
      onCleared: clearSession,
    });
    return () => {
      setSessionCallbacks({});
    };
  }, [applySession, clearSession]);

  const bootstrap = useCallback(async () => {
    setIsLoading(true);
    try {
      let needsSetup = false;
      try {
        const status = await getSetupStatus();
        needsSetup = status.setupRequired;
      } catch {
        // Transient backend error: continue to session attempt instead of assuming setupRequired.
        needsSetup = false;
      }

      if (needsSetup) {
        clearSession();
        setSetupRequired(true);
        return;
      }

      setSetupRequired(false);
      try {
        const session = await refreshSession();
        applySession(session);
      } catch {
        clearSession();
      }
    } finally {
      setIsLoading(false);
    }
  }, [applySession, clearSession]);

  useEffect(() => {
    let active = true;
    void (async () => {
      await bootstrap();
      if (!active) {
        return;
      }
    })();
    return () => {
      active = false;
    };
  }, [bootstrap]);

  const selectWorkspace = useCallback(
    (workspaceId: string) => {
      const found = workspaces.find((ws) => ws.id === workspaceId);
      if (found) {
        setActiveWorkspace(found);
        activeWorkspaceIdRef.current = found.id;
      }
    },
    [workspaces],
  );

  const reloadWorkspaces = useCallback(
    async (preferredId?: string): Promise<WorkspaceSummary[]> => {
      const data = await listWorkspaces();
      setWorkspaces(data.workspaces);
      const nextWorkspace = resolveActiveWorkspace(
        data.workspaces,
        preferredId ?? activeWorkspaceIdRef.current,
      );
      setActiveWorkspace(nextWorkspace);
      activeWorkspaceIdRef.current = nextWorkspace?.id ?? null;
      return data.workspaces;
    },
    [],
  );

  const signOut = useCallback(async () => {
    try {
      await logout();
    } finally {
      clearSession();
    }
  }, [clearSession]);

  const refreshSetupStatus = useCallback(async (): Promise<boolean> => {
    try {
      const status = await getSetupStatus();
      setSetupRequired(status.setupRequired);
      return status.setupRequired;
    } catch {
      return false;
    }
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      user,
      workspaces,
      activeWorkspace,
      setupRequired,
      isLoading,
      applySession,
      selectWorkspace,
      reloadWorkspaces,
      signOut,
      refreshSetupStatus,
    }),
    [
      user,
      workspaces,
      activeWorkspace,
      setupRequired,
      isLoading,
      applySession,
      selectWorkspace,
      reloadWorkspaces,
      signOut,
      refreshSetupStatus,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/**
 * Returns the current authentication and workspace context. Must be used inside AuthProvider.
 */
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used inside AuthProvider');
  }
  return ctx;
}
