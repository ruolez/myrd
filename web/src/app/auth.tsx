import { createContext, useCallback, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, ApiError, errorMessage } from "../api/client";
import type { AppConfig, User } from "../api/types";
import { ensureClientKey, forgetClientKeyCache } from "../lib/clientKey";
import { forgetPreviewKeyCache } from "../lib/previewKey";
import { signaling } from "../lib/signaling";

type AuthState = {
  user: User | null;
  config: AppConfig | null;
  loading: boolean;
  bootError: string | null;
  keyError: string | null;
  setUser: (u: User | null) => void;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
};

const Ctx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUserState] = useState<User | null>(null);
  const [config, setConfig] = useState<AppConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [bootError, setBootError] = useState<string | null>(null);
  const [keyError, setKeyError] = useState<string | null>(null);

  const setUser = useCallback((u: User | null) => {
    setUserState(u);
    if (u) {
      signaling.start();
      ensureClientKey(u.id)
        .then(() => setKeyError(null))
        .catch((e) =>
          setKeyError(`Could not register this browser's signing key: ${errorMessage(e)}`),
        );
    } else {
      signaling.stop();
      forgetClientKeyCache();
      forgetPreviewKeyCache();
    }
  }, []);

  const refresh = useCallback(async () => {
    try {
      const { user: u } = await api.auth.me();
      setUserState(u);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) setUser(null);
    }
  }, [setUser]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const cfg = await api.config();
        if (cancelled) return;
        setConfig(cfg);
      } catch (e) {
        if (!cancelled) setBootError(errorMessage(e));
      }
      try {
        const { user: u } = await api.auth.me();
        if (!cancelled) setUser(u);
      } catch (e) {
        if (!cancelled && !(e instanceof ApiError && e.status === 401))
          setBootError(errorMessage(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [setUser]);

  const logout = useCallback(async () => {
    await api.auth.logout().catch(() => undefined);
    setUser(null);
  }, [setUser]);

  return (
    <Ctx.Provider value={{ user, config, loading, bootError, keyError, setUser, refresh, logout }}>
      {children}
    </Ctx.Provider>
  );
}

export function useAuth(): AuthState {
  const v = useContext(Ctx);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}

export function useUser(): User {
  const { user } = useAuth();
  if (!user) throw new Error("useUser without a signed-in user");
  return user;
}
