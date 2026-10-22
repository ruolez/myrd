import { useState } from "react";
import type { FormEvent } from "react";
import { api, errorMessage } from "../../api/client";
import type { User } from "../../api/types";
import { useUser } from "../../app/auth";
import { Icon } from "../../components/icons";
import {
  Badge,
  EmptyState,
  ErrorBox,
  Field,
  Loading,
  Modal,
  PageHeader,
  Pager,
  Toggle,
  useConfirm,
  useToast,
} from "../../components/ui";
import { timeAgo } from "../../lib/format";
import { usePaged } from "../../lib/hooks";
import { MIN_PASSWORD } from "../Signup";

export function AdminUsers() {
  const me = useUser();
  const toast = useToast();
  const confirm = useConfirm();
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const [pwFor, setPwFor] = useState<User | null>(null);
  const p = usePaged((before) => api.admin.users({ q: query, before, limit: 25 }), [query]);

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      toast(ok, "success");
      await p.reload(true);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const ask = (title: string, message: string, confirmLabel: string, danger = true) =>
    confirm({ title, message, confirm: confirmLabel, danger });

  return (
    <>
      <PageHeader
        title="Users"
        actions={
          <button className="btn-primary" onClick={() => setCreating(true)}>
            <Icon name="add" size={18} />
            New user
          </button>
        }
      />
      <section className="card">
        <form
          className="flex gap-2 border-b border-outline p-3"
          onSubmit={(e) => {
            e.preventDefault();
            setQuery(q.trim());
          }}
        >
          <input
            className="input"
            type="search"
            placeholder="Search by email"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            aria-label="Search users"
          />
          <button className="btn-tonal">
            <Icon name="search" size={18} />
            Search
          </button>
        </form>
        {p.error && (
          <div className="p-4">
            <ErrorBox error={p.error} onRetry={() => void p.reload()} />
          </div>
        )}
        {p.loading && p.items.length === 0 ? (
          <Loading />
        ) : p.items.length === 0 ? (
          <EmptyState title="No users found" />
        ) : (
          <div className="overflow-x-auto">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Email</th>
                  <th>Sign-in</th>
                  <th>Devices</th>
                  <th>Last login</th>
                  <th>Admin</th>
                  <th>Enabled</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {p.items.map((u) => {
                  const self = u.id === me.id;
                  return (
                    <tr key={u.id} className={u.disabled ? "opacity-60" : ""}>
                      <td>
                        {u.email} {self && <Badge tone="primary">you</Badge>}
                      </td>
                      <td className="space-x-1">
                        {u.has_password && <Badge>password</Badge>}
                        {u.totp_enabled && <Badge tone="success">TOTP</Badge>}
                      </td>
                      <td>{u.device_count ?? "—"}</td>
                      <td>{timeAgo(u.last_login_at)}</td>
                      <td>
                        <Toggle
                          label={`Admin: ${u.email}`}
                          checked={u.is_admin}
                          disabled={self}
                          onChange={async (v) => {
                            if (
                              await ask(
                                v ? `Make ${u.email} an admin?` : `Remove admin from ${u.email}?`,
                                v
                                  ? "Admins can manage every user, device and session."
                                  : "They will lose access to the admin console.",
                                v ? "Make admin" : "Remove admin",
                                !v,
                              )
                            )
                              await act(
                                () => api.admin.updateUser(u.id, { is_admin: v }),
                                "Updated",
                              );
                          }}
                        />
                      </td>
                      <td>
                        <Toggle
                          label={`Enabled: ${u.email}`}
                          checked={!u.disabled}
                          disabled={self}
                          onChange={async (v) => {
                            if (
                              v ||
                              (await ask(
                                `Disable ${u.email}?`,
                                "They are signed out everywhere and their live sessions end.",
                                "Disable",
                              ))
                            )
                              await act(
                                () => api.admin.updateUser(u.id, { disabled: !v }),
                                v ? "User enabled" : "User disabled",
                              );
                          }}
                        />
                      </td>
                      <td className="text-right whitespace-nowrap">
                        <button className="btn-text btn-sm" onClick={() => setPwFor(u)}>
                          Password
                        </button>
                        <button
                          className="btn-text btn-sm"
                          disabled={!u.totp_enabled}
                          onClick={async () => {
                            if (
                              await ask(
                                `Reset MFA for ${u.email}?`,
                                "Their authenticator app is removed; they sign in with their password only.",
                                "Reset MFA",
                              )
                            )
                              await act(() => api.admin.resetMfa(u.id), "MFA reset");
                          }}
                        >
                          Reset MFA
                        </button>
                        <button
                          className="btn-text btn-sm"
                          onClick={async () => {
                            if (
                              await ask(
                                `Sign ${u.email} out everywhere?`,
                                "All their login sessions are revoked.",
                                "Sign out",
                                false,
                              )
                            )
                              await act(() => api.admin.logoutUser(u.id), "Signed out");
                          }}
                        >
                          Sign out
                        </button>
                        <button
                          className="btn-text btn-sm text-error"
                          disabled={self}
                          onClick={async () => {
                            if (
                              await ask(
                                `Delete ${u.email}?`,
                                "Their devices are removed and they lose access. Audit history is kept.",
                                "Delete user",
                              )
                            )
                              await act(() => api.admin.deleteUser(u.id), "User deleted");
                          }}
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <Pager hasPrev={p.hasPrev} hasNext={p.hasNext} onPrev={p.prev} onNext={p.next} />
      </section>

      {creating && (
        <CreateUser
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            toast("User created", "success");
            void p.reload();
          }}
        />
      )}
      {pwFor && (
        <SetPassword
          user={pwFor}
          onClose={() => setPwFor(null)}
          onDone={() => {
            setPwFor(null);
            toast("Password set", "success");
          }}
        />
      )}
    </>
  );
}

function CreateUser({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password.length < MIN_PASSWORD) return setError(`Use at least ${MIN_PASSWORD} characters.`);
    setBusy(true);
    try {
      await api.admin.createUser(email.trim(), password, admin);
      onCreated();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title="New user" onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => void submit(e)}>
        {error && <ErrorBox error={error} />}
        <Field label="Email">
          <input
            className="input"
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
        <Field label="Initial password" hint="Share it securely.">
          <input
            className="input"
            type="text"
            autoComplete="off"
            required
            minLength={MIN_PASSWORD}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <label className="flex items-center justify-between gap-3 text-sm">
          Administrator
          <Toggle label="Administrator" checked={admin} onChange={setAdmin} />
        </label>
        <div className="flex justify-end gap-2">
          <button type="button" className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" disabled={busy}>
            Create
          </button>
        </div>
      </form>
    </Modal>
  );
}

function SetPassword({
  user,
  onClose,
  onDone,
}: {
  user: User;
  onClose: () => void;
  onDone: () => void;
}) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password.length < MIN_PASSWORD) return setError(`Use at least ${MIN_PASSWORD} characters.`);
    try {
      await api.admin.setPassword(user.id, password);
      onDone();
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  return (
    <Modal title={`Set password for ${user.email}`} onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => void submit(e)}>
        {error && <ErrorBox error={error} />}
        <Field label="New password">
          <input
            className="input"
            type="text"
            autoComplete="off"
            required
            minLength={MIN_PASSWORD}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <div className="flex justify-end gap-2">
          <button type="button" className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary">Set password</button>
        </div>
      </form>
    </Modal>
  );
}
