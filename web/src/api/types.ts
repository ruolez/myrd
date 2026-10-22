// REST object shapes (docs/PROTOCOL.md §1).

export type Brand<T, B extends string> = T & { readonly __brand: B };
export type UserId = Brand<string, "UserId">;
export type DeviceId = Brand<string, "DeviceId">;
export type SessionId = Brand<string, "SessionId">;
export type ClientKeyId = Brand<string, "ClientKeyId">;
export type ReleaseId = Brand<string, "ReleaseId">;
export type FolderId = Brand<string, "FolderId">;
export type TagId = Brand<string, "TagId">;

export type User = {
  id: UserId;
  email: string;
  is_admin: boolean;
  disabled: boolean;
  totp_enabled: boolean;
  has_password: boolean;
  passkey_count: number;
  created_at: string;
  last_login_at: string | null;
  device_count?: number;
};

export type Monitor = {
  id: number;
  name: string;
  x: number;
  y: number;
  width: number;
  height: number;
  scale: number;
  primary: boolean;
};

export type DeviceStatusValue = "online" | "offline" | "disabled";
export type OS = "darwin" | "windows" | "linux";

export type UpdateState =
  | "checking"
  | "downloading"
  | "waiting_idle"
  | "installing"
  | "up_to_date"
  | "failed"
  | "rolled_back";

export type Device = {
  id: DeviceId;
  name: string;
  hostname: string;
  os: OS;
  arch: string;
  agent_version: string;
  public_key: string;
  status: DeviceStatusValue;
  online: boolean;
  busy: boolean;
  auto_accept: boolean;
  last_seen_at: string | null;
  created_at: string;
  monitors: Monitor[] | null;
  permissions: { screen: boolean; input: boolean } | null;
  encoder: string | null;
  pinned_clients: { client_key_id: ClientKeyId; label: string; approved_at: string }[] | null;
  /** Newest published release for its os/arch. */
  latest_version: string | null;
  update_available: boolean;
  /** Last update attempt as reported by the agent ("" = none). */
  update_state: UpdateState | "";
  update_from: string;
  update_version: string;
  update_error: string;
  update_at: string | null;
  owner_email?: string;
  /** The owner's organization (servers with the "organization" feature). */
  folder_id: FolderId | null;
  tag_ids: TagId[];
  favorite: boolean;
  notes: string;
  /** The owner's latest connected session. */
  last_connected_at: string | null;
  /** Latest device.info report (agents with the "device_info" feature). */
  info: HostInfo | null;
  info_at: string | null;
  /** Optional agent features, e.g. "device_info", "preview". */
  features: string[];
  /** The owner allows screen previews of this device. */
  previews: boolean;
};

export type DiskInfo = { name: string; mount: string; fs: string; total: number; free: number };

/** PROTOCOL §3 device.info. Null fields: the platform cannot tell. */
export type HostInfo = {
  os_name: string;
  os_version: string;
  os_build: string;
  /** Console user; "" when nobody is signed in. */
  user: string | null;
  idle_s: number | null;
  uptime_s: number;
  locked: boolean | null;
  cpu_pct: number | null;
  cpu_count: number;
  mem_total: number;
  mem_used: number;
  disks: DiskInfo[];
};

export type Folder = {
  id: FolderId;
  parent_id: FolderId | null;
  name: string;
  position: number;
  created_at: string;
};

/** Palette slot 0..11 (--c-tag-1..12). */
export type TagColor = number;

export type Tag = { id: TagId; name: string; color: TagColor; created_at: string };

export type DevicePatch = {
  name?: string;
  auto_accept?: boolean;
  previews?: boolean;
  folder_id?: FolderId | null;
  favorite?: boolean;
  notes?: string;
  tag_ids?: TagId[];
};

export type BulkPatch = {
  folder_id?: FolderId | null;
  favorite?: boolean;
  add_tag_ids?: TagId[];
  remove_tag_ids?: TagId[];
};

