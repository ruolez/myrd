import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api, errorMessage } from "../api/client";
import { useAuth, useUser } from "../app/auth";
import { useTheme } from "../app/theme";
import type { ThemePref } from "../app/theme";
import { Icon } from "../components/icons";
import {
  Badge,
  EmptyState,
  ErrorBox,
  Field,
  Loading,
  Modal,
  PageHeader,
  useConfirm,
  useToast,
} from "../components/ui";
import { currentClientKeyId, forgetClientKeyCache } from "../lib/clientKey";
import { dateTime, timeAgo } from "../lib/format";
import { useAsync } from "../lib/hooks";
import { createCredential, passkeysSupported } from "../lib/webauthn";
import { MIN_PASSWORD } from "./Signup";

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <section className="card p-6">
      <h2 className="font-medium">{title}</h2>
      {description && <p className="mt-1 mb-4 text-sm text-muted">{description}</p>}
      {!description && <div className="mb-4" />}
      {children}
    </section>
  );
}

export function Settings() {
  return (
    <>
      <PageHeader title="Settings" subtitle="Sign-in methods, browser keys and appearance." />
      <div className="space-y-6">
        <PasskeysSection />
        <TotpSection />
        <PasswordSection />
        <ClientKeysSection />
        <LoginSessionsSection />
        <AppearanceSection />
      </div>
    </>
  );
}

function PasswordSection() {
  const user = useUser();
  const { refresh } = useAuth();
  const toast = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (next !== confirm) return setError("New passwords do not match.");
    if (next.length < MIN_PASSWORD) return setError(`Use at least ${MIN_PASSWORD} characters.`);
    setBusy(true);
    setError(null);
    try {
      await api.account.changePassword(user.has_password ? current : null, next);
      setCurrent("");
      setNext("");
      setConfirm("");
      toast("Password updated", "success");
      await refresh();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section
      title="Password"
      description={
        user.has_password
          ? "Fallback sign-in when no passkey is available."
          : "No password set — you sign in with passkeys only."
      }
    >
      <form className="grid max-w-xl gap-4 sm:grid-cols-2" onSubmit={(e) => void submit(e)}>
        {error && (
          <div className="sm:col-span-2">
            <ErrorBox error={error} />
          </div>
        )}
        {user.has_password && (
          <div className="sm:col-span-2">
            <Field label="Current password">
              <input
                className="input"
                type="password"
                autoComplete="current-password"
                required
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            </Field>
          </div>
        )}
        <Field label="New password">
          <input
            className="input"
            type="password"
            autoComplete="new-password"
            required
            minLength={MIN_PASSWORD}
            value={next}
            onChange={(e) => setNext(e.target.value)}
          />
        </Field>
        <Field label="Confirm new password">
          <input
            className="input"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </Field>
        <div>
          <button className="btn-primary" disabled={busy}>
            {user.has_password ? "Change password" : "Set password"}
          </button>
        </div>
      </form>
    </Section>
  );
}

