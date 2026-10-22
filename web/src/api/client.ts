import type {
  AdminOverview,
  AgentDownload,
  AdminSettings,
  AppConfig,
  AuditEvent,
  BulkPatch,
  ClientKey,
  Device,
  DevicePatch,
  DownloadOS,
  Folder,
  FolderId,
  LoginSession,
  Page,
  Release,
  ReleaseId,
  ReleaseOS,
  Session,
  Tag,
  TagId,
  User,
} from "./types";

import type { SealedPreview } from "../lib/previewCrypto";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

type Method = "GET" | "POST" | "PATCH" | "PUT" | "DELETE";

export async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ApiError(0, "network", "Cannot reach the server.");
  }
  if (res.status === 204) return undefined as T;
  return parseResponse<T>(res.status, await res.text());
}

function parseResponse<T>(status: number, text: string): T {
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (status < 200 || status >= 300) {
    const err = (data as { error?: { code?: string; message?: string } } | undefined)?.error;
    throw new ApiError(
      status,
      err?.code ?? "http_" + status,
      err?.message ?? `Request failed (${status})`,
    );
  }
  return data as T;
}

/** XHR because fetch has no upload progress. */
function sendWithProgress<T>(
  method: Method,
  path: string,
  body: Blob | FormData,
  onProgress?: (fraction: number) => void,
): Promise<T> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open(method, path);
    xhr.withCredentials = true;
    if (body instanceof Blob) xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total);
    xhr.onload = () => {
      try {
        resolve(parseResponse<T>(xhr.status, xhr.responseText));
      } catch (err) {
        reject(err);
      }
    };
    xhr.onerror = () => reject(new ApiError(0, "network", "Cannot reach the server."));
    xhr.send(body);
  });
}

/** PUTs a file as the raw body. */
export function uploadFile<T>(
  path: string,
  file: Blob,
  onProgress?: (fraction: number) => void,
): Promise<T> {
  return sendWithProgress<T>("PUT", path, file, onProgress);
}

/** Uploads an installer with its .sig sidecar (PROTOCOL §2: the server reads `sig` before `file`). */
export function uploadRelease(
  parts: { sig: File; file: File; rolloutPct: number },
  onProgress?: (fraction: number) => void,
): Promise<{ release: Release }> {
  const form = new FormData();
  form.append("sig", parts.sig, parts.sig.name);
  form.append("rollout_pct", String(parts.rolloutPct));
  form.append("file", parts.file, parts.file.name);
  return sendWithProgress("POST", "/v1/admin/releases", form, onProgress);
}

const qs = (params: Record<string, string | number | null | undefined>) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params))
    if (v !== undefined && v !== null && v !== "") s.set(k, String(v));
  const out = s.toString();
  return out ? `?${out}` : "";
};

export type PageQuery = { limit?: number; before?: string | null };

/** Fills fields that servers without the newer features leave out. */
export function normalizeDevice(d: Device): Device {
  return {
    ...d,
    folder_id: d.folder_id ?? null,
    tag_ids: d.tag_ids ?? [],
    favorite: d.favorite ?? false,
    notes: d.notes ?? "",
    last_connected_at: d.last_connected_at ?? null,
    info: d.info ?? null,
    info_at: d.info_at ?? null,
    features: d.features ?? [],
    previews: d.previews ?? true,
  };
}

const devicesOf = (r: { items: Device[] }) => ({ items: r.items.map(normalizeDevice) });
const deviceOf = (r: { device: Device }) => ({ device: normalizeDevice(r.device) });

type LoginResult = { user: User } | { totp_required: true; ticket: string };

