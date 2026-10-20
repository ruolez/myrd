import { createContext, useCallback, useContext, useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./icons";

export function Spinner({ size = 20, label = "Loading" }: { size?: number; label?: string }) {
  return (
    <svg
      className="animate-spin text-primary"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      role="status"
      aria-label={label}
    >
      <circle
        cx="12"
        cy="12"
        r="9"
        fill="none"
        stroke="currentColor"
        strokeOpacity="0.2"
        strokeWidth="3"
      />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        fill="none"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-3 p-10 text-muted">
      <Spinner />
      <span>{label}</span>
    </div>
  );
}

type Tone = "neutral" | "primary" | "success" | "error" | "warning" | "relay";

const toneClass: Record<Tone, string> = {
  neutral: "bg-surface-2 text-muted",
  primary: "bg-primary-soft text-primary",
  success: "bg-success-soft text-success",
  error: "bg-error-soft text-error",
  warning: "bg-warning-soft text-warning",
  relay: "bg-surface-2 text-relay",
};

export function Badge({
  tone = "neutral",
  children,
  dot,
}: {
  tone?: Tone;
  children: ReactNode;
  dot?: boolean;
}) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${toneClass[tone]}`}
    >
      {dot && <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}

export function ErrorBox({ error, onRetry }: { error: string; onRetry?: () => void }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-3 rounded-xl bg-error-soft p-4 text-sm text-error"
    >
      <Icon name="warning" className="mt-0.5 shrink-0" />
      <div className="flex-1">{error}</div>
      {onRetry && (
        <button className="btn-text btn-sm" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  );
}

export function EmptyState({
  title,
  children,
  action,
}: {
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-2 px-6 py-12 text-center">
      <div className="text-base font-medium">{title}</div>
      {children && <div className="max-w-md text-sm text-muted">{children}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-normal tracking-tight">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors disabled:opacity-50 ${
        checked ? "bg-primary" : "bg-outline"
      }`}
    >
      <span
        className={`inline-block h-5 w-5 rounded-full bg-white shadow transition-transform ${
          checked ? "translate-x-5.5" : "translate-x-0.5"
        }`}
      />
    </button>
  );
}

export function Modal({
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const id = useId();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>("input,button,select,textarea")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      prev?.focus();
    };
  }, [onClose]);
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onMouseDown={onClose}
    >
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-labelledby={id}
        className={`card max-h-[90vh] w-full overflow-auto p-6 shadow-xl ${wide ? "max-w-2xl" : "max-w-md"}`}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <h2 id={id} className="text-lg font-medium">
            {title}
          </h2>
          <button className="btn-text btn-sm -mr-2" aria-label="Close" onClick={onClose}>
            <Icon name="close" size={18} />
          </button>
        </div>
        <div className="text-sm">{children}</div>
        {footer && <div className="mt-6 flex flex-wrap justify-end gap-2">{footer}</div>}
      </div>
    </div>
  );
}

type ConfirmOpts = { title: string; message: ReactNode; confirm?: string; danger?: boolean };
type ConfirmFn = (o: ConfirmOpts) => Promise<boolean>;
type ToastFn = (message: string, tone?: "success" | "error" | "neutral") => void;

const ConfirmCtx = createContext<ConfirmFn>(() => Promise.resolve(false));
const ToastCtx = createContext<ToastFn>(() => undefined);

export const useConfirm = () => useContext(ConfirmCtx);
export const useToast = () => useContext(ToastCtx);

export function FeedbackProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(
    null,
  );
  const [toasts, setToasts] = useState<{ id: number; message: string; tone: string }[]>([]);
  const nextId = useRef(1);

  const confirm = useCallback<ConfirmFn>(
    (o) => new Promise((resolve) => setPending({ ...o, resolve })),
    [],
  );
  const toast = useCallback<ToastFn>((message, tone = "neutral") => {
    const id = nextId.current++;
    setToasts((t) => [...t, { id, message, tone }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4000);
  }, []);

  const close = (v: boolean) => {
    pending?.resolve(v);
    setPending(null);
  };

  return (
    <ConfirmCtx.Provider value={confirm}>
      <ToastCtx.Provider value={toast}>
        {children}
        {pending && (
          <Modal
            title={pending.title}
            onClose={() => close(false)}
            footer={
              <>
                <button className="btn-text" onClick={() => close(false)}>
                  Cancel
                </button>
                <button
                  className={pending.danger ? "btn-danger" : "btn-primary"}
                  onClick={() => close(true)}
                >
                  {pending.confirm ?? "Confirm"}
                </button>
              </>
            }
          >
            <div className="text-muted">{pending.message}</div>
          </Modal>
        )}
        <div
          className="pointer-events-none fixed bottom-4 left-1/2 z-[60] flex -translate-x-1/2 flex-col items-center gap-2"
          aria-live="polite"
        >
          {toasts.map((t) => (
            <div
              key={t.id}
              className={`pointer-events-auto rounded-lg px-4 py-3 text-sm shadow-lg ${
                t.tone === "error"
                  ? "bg-error text-white"
                  : t.tone === "success"
                    ? "bg-success text-white"
                    : "bg-text text-bg"
              }`}
            >
              {t.message}
            </div>
          ))}
        </div>
      </ToastCtx.Provider>
    </ConfirmCtx.Provider>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="btn-tonal btn-sm"
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        });
      }}
    >
      <Icon name={done ? "check" : "copy"} size={16} />
      {done ? "Copied" : label}
    </button>
  );
}

export function Pager({
  hasPrev,
  hasNext,
  onPrev,
  onNext,
}: {
  hasPrev: boolean;
  hasNext: boolean;
  onPrev: () => void;
  onNext: () => void;
}) {
  if (!hasPrev && !hasNext) return null;
  return (
    <div className="flex justify-end gap-2 p-3">
      <button className="btn-outline btn-sm" disabled={!hasPrev} onClick={onPrev}>
        Newer
      </button>
      <button className="btn-outline btn-sm" disabled={!hasNext} onClick={onNext}>
        Older
      </button>
    </div>
  );
}

export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <label className="block">
      <span className="label">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-xs text-muted">{hint}</span>}
    </label>
  );
}

export function Brand({ size = 28, className = "text-lg" }: { size?: number; className?: string }) {
  return (
    <span className={`inline-flex items-center gap-2.5 ${className}`}>
      <img src="/favicon.svg" alt="" width={size} height={size} className="shrink-0" />
      <span className="leading-none tracking-tight whitespace-nowrap">
        <span className="font-normal text-muted">my </span>
        <span className="font-semibold">Remote</span>
        <span className="brand-accent font-semibold">Desktop</span>
      </span>
    </span>
  );
}
