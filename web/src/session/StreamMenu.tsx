// Stream settings: a toolbar button plus an in-stage popover (the toolbar is
// outside the fullscreen element, so the popover always renders in the stage).

import { useEffect, useRef } from "react";
import { Icon } from "../components/icons";
import { Toggle } from "../components/ui";
import {
  describeAuto,
  editStreamField,
  fpsChoices,
  fullColorAvailable,
  KBPS_STEP,
  MAX_KBPS,
  MIN_KBPS,
  NAMED_PRESETS,
  presetName,
  RELAY_CAP_KBPS,
  relayCapped,
  resolutionLabel,
  selectPreset,
  streamLabel,
} from "../lib/stream";
import type { ResolutionId, StreamPrefs, StreamState } from "../lib/stream";
import { groupModes, modeLabel, sameSize } from "../lib/display";
import type { DisplayChoice, DisplayInfo, DisplayMode } from "../lib/display";
import type { Monitor } from "../api/types";

type Props = {
  prefs: StreamPrefs;
  state: StreamState | null;
  /** null until hello; false = agent without the `stream` feature (presets only). */
  feature: boolean | null;
  /** The agent advertises `wallpaper`: it can hide the device's wallpaper. */
  canHideWallpaper: boolean;
  maxFps: number | null;
  monitor: Monitor | undefined;
  onChange: (prefs: StreamPrefs) => void;
  /** Switches the device's screen resolution (feature `display`). */
  onDisplay: (choice: DisplayChoice | "original") => void;
  displayBusy: boolean;
  displayError: string | null;
};

const DISPLAY_ERRORS: Record<string, string> = {
  unknown_mode: "The device doesn't offer that resolution.",
  unsupported: "This device can't change its resolution.",
  timeout: "The device didn't answer in time.",
  failed: "The device couldn't switch to that resolution.",
};

const sizeKey = (m: { width: number; height: number }) => `${m.width}x${m.height}`;

function ScreenResolution({
  display,
  busy,
  error,
  onDisplay,
}: {
  display: DisplayInfo;
  busy: boolean;
  error: string | null;
  onDisplay: Props["onDisplay"];
}) {
  const { same, other } = groupModes(display.modes, display.current);
  const listed = display.modes.some((m) => sameSize(m, display.current));
  const option = (m: DisplayMode) => (
    <option key={sizeKey(m)} value={sizeKey(m)} className="text-black">
      {modeLabel(m)}
    </option>
  );
  const pick = (v: string) => {
    if (v === "original" || v === "fit") return onDisplay(v === "fit" ? { kind: "fit" } : v);
    const [width = 0, height = 0] = v.split("x").map(Number);
    onDisplay({ kind: "mode", width, height });
  };
  const fixed = display.modes.length < 2 && !display.original;
  const hint =
    error && error !== "ended"
      ? (DISPLAY_ERRORS[error] ?? DISPLAY_ERRORS.failed)
      : fixed
        ? "This device offers no other screen mode. A computer without a monitor attached usually has only one; a dummy HDMI or VGA plug gives it the full range."
        : display.original
          ? `Changed for this session; back to ${modeLabel(display.original)} when you disconnect.`
          : "Changes the device's own screen; it goes back when you disconnect.";
  return (
    <label className="block">
      <span className="mb-1 flex items-center text-xs text-white/60">
        Screen resolution
        {busy && <span className="ml-auto text-[#8ab4f8]">Switching…</span>}
      </span>
      <select
        className="w-full rounded-lg bg-white/10 px-2 py-1.5 text-xs disabled:opacity-60"
        aria-label="Screen resolution"
        value={sizeKey(display.current)}
        onChange={(e) => pick(e.target.value)}
        disabled={busy || fixed}
      >
        {display.original && (
          <option value="original" className="text-black">
            Original · {modeLabel(display.original)}
          </option>
        )}
        {!fixed && (
          <option value="fit" className="text-black">
            Best fit for this window
          </option>
        )}
        {!listed && option(display.current)}
        <optgroup label="Same shape as now" className="text-black">
          {same.map(option)}
        </optgroup>
        {other.length > 0 && (
          <optgroup label="Other shapes" className="text-black">
            {other.map(option)}
          </optgroup>
        )}
      </select>
      <p
        className={`mt-1 text-xs ${error && error !== "ended" ? "text-[#fdd663]" : "text-white/50"}`}
      >
        {hint}
      </p>
    </label>
  );
}