function PasskeysSection() {
  const user = useUser();
  const { refresh } = useAuth();
  const toast = useToast();
  const confirm = useConfirm();
  const { data, error, loading, reload } = useAsync(() => api.account.passkeys(), []);
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  const register = async () => {
    setBusy(true);
    try {
      const { ticket, publicKey } = await api.account.passkeyBegin();
      const credential = await createCredential(publicKey);
      await api.account.passkeyFinish(ticket, name.trim() || "Passkey", credential);
      toast("Passkey added", "success");
      setNaming(false);
      setName("");
      await reload();
      await refresh();
    } catch (e) {
      const msg =
        e instanceof DOMException && e.name === "NotAllowedError"
          ? "Passkey creation was cancelled."
          : errorMessage(e);
      toast(msg, "error");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string, label: string) => {
    if (
      !(await confirm({
        title: `Remove passkey “${label}”?`,
        message: "You will no longer be able to sign in with it.",
        confirm: "Remove",
        danger: true,
      }))
    )
      return;
    try {
      await api.account.deletePasskey(id);
      await reload();
      await refresh();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  return (
    <Section
      title="Passkeys"
      description="Phishing-resistant sign-in with Touch ID, Windows Hello or a security key. Recommended."
    >
      {!passkeysSupported() && <ErrorBox error="This browser does not support passkeys." />}
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {loading && !data ? (
        <Loading />
      ) : data && data.items.length > 0 ? (
        <ul className="mb-4 divide-y divide-outline rounded-xl border border-outline">
          {data.items.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <Icon name="key" className="text-primary" />
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium">{p.name}</div>
                <div className="text-xs text-muted">
                  Added {dateTime(p.created_at)} · last used {timeAgo(p.last_used_at)}
                </div>
              </div>
              <button
                className="btn-text btn-sm text-error"
                onClick={() => void remove(p.id, p.name)}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="mb-4 text-sm text-muted">No passkeys yet.</p>
      )}
      <button
        className="btn-primary"
        disabled={!passkeysSupported() || busy}
        onClick={() => setNaming(true)}
      >
        <Icon name="add" size={18} />
        Add a passkey
      </button>
      {naming && (
        <Modal
          title="Add a passkey"
          onClose={() => setNaming(false)}
          footer={
            <>
              <button className="btn-text" onClick={() => setNaming(false)}>
                Cancel
              </button>
              <button className="btn-primary" disabled={busy} onClick={() => void register()}>
                Continue
              </button>
            </>
          }
        >
          <Field label="Name" hint={`Signed in as ${user.email}`}>
            <input
              className="input"
              placeholder="e.g. MacBook Touch ID"
              value={name}
              maxLength={64}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
        </Modal>
      )}
    </Section>
  );
}

function TotpSection() {
  const user = useUser();
  const { refresh } = useAuth();
  const toast = useToast();
  const [setup, setSetup] = useState<{
    secret: string;
    otpauth_url: string;
    qr_png: string;
  } | null>(null);
  const [disabling, setDisabling] = useState(false);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const start = async () => {
    setError(null);
    try {
      setSetup(await api.account.totpSetup());
      setCode("");
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const finish = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (disabling) await api.account.totpDisable(code.replace(/\s/g, ""));
      else await api.account.totpEnable(code.replace(/\s/g, ""));
      toast(
        disabling ? "Two-step verification turned off" : "Two-step verification is on",
        "success",
      );
      setSetup(null);
      setDisabling(false);
      await refresh();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const close = () => {
    setSetup(null);
    setDisabling(false);
    setError(null);
  };

  return (
    <Section
      title="Two-step verification"
      description="A 6-digit code from an authenticator app, asked after your password."
    >
      <div className="flex flex-wrap items-center gap-3">
        {user.totp_enabled ? <Badge tone="success">On</Badge> : <Badge>Off</Badge>}
        {user.totp_enabled ? (
          <button className="btn-outline" onClick={() => setDisabling(true)}>
            Turn off
          </button>
        ) : (
          <button
            className="btn-primary"
            onClick={() => void start()}
            disabled={!user.has_password}
          >
            Set up authenticator
          </button>
        )}
        {!user.has_password && !user.totp_enabled && (
          <span className="text-xs text-muted">Set a password first.</span>
        )}
      </div>
      {(setup || disabling) && (
        <Modal
          title={disabling ? "Turn off two-step verification" : "Set up authenticator"}
          onClose={close}
        >
          <form className="space-y-4" onSubmit={(e) => void finish(e)}>
            {setup && (
              <>
                <p className="text-muted">
                  Scan this QR code with your authenticator app, then enter the code it shows.
                </p>
                <div className="flex justify-center">
                  <img
                    src={setup.qr_png}
                    alt="TOTP QR code"
                    width={200}
                    height={200}
                    className="rounded-lg bg-white p-2"
                  />
                </div>
                <div className="rounded-lg bg-surface-2 p-3 text-center font-mono text-xs break-all">
                  {setup.secret}
                </div>
              </>
            )}
            {error && <ErrorBox error={error} />}
            <Field label="Code">
              <input
                className="input text-center font-mono tracking-[0.3em]"
                inputMode="numeric"
                autoComplete="one-time-code"
                required
                maxLength={8}
                value={code}
                autoFocus
                onChange={(e) => setCode(e.target.value)}
              />
            </Field>
            <div className="flex justify-end gap-2">
              <button type="button" className="btn-text" onClick={close}>
                Cancel
              </button>
              <button className={disabling ? "btn-danger" : "btn-primary"} disabled={busy}>
                {disabling ? "Turn off" : "Enable"}
              </button>
            </div>
          </form>
        </Modal>
      )}
    </Section>
  );
}

function ClientKeysSection() {
  const user = useUser();
  const toast = useToast();
  const confirm = useConfirm();
  const { data, error, loading, reload } = useAsync(() => api.clientKeys.list(), []);
  const [mine, setMine] = useState<string | null>(null);
  useEffect(() => {
    void currentClientKeyId(user.id).then(setMine);
  }, [user.id, data]);

  const revoke = async (id: string, label: string) => {
    const self = id === mine;
    if (
      !(await confirm({
        title: `Revoke “${label}”?`,
        message: self
          ? "This is the browser you're using. It will get a new key that each device must approve again."
          : "Every device drops this browser from its approved list on next connect. Use this for a lost or stolen laptop.",
        confirm: "Revoke",
        danger: true,
      }))
    )
      return;
    try {
      await api.clientKeys.revoke(id);
      if (self) forgetClientKeyCache(user.id);
      toast("Key revoked", "success");
      await reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const keys = data?.items ?? [];
  return (
    <Section
      title="Browser keys"
      description="Each browser profile holds a private signing key that never leaves it. Devices pin these keys after approving them on their own screen."
    >
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {loading && !data ? (
        <Loading />
      ) : keys.length === 0 ? (
        <EmptyState title="No browser keys yet" />
      ) : (
        <div className="overflow-x-auto">
          <table className="data-table">
            <thead>
              <tr>
                <th>Browser</th>
                <th>Created</th>
                <th>Last used</th>
                <th>Status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id} className={k.id === mine ? "bg-primary-soft/50" : ""}>
                  <td>
                    {k.label} {k.id === mine && <Badge tone="primary">this browser</Badge>}
                  </td>
                  <td>{dateTime(k.created_at)}</td>
                  <td>{timeAgo(k.last_used_at)}</td>
                  <td>
                    {k.revoked_at ? (
                      <Badge tone="error">revoked</Badge>
                    ) : (
                      <Badge tone="success">active</Badge>
                    )}
                  </td>
                  <td className="text-right">
                    {!k.revoked_at && (
                      <button
                        className="btn-text btn-sm text-error"
                        onClick={() => void revoke(k.id, k.label)}
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}

function LoginSessionsSection() {
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => api.account.sessions(), []);
  const revoke = async (id: string) => {
    try {
      await api.account.revokeSession(id);
      await reload();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <Section
      title="Where you're signed in"
      description="Sign out other browsers you no longer use."
    >
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {loading && !data ? (
        <Loading />
      ) : (
        <ul className="divide-y divide-outline rounded-xl border border-outline">
          {(data?.items ?? []).map((s) => (
            <li key={s.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
              <div className="min-w-0 flex-1">
                <div className="truncate">{s.user_agent ?? "Unknown browser"}</div>
                <div className="text-xs text-muted">
                  {s.ip ?? "unknown IP"} · active {timeAgo(s.last_seen_at ?? s.created_at)} ·
                  expires {dateTime(s.expires_at)}
                </div>
              </div>
              {s.current ? (
                <Badge tone="primary">this session</Badge>
              ) : (
                <button className="btn-text btn-sm text-error" onClick={() => void revoke(s.id)}>
                  Sign out
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function AppearanceSection() {
  const [pref, setPref] = useTheme();
  const opts: { v: ThemePref; label: string }[] = [
    { v: "system", label: "System" },
    { v: "light", label: "Light" },
    { v: "dark", label: "Dark" },
  ];
  return (
    <Section title="Appearance">
      <div
        className="inline-flex rounded-full border border-outline p-1"
        role="radiogroup"
        aria-label="Theme"
      >
        {opts.map((o) => (
          <button
            key={o.v}
            role="radio"
            aria-checked={pref === o.v}
            onClick={() => setPref(o.v)}
            className={`rounded-full px-4 py-1.5 text-sm ${pref === o.v ? "bg-primary text-on-primary" : "text-text hover:bg-surface-2"}`}
          >
            {o.label}
          </button>
        ))}
      </div>
    </Section>
  );
}
