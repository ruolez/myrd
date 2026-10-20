import { useState } from "react";
import type { FormEvent } from "react";
import { Link, Navigate, useNavigate } from "react-router";
import { api, errorMessage } from "../api/client";
import { useAuth } from "../app/auth";
import { ErrorBox, Field, Loading, Spinner } from "../components/ui";
import { AuthCard } from "./Login";

export const MIN_PASSWORD = 10;

export function Signup() {
  const { user, loading, config, setUser } = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (loading) return <Loading />;
  if (user) return <Navigate to="/devices" replace />;
  if (config && !config.signup_allowed) {
    return (
      <AuthCard
        title="Sign-up is closed"
        subtitle="Ask an administrator to create an account for you."
      >
        <Link className="btn-outline w-full" to="/login">
          Back to sign in
        </Link>
      </AuthCard>
    );
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) return setError("Passwords do not match.");
    if (password.length < MIN_PASSWORD) return setError(`Use at least ${MIN_PASSWORD} characters.`);
    setBusy(true);
    setError(null);
    try {
      const r = await api.auth.signup(email.trim(), password);
      setUser(r.user);
      navigate("/devices", { replace: true });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard title="Create account">
      <form className="space-y-4" onSubmit={(e) => void submit(e)}>
        {error && <ErrorBox error={error} />}
        <Field label="Email">
          <input
            className="input"
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
        <Field
          label="Password"
          hint={`At least ${MIN_PASSWORD} characters. You can add a passkey after signing in.`}
        >
          <input
            className="input"
            type="password"
            autoComplete="new-password"
            required
            minLength={MIN_PASSWORD}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field label="Confirm password">
          <input
            className="input"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </Field>
        <button className="btn-primary w-full" disabled={busy}>
          {busy && <Spinner size={16} />}Create account
        </button>
        <p className="text-center text-sm text-muted">
          Have an account?{" "}
          <Link className="text-primary hover:underline" to="/login">
            Sign in
          </Link>
        </p>
      </form>
    </AuthCard>
  );
}
