// Who keeps a device busy, and the take-over / end actions on that session.
// Shared by the busy screen of a session and the device page.

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage } from "../api/client";
import type { ClientKeyId } from "../api/types";
import { useAuth } from "../app/auth";
import { currentClientKeyId } from "../lib/clientKey";
import { describeHolder } from "../lib/takeover";
import type { Holder } from "../lib/takeover";

export type HolderState = {
  /** undefined while loading, null when the device is free now. */
  holder: Holder | null | undefined;
  ownKey: ClientKeyId | null;
  error: string | null;
  reload: () => void;
};

/** Loads the device's live session; again whenever `version` changes. */
export function useSessionHolder(deviceId: string, version: unknown = null): HolderState {
  const userId = useAuth().user?.id;
  const [holder, setHolder] = useState<Holder | null | undefined>(undefined);
  const [ownKey, setOwnKey] = useState<ClientKeyId | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    setError(null);
    void Promise.all([
      api.devices.session(deviceId),
      userId ? currentClientKeyId(userId) : Promise.resolve(null),
    ])
      .then(([r, key]) => {
        if (!live) return;
        setHolder(r.session);
        setOwnKey(key);
      })
      .catch((e: unknown) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
  }, [deviceId, userId, version, tick]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { holder, ownKey, error, reload };
}

/** Ends the device's session; resolves false (after `onError`) when the request failed. */
export async function endDeviceSession(
  deviceId: string,
  takeover: boolean,
  onError: (msg: string) => void,
): Promise<boolean> {
  try {
    await api.devices.endSession(deviceId, takeover);
    return true;
  } catch (e) {
    onError(errorMessage(e));
    return false;
  }
}

/** One line naming the browser that holds the device and what it does there. */
export function HolderSummary({
  state,
  className = "",
  mutedClass = "text-muted",
}: {
  state: HolderState;
  className?: string;
  mutedClass?: string;
}) {
  if (state.error) return <p className={`text-sm ${mutedClass} ${className}`}>{state.error}</p>;
  if (state.holder === undefined)
    return <p className={`text-sm ${mutedClass} ${className}`}>Looking up the session…</p>;
  if (state.holder === null)
    return <p className={`text-sm ${mutedClass} ${className}`}>That session has just ended.</p>;
  const { who, detail } = describeHolder(state.holder, state.ownKey);
  return (
    <div className={className}>
      <div className="text-sm font-medium">{who}</div>
      <div className={`text-sm ${mutedClass}`}>{detail}</div>
    </div>
  );
}