export const api = {
  config: () => request<AppConfig>("GET", "/v1/config"),

  auth: {
    me: () => request<{ user: User }>("GET", "/v1/auth/me"),
    login: (email: string, password: string) =>
      request<LoginResult>("POST", "/v1/auth/login", { email, password }),
    loginTotp: (ticket: string, code: string) =>
      request<{ user: User }>("POST", "/v1/auth/login/totp", { ticket, code }),
    signup: (email: string, password: string) =>
      request<{ user: User }>("POST", "/v1/auth/signup", { email, password }),
    logout: () => request<object>("POST", "/v1/auth/logout", {}),
  },

  account: {
    changePassword: (current_password: string | null, new_password: string) =>
      request<object>("POST", "/v1/account/password", { current_password, new_password }),
    totpSetup: () =>
      request<{ secret: string; otpauth_url: string; qr_png: string }>(
        "POST",
        "/v1/account/totp/setup",
        {},
      ),
    totpEnable: (code: string) => request<object>("POST", "/v1/account/totp/enable", { code }),
    totpDisable: (code: string) => request<object>("POST", "/v1/account/totp/disable", { code }),
    sessions: () => request<{ items: LoginSession[] }>("GET", "/v1/account/sessions"),
    revokeSession: (id: string) => request<object>("DELETE", `/v1/account/sessions/${id}`),
  },

  clientKeys: {
    list: () => request<{ items: ClientKey[] }>("GET", "/v1/client-keys"),
    register: (public_key: string, label: string) =>
      request<{ client_key: ClientKey }>("POST", "/v1/client-keys", { public_key, label }),
    revoke: (id: string) => request<object>("DELETE", `/v1/client-keys/${id}`),
    setPreviewKey: (id: string, public_key: string, signature: string) =>
      request<{ client_key: ClientKey }>("PUT", `/v1/client-keys/${id}/preview-key`, {
        public_key,
        signature,
      }),
  },

  previews: (client_key_id: string, device_ids: string[]) =>
    request<{ items: SealedPreview[] }>(
      "GET",
      `/v1/previews${qs({ client_key_id, device_ids: device_ids.join(",") })}`,
    ),

  devices: {
    list: () => request<{ items: Device[] }>("GET", "/v1/devices").then(devicesOf),
    get: (id: string) => request<{ device: Device }>("GET", `/v1/devices/${id}`).then(deviceOf),
    update: (id: string, patch: DevicePatch) =>
      request<{ device: Device }>("PATCH", `/v1/devices/${id}`, patch).then(deviceOf),
    bulk: (device_ids: string[], patch: BulkPatch) =>
      request<{ items: Device[] }>("POST", "/v1/devices/bulk", { device_ids, ...patch }).then(
        devicesOf,
      ),
    remove: (id: string) => request<object>("DELETE", `/v1/devices/${id}`),
    requestUpdate: (id: string, force: boolean) =>
      request<{ version: string }>("POST", `/v1/devices/${id}/update`, { force }),
    enrollToken: () =>
      request<{ token: string; expires_at: string }>("POST", "/v1/enroll-tokens", {}),
  },

  folders: {
    list: () => request<{ items: Folder[] }>("GET", "/v1/folders"),
    create: (name: string, parent_id: FolderId | null) =>
      request<{ folder: Folder }>("POST", "/v1/folders", { name, parent_id }),
    update: (
      id: FolderId,
      patch: { name?: string; parent_id?: FolderId | null; position?: number },
    ) => request<{ folder: Folder }>("PATCH", `/v1/folders/${id}`, patch),
    remove: (id: FolderId) => request<object>("DELETE", `/v1/folders/${id}`),
  },

  tags: {
    list: () => request<{ items: Tag[] }>("GET", "/v1/tags"),
    create: (name: string, color: number) =>
      request<{ tag: Tag }>("POST", "/v1/tags", { name, color }),
    update: (id: TagId, patch: { name?: string; color?: number }) =>
      request<{ tag: Tag }>("PATCH", `/v1/tags/${id}`, patch),
    remove: (id: TagId) => request<object>("DELETE", `/v1/tags/${id}`),
  },

  sessions: (q: PageQuery & { device_id?: string }) =>
    request<Page<Session>>("GET", `/v1/sessions${qs(q)}`),
  audit: (q: PageQuery & { device_id?: string }) =>
    request<Page<AuditEvent>>("GET", `/v1/audit${qs(q)}`),

  admin: {
    overview: () => request<AdminOverview>("GET", "/v1/admin/overview"),
    users: (q: PageQuery & { q?: string }) => request<Page<User>>("GET", `/v1/admin/users${qs(q)}`),
    createUser: (email: string, password: string, is_admin: boolean) =>
      request<{ user: User }>("POST", "/v1/admin/users", { email, password, is_admin }),
    updateUser: (id: string, patch: { is_admin?: boolean; disabled?: boolean }) =>
      request<{ user: User }>("PATCH", `/v1/admin/users/${id}`, patch),
    setPassword: (id: string, password: string) =>
      request<object>("POST", `/v1/admin/users/${id}/password`, { password }),
    resetMfa: (id: string) => request<object>("POST", `/v1/admin/users/${id}/reset-mfa`, {}),
    logoutUser: (id: string) => request<object>("POST", `/v1/admin/users/${id}/logout`, {}),
    deleteUser: (id: string) => request<object>("DELETE", `/v1/admin/users/${id}`),
    devices: (q: PageQuery & { q?: string; status?: string }) =>
      request<Page<Device>>("GET", `/v1/admin/devices${qs(q)}`),
    requestDeviceUpdate: (id: string, force: boolean) =>
      request<{ version: string }>("POST", `/v1/admin/devices/${id}/update`, { force }),
    updateDevice: (id: string, disabled: boolean) =>
      request<{ device: Device }>("PATCH", `/v1/admin/devices/${id}`, { disabled }),
    deleteDevice: (id: string) => request<object>("DELETE", `/v1/admin/devices/${id}`),
    sessions: (q: PageQuery & { state?: string }) =>
      request<Page<Session>>("GET", `/v1/admin/sessions${qs(q)}`),
    endSession: (id: string) => request<object>("POST", `/v1/admin/sessions/${id}/end`, {}),
    uploadDownload: (os: DownloadOS, file: File, onProgress?: (fraction: number) => void) =>
      uploadFile<AgentDownload>(
        `/v1/admin/downloads/${os}${qs({ filename: file.name })}`,
        file,
        onProgress,
      ),
    deleteDownload: (os: DownloadOS) => request<void>("DELETE", `/v1/admin/downloads/${os}`),
    audit: (
      q: PageQuery & { user_id?: string; device_id?: string; event?: string; actor?: string },
    ) => request<Page<AuditEvent>>("GET", `/v1/admin/audit${qs(q)}`),
    releases: () => request<{ items: Release[] }>("GET", "/v1/admin/releases"),
    uploadRelease,
    updateRelease: (id: ReleaseId, patch: { rollout_pct?: number; published?: boolean }) =>
      request<{ release: Release }>("PATCH", `/v1/admin/releases/${id}`, patch),
    setReleaseInstaller: (id: ReleaseId) =>
      request<{ release: Release }>("POST", `/v1/admin/releases/${id}/installer`, {}),
    deleteRelease: (id: ReleaseId) => request<object>("DELETE", `/v1/admin/releases/${id}`),
    pushUpdates: (os?: ReleaseOS) =>
      request<{ queued: number }>("POST", "/v1/admin/updates/push", os ? { os } : {}),
    settings: () => request<AdminSettings>("GET", "/v1/admin/settings"),
    saveSettings: (s: AdminSettings) => request<AdminSettings>("PUT", "/v1/admin/settings", s),
  },
};

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
