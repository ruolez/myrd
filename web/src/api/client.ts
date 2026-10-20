import type {
  AdminOverview,
  AgentDownload,
  AdminSettings,
  AppConfig,
  AuditEvent,
  ClientKey,
  Device,
  DownloadOS,
  LoginSession,
  Page,
  Passkey,
  Release,
  Session,
  User,
} from "./types";

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

/** PUTs a file as the raw body; XHR because fetch has no upload progress. */
export function uploadFile<T>(
  path: string,
  file: Blob,
  onProgress?: (fraction: number) => void,
): Promise<T> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", path);
    xhr.withCredentials = true;
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total);
    xhr.onload = () => {
      try {
        resolve(parseResponse<T>(xhr.status, xhr.responseText));
      } catch (err) {
        reject(err);
      }
    };
    xhr.onerror = () => reject(new ApiError(0, "network", "Cannot reach the server."));
    xhr.send(file);
  });
}

const qs = (params: Record<string, string | number | null | undefined>) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params))
    if (v !== undefined && v !== null && v !== "") s.set(k, String(v));
  const out = s.toString();
  return out ? `?${out}` : "";
};

export type PageQuery = { limit?: number; before?: string | null };

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
    passkeyBegin: () =>
      request<{ ticket: string; publicKey: unknown }>("POST", "/v1/auth/passkey/begin", {}),
    passkeyFinish: (ticket: string, credential: unknown) =>
      request<{ user: User }>("POST", "/v1/auth/passkey/finish", { ticket, credential }),
    logout: () => request<object>("POST", "/v1/auth/logout", {}),
  },

  account: {
    changePassword: (current_password: string | null, new_password: string) =>
      request<object>("POST", "/v1/account/password", { current_password, new_password }),
    passkeys: () => request<{ items: Passkey[] }>("GET", "/v1/account/passkeys"),
    passkeyBegin: () =>
      request<{ ticket: string; publicKey: unknown }>("POST", "/v1/account/passkeys/begin", {}),
    passkeyFinish: (ticket: string, name: string, credential: unknown) =>
      request<{ passkey: Passkey }>("POST", "/v1/account/passkeys/finish", {
        ticket,
        name,
        credential,
      }),
    deletePasskey: (id: string) => request<object>("DELETE", `/v1/account/passkeys/${id}`),
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
  },

  devices: {
    list: () => request<{ items: Device[] }>("GET", "/v1/devices"),
    get: (id: string) => request<{ device: Device }>("GET", `/v1/devices/${id}`),
    update: (id: string, patch: { name?: string; auto_accept?: boolean }) =>
      request<{ device: Device }>("PATCH", `/v1/devices/${id}`, patch),
    remove: (id: string) => request<object>("DELETE", `/v1/devices/${id}`),
    enrollToken: () =>
      request<{ token: string; expires_at: string }>("POST", "/v1/enroll-tokens", {}),
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
    createRelease: (r: Omit<Release, "id" | "published_at" | "created_at">) =>
      request<{ release: Release }>("POST", "/v1/admin/releases", r),
    updateRelease: (id: string, patch: { rollout_pct?: number; published?: boolean }) =>
      request<{ release: Release }>("PATCH", `/v1/admin/releases/${id}`, patch),
    deleteRelease: (id: string) => request<object>("DELETE", `/v1/admin/releases/${id}`),
    settings: () => request<AdminSettings>("GET", "/v1/admin/settings"),
    saveSettings: (s: AdminSettings) => request<AdminSettings>("PUT", "/v1/admin/settings", s),
  },
};

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