export type SessionState = "pending" | "active" | "ended" | "failed";
export type IcePath = "host" | "srflx" | "relay";

export type Session = {
  id: SessionId;
  device_id: DeviceId;
  device_name: string;
  user_id: UserId;
  user_email: string;
  client_key_id: ClientKeyId | null;
  client_key_label: string | null;
  state: SessionState;
  created_at: string;
  connected_at: string | null;
  ended_at: string | null;
  end_reason: string | null;
  ice_path: IcePath | null;
  bytes_sent: number;
  bytes_recv: number;
  client_ip: string | null;
  agent_ip: string | null;
  duration_s: number | null;
};

export type AuditEvent = {
  id: number;
  ts: string;
  actor: "user" | "agent" | "admin" | "system";
  event: string;
  user_id: UserId | null;
  user_email: string | null;
  device_id: DeviceId | null;
  device_name: string | null;
  session_id: SessionId | null;
  ip: string | null;
  meta: Record<string, unknown> | null;
};

export type ClientKey = {
  id: ClientKeyId;
  label: string;
  public_key: string;
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  /** ECDH P-256 SPKI the agent seals screen previews to; null before registration. */
  preview_key?: string | null;
};

export type LoginSession = {
  id: string;
  ip: string | null;
  user_agent: string | null;
  created_at: string;
  last_seen_at: string | null;
  expires_at: string;
  current: boolean;
};

export type ReleaseOS = Exclude<OS, "linux">;
/** "amd64-win7": the Windows 7 build of the windows/amd64 agent. */
export type ReleaseArch = "amd64" | "arm64" | "universal" | "amd64-win7";

export type Release = {
  id: ReleaseId;
  version: string;
  os: ReleaseOS;
  arch: ReleaseArch;
  url: string;
  filename: string;
  size: number;
  sha256: string;
  signature: string;
  key_id: string;
  rollout_pct: number;
  published_at: string | null;
  created_at: string;
  /** Also served as the Add-device download for its os. */
  installer: boolean;
  /** Devices currently reporting this version. */
  devices: number;
};

export type Page<T> = { items: T[]; next_before: string | null };

export type DownloadOS = "macos" | "windows" | "windows7";

export type AgentDownload = {
  os: DownloadOS;
  filename: string;
  size: number;
  sha256: string;
  uploaded_at: string;
};

/** An uploaded installer (file set) or the external URL from Admin → System. */
export type DownloadLink = { url: string; file: AgentDownload | null };

export type AppConfig = {
  product_name: string;
  version: string;
  signup_allowed: boolean;
  webauthn: { rp_id: string; origin: string };
  downloads: Record<DownloadOS, DownloadLink | null>;
  /** Optional server APIs, e.g. "organization" (absent on older servers). */
  features?: string[];
  /** The admin allows screen previews (servers with the "previews" feature). */
  screen_previews?: boolean;
};

export type AdminOverview = {
  server: {
    version: string;
    started_at: string;
    uptime_s: number;
    go_version: string;
    db_ok: boolean;
  };
  users: { total: number; admins: number; disabled: number };
  devices: {
    total: number;
    online: number;
    disabled: number;
    by_os: Partial<Record<OS, number>>;
  };
  sessions: {
    active: number;
    pending: number;
    today: number;
    relay_share_24h: number;
    avg_duration_s_24h: number;
    by_path_24h: Partial<Record<IcePath, number>>;
    failed_24h: number;
  };
  ws: { agents: number; browsers: number; reconnects_per_min: number };
  series: { hour: string; sessions: number; relay: number; failed: number }[];
  recent_audit: AuditEvent[];
};

export type AdminSettings = {
  signup_allowed: boolean;
  screen_previews?: boolean;
  default_auto_accept: boolean;
  max_session_hours: number;
  download_macos_url: string;
  download_windows_url: string;
};

export type ICEServer = { urls: string[]; username?: string; credential?: string };
