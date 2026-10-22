import { useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { api, errorMessage } from "../api/client";
import { useAuth } from "../app/auth";
import { Brand, ErrorBox, Field, Loading, Spinner } from "../components/ui";

export function AuthCard({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
}) {
  return (
    <div className="auth-backdrop flex min-h-full flex-col items-center justify-center gap-6 p-4">
      <Brand size={40} className="text-2xl" />
      <div className="card w-full max-w-sm p-8 shadow-lg shadow-black/5">
        <div className="mb-6 flex flex-col items-center gap-2 text-center">
          <h1 className="text-2xl font-normal">{title}</h1>
          {subtitle && <p className="text-sm text-muted">{subtitle}</p>}
        </div>
        {children}
      </div>
    </div>
  );
}

function safeNext(next: string | null) {
  return next && next.startsWith("/") && !next.startsWith("//") ? next : "/devices";
}

export function Login() {
  const { user, loading, config, setUser } = useAuth();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [ticket, setTicket] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const next = safeNext(params.get("next"));

  if (loading) return <Loading />;
  if (user) return <Navigate to={next} replace />;

  const done = (u: Parameters<typeof setUser>[0]) => {
    setUser(u);
    navigate(next, { replace: true });
  };

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const onPassword = (e: FormEvent) => {
    e.preventDefault();
    void run(async () => {
      const r = await api.auth.login(email.trim(), password);
      if ("totp_required" in r) {
        setTicket(r.ticket);
        setPassword("");
      } else done(r.user);
    });
  };

  const onTotp = (e: FormEvent) => {
    e.preventDefault();
    if (!ticket) return;
    void run(async () => {
      const r = await api.auth.loginTotp(ticket, code.replace(/\s/g, ""));
      done(r.user);
    });
  };

  if (ticket) {
    return (
      <AuthCard
        title="Two-step verification"
        subtitle="Enter the 6-digit code from your authenticator app."
      >
        <form className="space-y-4" onSubmit={onTotp}>
          {error && <ErrorBox error={error} />}
          <Field label="Authentication code">
            <input
              className="input text-center font-mono text-lg tracking-[0.4em]"
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="[0-9 ]{6,8}"
              maxLength={8}
              required
              autoFocus
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          </Field>
          <button className="btn-primary w-full" disabled={busy}>
            {busy && <Spinner size={16} />}Verify
          </button>
          <button
            type="button"
            className="btn-text w-full"
            onClick={() => {
              setTicket(null);
              setCode("");
              setError(null);
            }}
          >
            Back
          </button>
        </form>
      </AuthCard>
    );
  }

  return (
    <AuthCard title="Sign in" subtitle="Control your computers from anywhere.">
      <div className="space-y-4">
        {error && <ErrorBox error={error} />}
        <form className="space-y-4" onSubmit={onPassword}>
          <Field label="Email or username">
            <input
              className="input"
              type="text"
              autoCapitalize="none"
              spellCheck={false}
              autoComplete="username"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>
          <Field label="Password">
            <input
              className="input"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <button className="btn-primary w-full" disabled={busy}>
            {busy && <Spinner size={16} />}Sign in
          </button>
        </form>
        {config?.signup_allowed && (
          <p className="text-center text-sm text-muted">
            No account?{" "}
            <Link className="text-primary hover:underline" to="/signup">
              Create one
            </Link>
          </p>
        )}
      </div>
    </AuthCard>
  );
}