export function StreamButton({
  prefs,
  state,
  open,
  disabled,
  onClick,
}: {
  prefs: StreamPrefs;
  state: StreamState | null;
  open: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  const label = streamLabel(prefs, state);
  return (
    <button
      className={`btn btn-sm text-white/90 hover:bg-white/10 ${open ? "bg-white/15" : ""}`}
      aria-label={`Stream settings: ${label}`}
      aria-haspopup="dialog"
      aria-expanded={open}
      title="Stream settings (Ctrl+Alt+Shift+S)"
      onClick={onClick}
      disabled={disabled}
    >
      <Icon name="speed" size={18} />
      <span className="hidden sm:inline">{label}</span>
    </button>
  );
}

const mbps = (kbps: number) => `${(kbps / 1000).toFixed(kbps % 1000 ? 1 : 0)} Mbps`;

const clampKbps = (v: number) =>
  Math.min(MAX_KBPS, Math.max(MIN_KBPS, Math.round(v / KBPS_STEP) * KBPS_STEP));

export function StreamMenu({
  prefs,
  state,
  feature,
  canHideWallpaper,
  maxFps,
  monitor,
  onChange,
  onDisplay,
  displayBusy,
  displayError,
  onClose,
  className = "",
}: Props & { onClose: () => void; className?: string }) {
  const root = useRef<HTMLDivElement>(null);
  // Taking focus blurs the remote keyboard, which releases held keys on the device.
  useEffect(() => {
    root.current?.focus({ preventScroll: true });
  }, []);

  const full = feature === true;
  const edit = <K extends "fps" | "res" | "max_kbps">(k: K, v: StreamPrefs[K]) =>
    onChange(editStreamField(prefs, state, k, v));

  const fps = prefs.fps ?? state?.resolved.fps ?? null;
  const res = prefs.res ?? state?.resolved.res ?? "native";
  const followsPreset = prefs.preset !== "auto" && prefs.preset !== "custom";
  const capKbps = followsPreset ? (state?.resolved.max_kbps ?? null) : prefs.max_kbps;
  const manual = capKbps !== null;
  const native = state ? { width: state.native_width, height: state.native_height } : null;
  const autoText = describeAuto(state);
  const canFullColor = fullColorAvailable(state);
  const colorHint = !canFullColor
    ? "Full color (4:4:4) needs an updated agent and Chrome or Edge."
    : prefs.color === "full"
      ? "4:4:4: sharp colored text. Software encoded, up to 30 fps."
      : "4:2:0 hardware video. Full color sharpens colored text.";

  return (
    <div
      ref={root}
      role="dialog"
      aria-label="Stream settings"
      tabIndex={-1}
      className={`absolute z-30 w-[min(22rem,calc(100%-1.5rem))] space-y-4 rounded-2xl border border-white/10 bg-[#1e1f20] p-4 text-sm text-white shadow-xl outline-none ${className}`}
    >
      <div className="flex items-center gap-2">
        <h2 className="font-medium">Stream</h2>
        {prefs.preset === "custom" && (
          <span className="rounded-full bg-[#8ab4f8]/20 px-2 py-0.5 text-xs text-[#8ab4f8]">
            Custom
          </span>
        )}
        <button
          className="btn btn-sm ml-auto px-2 text-white/80 hover:bg-white/10"
          aria-label="Close stream settings"
          onClick={onClose}
        >
          <Icon name="close" size={18} />
        </button>
      </div>

      <div
        role="radiogroup"
        aria-label="Preset"
        className="grid grid-cols-4 gap-1 rounded-full bg-white/5 p-1"
      >
        {NAMED_PRESETS.map((p) => (
          <button
            key={p}
            role="radio"
            aria-checked={prefs.preset === p}
            className={`rounded-full px-2 py-1 text-xs font-medium transition-colors ${prefs.preset === p ? "bg-[#8ab4f8] text-[#0b1d36]" : "text-white/80 hover:bg-white/10"}`}
            onClick={() => onChange(selectPreset(prefs, p, state))}
          >
            {presetName(p)}
          </button>
        ))}
      </div>

      {state?.display && (
        <ScreenResolution
          display={state.display}
          busy={displayBusy}
          error={displayError}
          onDisplay={onDisplay}
        />
      )}

      {feature === false && (
        <p className="text-xs text-white/60">
          This device's agent only supports presets. Update the agent for frame rate, resolution and
          bitrate controls.
        </p>
      )}

      {full && (
        <>
          <fieldset>
            <legend className="mb-1 text-xs text-white/60">
              Frame rate{prefs.preset === "auto" ? " (ceiling)" : ""}
            </legend>
            <div className="flex flex-wrap gap-1">
              {fpsChoices(maxFps).map((c) => (
                <button
                  key={c.fps}
                  aria-pressed={fps === c.fps}
                  disabled={c.disabled}
                  title={
                    c.disabled ? `This device's encoder is limited to ${maxFps} fps` : undefined
                  }
                  className={`rounded-full border px-3 py-0.5 text-xs disabled:cursor-not-allowed disabled:opacity-40 ${fps === c.fps ? "border-[#8ab4f8] bg-[#8ab4f8]/20 text-[#8ab4f8]" : "border-white/15 text-white/80 hover:bg-white/10"}`}
                  onClick={() => edit("fps", c.fps)}
                >
                  {c.fps}
                </button>
              ))}
            </div>
          </fieldset>

          <label className="block">
            <span className="mb-1 block text-xs text-white/60">
              Video size{prefs.preset === "auto" ? " (ceiling)" : ""}
            </span>
            <select
              className="w-full rounded-lg bg-white/10 px-2 py-1.5 text-xs"
              value={res}
              onChange={(e) => edit("res", e.target.value as ResolutionId)}
              disabled={!state}
            >
              {(state?.options.res ?? []).map((o) => (
                <option key={o.id} value={o.id} className="text-black">
                  {resolutionLabel(o, monitor?.scale ?? 1, native)}
                </option>
              ))}
            </select>
          </label>

          <fieldset>
            <legend className="mb-1 flex w-full items-center text-xs text-white/60">
              Bitrate
              <span className="ml-auto flex gap-1">
                {(["Auto", "Manual"] as const).map((mode) => {
                  const on = (mode === "Manual") === manual;
                  return (
                    <button
                      key={mode}
                      aria-pressed={on}
                      className={`rounded-full px-2 py-0.5 ${on ? "bg-white/15 text-white" : "text-white/60 hover:bg-white/10"}`}
                      onClick={() =>
                        edit(
                          "max_kbps",
                          mode === "Auto" ? null : clampKbps(capKbps ?? state?.ceil_kbps ?? 8000),
                        )
                      }
                    >
                      {mode}
                    </button>
                  );
                })}
              </span>
            </legend>
            {manual ? (
              <div className="flex items-center gap-3">
                <input
                  type="range"
                  className="flex-1 accent-[#8ab4f8]"
                  aria-label="Bitrate cap in Mbps"
                  min={MIN_KBPS}
                  max={MAX_KBPS}
                  step={KBPS_STEP}
                  value={capKbps ?? MIN_KBPS}
                  onChange={(e) => edit("max_kbps", Number(e.target.value))}
                />
                <span className="w-16 text-right text-xs tabular-nums">{mbps(capKbps ?? 0)}</span>
              </div>
            ) : (
              <p className="text-xs text-white/50">Follows the measured bandwidth.</p>
            )}
            {relayCapped(state) && (
              <p className="mt-1 text-xs text-[#c58af9]">
                This session is relayed, so the bitrate is capped at{" "}
                {mbps(state?.ceil_kbps ?? RELAY_CAP_KBPS)}.
              </p>
            )}
          </fieldset>
        </>
      )}

      <div className="flex items-center gap-3">
        <div className="flex-1">
          <div className="text-xs">Display</div>
          <p className="text-xs text-white/50">
            {prefs.scale === "fit"
              ? "Sized to this window, pixel for pixel."
              : "The device's full size, 1:1; scroll to see all of it."}
          </p>
        </div>
        <div
          role="radiogroup"
          aria-label="Display size"
          className="flex gap-1 rounded-full bg-white/5 p-1"
        >
          {(["fit", "actual"] as const).map((scale) => (
            <button
              key={scale}
              role="radio"
              aria-checked={prefs.scale === scale}
              className={`rounded-full px-2 py-0.5 text-xs font-medium ${prefs.scale === scale ? "bg-[#8ab4f8] text-[#0b1d36]" : "text-white/80 hover:bg-white/10"}`}
              onClick={() => onChange({ ...prefs, scale })}
            >
              {scale === "fit" ? "Fit" : "1:1"}
            </button>
          ))}
        </div>
      </div>

      {full && (
        <div className="flex items-center gap-3">
          <div className="flex-1">
            <div className="text-xs">Color</div>
            <p className="text-xs text-white/50">{colorHint}</p>
          </div>
          <div
            role="radiogroup"
            aria-label="Color"
            className="flex gap-1 rounded-full bg-white/5 p-1"
          >
            {(["standard", "full"] as const).map((color) => (
              <button
                key={color}
                role="radio"
                aria-checked={prefs.color === color}
                disabled={color === "full" && !canFullColor && prefs.color !== "full"}
                className={`rounded-full px-2 py-0.5 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-40 ${prefs.color === color ? "bg-[#8ab4f8] text-[#0b1d36]" : "text-white/80 hover:bg-white/10"}`}
                onClick={() => onChange({ ...prefs, color })}
              >
                {color === "standard" ? "Standard" : "Full"}
              </button>
            ))}
          </div>
        </div>
      )}

      {canHideWallpaper && (
        <div className="flex items-center gap-3">
          <div className="flex-1">
            <div className="text-xs">Show wallpaper</div>
            <p className="text-xs text-white/50">
              Off: a plain background, faster and sharper to stream.
            </p>
          </div>
          <Toggle
            label="Show wallpaper"
            checked={prefs.wallpaper}
            onChange={(wallpaper) => onChange({ ...prefs, wallpaper })}
          />
        </div>
      )}

      <div className="space-y-0.5 border-t border-white/10 pt-3 text-xs text-white/60">
        {state ? (
          <div>
            Now {state.width}×{state.height} · {state.target_fps} fps
            {state.resolved.color === "full" ? " · 4:4:4" : ""}
            {state.ceil_kbps ? ` · up to ${mbps(state.ceil_kbps)}` : ""}
            {state.relay ? " · relay" : ""}
            {state.background ? " · background" : ""}
          </div>
        ) : (
          <div>
            {feature === null ? "Waiting for the device…" : "Preset applied on the device."}
          </div>
        )}
        {autoText && <div>Auto: {autoText}</div>}
        {state && !state.ok && (
          <div className="text-[#fdd663]">The device rejected the last change.</div>
        )}
      </div>
    </div>
  );
}
