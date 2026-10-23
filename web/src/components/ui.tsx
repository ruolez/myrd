import { createContext, useCallback, useContext, useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./icons";
import type { IconName } from "./icons";

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
  title: ReactNode;
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
  // Read before children mount, so an autoFocus field does not count as "before".
  const [prev] = useState(() => document.activeElement as HTMLElement | null);
  const closeRef = useRef(onClose);
  useEffect(() => {
    closeRef.current = onClose;
  });
  // Once per open: a parent passing a new onClose each render must not move
  // focus while someone types.
  useEffect(() => {
    const root = ref.current;
    if (!root?.contains(document.activeElement))
      root?.querySelector<HTMLElement>("input,button,select,textarea")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeRef.current();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      prev?.focus();
    };
  }, [prev]);
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
type PromptOpts = {
  title: string;
  label: string;
  initial?: string;
  confirm?: string;
  maxLength?: number;
  hint?: ReactNode;
};
/** Resolves to the trimmed text, or null when cancelled. */
type PromptFn = (o: PromptOpts) => Promise<string | null>;

const ConfirmCtx = createContext<ConfirmFn>(() => Promise.resolve(false));
const ToastCtx = createContext<ToastFn>(() => undefined);
const PromptCtx = createContext<PromptFn>(() => Promise.resolve(null));

export const useConfirm = () => useContext(ConfirmCtx);
export const useToast = () => useContext(ToastCtx);
export const usePrompt = () => useContext(PromptCtx);

function PromptModal({ opts, onDone }: { opts: PromptOpts; onDone: (v: string | null) => void }) {
  const [value, setValue] = useState(opts.initial ?? "");
  const cancel = useCallback(() => onDone(null), [onDone]);
  return (
    <Modal
      title={opts.title}
      onClose={cancel}
      footer={
        <>
          <button className="btn-text" onClick={cancel}>
            Cancel
          </button>
          <button className="btn-primary" type="submit" form="prompt-form" disabled={!value.trim()}>
            {opts.confirm ?? "Save"}
          </button>
        </>
      }
    >
      <form
        id="prompt-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (value.trim()) onDone(value.trim());
        }}
      >
        <Field label={opts.label} hint={opts.hint}>
          <input
            className="input"
            value={value}
            maxLength={opts.maxLength}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
          />
        </Field>
      </form>
    </Modal>
  );
}

export function FeedbackProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(
    null,
  );
  const [toasts, setToasts] = useState<{ id: number; message: string; tone: string }[]>([]);
  const [asking, setAsking] = useState<
    (PromptOpts & { resolve: (v: string | null) => void }) | null
  >(null);
  const nextId = useRef(1);

  const prompt = useCallback<PromptFn>(
    (o) => new Promise((resolve) => setAsking({ ...o, resolve })),
    [],
  );
  const answer = useCallback(
    (v: string | null) =>
      setAsking((a) => {
        a?.resolve(v);
        return null;
      }),
    [],
  );

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
        <PromptCtx.Provider value={prompt}>
          {children}
          {asking && <PromptModal opts={asking} onDone={answer} />}
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
        </PromptCtx.Provider>
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

export function SegmentedControl<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { value: T; label: string; icon?: IconName }[];
  onChange: (v: T) => void;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex rounded-full border border-outline p-0.5"
      onKeyDown={(e) => {
        const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
        if (!step) return;
        e.preventDefault();
        const i = options.findIndex((o) => o.value === value);
        const next = options[(i + step + options.length) % options.length];
        if (next) {
          onChange(next.value);
          (
            e.currentTarget.querySelector(`[data-v="${next.value}"]`) as HTMLElement | null
          )?.focus();
        }
      }}
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          data-v={o.value}
          aria-checked={o.value === value}
          aria-label={o.icon ? o.label : undefined}
          title={o.label}
          tabIndex={o.value === value ? 0 : -1}
          onClick={() => onChange(o.value)}
          className={`inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs font-medium transition-colors ${
            o.value === value ? "bg-primary-soft text-primary" : "text-muted hover:text-text"
          }`}
        >
          {o.icon ? <Icon name={o.icon} size={16} /> : o.label}
        </button>
      ))}
    </div>
  );
}

/** Tag colors are palette slots 0..11 (index.css --c-tag-1..12). */
export const TAG_COLORS = 12;
export const tagStyle = (color: number) => {
  const n = (((Math.trunc(color) % TAG_COLORS) + TAG_COLORS) % TAG_COLORS) + 1;
  return { color: `var(--c-tag-${n})`, backgroundColor: `var(--c-tag-${n}-soft)` };
};

export function TagChip({
  name,
  color,
  onRemove,
}: {
  name: string;
  color: number;
  onRemove?: () => void;
}) {
  return (
    <span
      className="inline-flex max-w-full items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium"
      style={tagStyle(color)}
    >
      <span className="truncate">{name}</span>
      {onRemove && (
        <button
          type="button"
          className="-mr-1 rounded-full hover:brightness-90"
          aria-label={`Remove tag ${name}`}
          onClick={onRemove}
        >
          <Icon name="close" size={14} />
        </button>
      )}
    </span>
  );
}

export function TagDot({ color }: { color: number }) {
  return (
    <span
      aria-hidden="true"
      className="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
      style={{ backgroundColor: tagStyle(color).color }}
    />
  );
}

const meterTone = { ok: "bg-primary", warning: "bg-warning", error: "bg-error" } as const;

/** A labelled bar for a used share (memory, disk). */
export function Meter({
  label,
  value,
  tone = "ok",
  detail,
  compact,
}: {
  label: string;
  /** 0..1 */
  value: number;
  tone?: "ok" | "warning" | "error";
  detail?: string;
  compact?: boolean;
}) {
  const pctValue = Math.round(Math.max(0, Math.min(1, value)) * 100);
  return (
    <div className="min-w-0" title={detail ? `${label}: ${detail}` : undefined}>
      {!compact && (
        <div className="mb-1 flex justify-between gap-2 text-xs">
          <span className="truncate">{label}</span>
          {detail && <span className="shrink-0 text-muted">{detail}</span>}
        </div>
      )}
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pctValue}
        aria-valuetext={detail}
        className={`overflow-hidden rounded-full bg-surface-2 ${compact ? "h-1" : "h-1.5"}`}
      >
        <div
          className={`h-full rounded-full ${meterTone[tone]}`}
          style={{ width: `${pctValue}%` }}
        />
      </div>
    </div>
  );
}
