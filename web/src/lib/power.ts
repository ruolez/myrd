import { compareVersions } from "./version";

// Remote restart (control `power`): which devices offer it, which actions a
// session's hello lists, and what a refusal means.

export type PowerAction = "restart" | "restart_agent";

export const POWER_AGENT = "0.5.4";

/** Devices whose agent restarts on request (macOS and Windows, agent 0.5.4 and later). */
export const canRestart = (d: { os: string; agent_version: string }) =>
  (d.os === "darwin" || d.os === "windows") && compareVersions(d.agent_version, POWER_AGENT) >= 0;

/** The actions a hello offers (feature `power`, field `power`). */
export function powerActions(features: string[] | null, power: unknown): PowerAction[] {
  if (!features?.includes("power") || !Array.isArray(power)) return [];
  return power.filter((a): a is PowerAction => a === "restart" || a === "restart_agent");
}

const failures: Record<string, string> = {
  unsupported: `This device does not offer that restart. Its agent needs ${POWER_AGENT} or later.`,
  failed: "The device could not schedule the restart. Its agent log has the reason.",
  timeout: "The device did not answer.",
  ended: "The session ended before the device answered.",
};

/** Text for a refused or failed power request. */
export const powerFailure = (code: string | null) =>
  (code && failures[code]) ?? "The device could not restart.";
