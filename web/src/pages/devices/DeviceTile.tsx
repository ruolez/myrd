import type { DragEvent, KeyboardEvent, MouseEvent, ReactNode } from "react";
import type { Device, Tag } from "../../api/types";
import { DeviceUpdateBadge, UpdateButton } from "../../components/devices";
import { Icon } from "../../components/icons";
import { Menu } from "../../components/menu";
import { PreviewImage } from "../../components/PreviewImage";
import { Meter, TagChip } from "../../components/ui";
import { api } from "../../api/client";
import { attentionReasons, attentionText } from "../../lib/deviceFilters";
import { bytes, osName, timeAgo } from "../../lib/format";
import {
  diskLabel,
  diskSeverity,
  freeFraction,
  infoStale,
  lowestFreeDisk,
  memSeverity,
  tileHealthLine,
} from "../../lib/health";
import type { Severity } from "../../lib/health";
import { canConnect, connectTitle, useDeviceActions } from "./actions";

export const DEVICE_DRAG = "application/x-remotectl-devices";

export type TileProps = {
  device: Device;
  tags: Map<string, Tag>;
  now: number;
  size: "s" | "m" | "l";
  selected: boolean;
  selecting: boolean;
  organization: boolean;
  onSelect: (e: MouseEvent | KeyboardEvent) => void;
  onDragStart: (e: DragEvent) => void;
  onKick: () => void;
  /** Replaces the OS artwork (live thumbnail or screen preview). */
  media?: ReactNode;
  /** Overrides the status line and primary action (e.g. open in this tab). */
  status?: { label: string; tone: StatusTone };
  primary?: { label: string; onClick: () => void; enabled: boolean; title: string };
};

export type StatusTone = "online" | "busy" | "offline" | "disabled" | "here";

const dotClass: Record<StatusTone, string> = {
  online: "bg-success",
  busy: "bg-warning",
  offline: "bg-outline",
  disabled: "bg-error",
  here: "bg-primary",
};

export function deviceStatus(d: Device): { label: string; tone: StatusTone } {
  if (d.status === "disabled") return { label: "Disabled", tone: "disabled" };
  if (!d.online) return { label: "Offline", tone: "offline" };
  if (d.busy) return { label: "In session", tone: "busy" };
  return { label: "Online", tone: "online" };
}

export function StatusPill({ label, tone }: { label: string; tone: StatusTone }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full bg-surface/90 px-2 py-0.5 text-xs font-medium text-text shadow-sm backdrop-blur">
      <span className={`h-2 w-2 rounded-full ${dotClass[tone]}`} />
      {label}
    </span>
  );
}

export function secondLine(d: Device, now: number): string {
  if (!d.online) return `Last seen ${timeAgo(d.last_seen_at, now)}`;
  const health = d.info && !infoStale(d.info_at, d.online, now) ? tileHealthLine(d.info) : "";
  return health || `${osName(d.os)} · ${d.hostname}`;
}

/** CPU, memory and the fullest disk as small bars, dimmed when out of date. */
export function HealthBars({ device: d, now }: { device: Device; now: number }) {
  const info = d.info;
  if (!info) return null;
  const stale = infoStale(d.info_at, d.online, now);
  const disk = lowestFreeDisk(info.disks);
  const mem = info.mem_total > 0 ? info.mem_used / info.mem_total : 0;
  const bars: { label: string; value: number; tone: Severity; detail: string }[] = [];
  if (info.cpu_pct !== null)
    bars.push({
      label: "CPU",
      value: info.cpu_pct / 100,
      tone: "ok",
      detail: `${Math.round(info.cpu_pct)}%`,
    });
  if (info.mem_total > 0)
    bars.push({
      label: "Memory",
      value: mem,
      tone: memSeverity(info),
      detail: `${bytes(info.mem_used)} of ${bytes(info.mem_total)}`,
    });
  if (disk)
    bars.push({
      label: "Disk",
      value: 1 - freeFraction(disk),
      tone: diskSeverity(disk),
      detail: `${diskLabel(disk)}: ${bytes(disk.free)} free of ${bytes(disk.total)}`,
    });
  if (!bars.length) return null;
  return (
    <div
      className={`grid gap-2 text-[11px] text-muted ${stale ? "opacity-50" : ""}`}
      style={{ gridTemplateColumns: `repeat(${bars.length}, minmax(0, 1fr))` }}
      title={stale ? `As of ${timeAgo(d.info_at, now)}` : undefined}
    >
      {bars.map((b) => (
        <div key={b.label} className="min-w-0" title={`${b.label}: ${b.detail}`}>
          <div className="mb-0.5 flex justify-between gap-1">
            <span className="truncate">{b.label}</span>
            <span
              className={
                b.tone === "ok" ? "" : b.tone === "warning" ? "text-warning" : "text-error"
              }
            >
              {Math.round(b.value * 100)}%
            </span>
          </div>
          <Meter label={b.label} value={b.value} tone={b.tone} detail={b.detail} compact />
        </div>
      ))}
    </div>
  );
}

export function DeviceTile(p: TileProps) {
  const { device: d, size } = p;
  const actions = useDeviceActions();
  const status = p.status ?? deviceStatus(d);
  const primary = p.primary ?? {
    label: d.online ? "Connect" : "Details",
    onClick: () => (canConnect(d) ? actions.connect(d) : actions.details(d)),
    enabled: true,
    title: canConnect(d) ? connectTitle(d) : d.online ? connectTitle(d) : `Details of ${d.name}`,
  };
  const attention = attentionReasons(d, p.now);
  const tagList = d.tag_ids.map((t) => p.tags.get(t)).filter((t): t is Tag => !!t);
  const maxTags = size === "s" ? 1 : 3;

  return (
    <article
      className={`group relative flex h-full flex-col rounded-2xl border bg-surface transition-shadow hover:shadow-md ${
        p.selected ? "border-primary ring-2 ring-primary/40" : "border-outline"
      }`}
      draggable={p.organization}
      onDragStart={p.onDragStart}
      aria-label={d.name}
    >
      <div className="relative">
        <button
          type="button"
          data-tile-primary
          className={`group/media relative block aspect-video w-full overflow-hidden rounded-t-2xl ${
            d.online ? "bg-primary-soft/60" : "bg-surface-2"
          }`}
          onClick={(e) =>
            p.selecting || e.metaKey || e.ctrlKey || e.shiftKey ? p.onSelect(e) : primary.onClick()
          }
          title={primary.title}
          aria-label={`${primary.label}: ${d.name}`}
        >
          {p.media ?? <PreviewImage device={d} now={p.now} compact={size === "s"} />}
          {primary.enabled && (
            <span className="absolute inset-0 flex items-center justify-center bg-black/25 opacity-0 transition-opacity group-hover/media:opacity-100 group-focus-visible/media:opacity-100 motion-reduce:transition-none">
              <span className="inline-flex items-center gap-1.5 rounded-full bg-primary px-3 py-1.5 text-sm font-medium text-on-primary shadow">
                <Icon name={primary.label === "Details" ? "info" : "sessions"} size={16} />
                {primary.label}
              </span>
            </span>
          )}
        </button>
        <div className="pointer-events-none absolute bottom-2 left-2">
          <StatusPill {...status} />
        </div>
        {p.organization && (
          <label
            className={`absolute top-2 left-2 flex h-7 w-7 cursor-pointer items-center justify-center rounded-full bg-surface/90 shadow-sm ${
              p.selecting || p.selected
                ? ""
                : "opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
            }`}
            title="Select"
          >
            <input
              type="checkbox"
              className="h-4 w-4"
              aria-label={`Select ${d.name}`}
              checked={p.selected}
              onChange={() => undefined}
              onClick={(e) => {
                e.stopPropagation();
                p.onSelect(e);
              }}
            />
          </label>
        )}
        {p.organization && (
          <button
            type="button"
            className={`absolute top-2 right-2 flex h-7 w-7 items-center justify-center rounded-full bg-surface/90 shadow-sm ${
              d.favorite
                ? "text-warning"
                : "text-muted opacity-0 group-focus-within:opacity-100 group-hover:opacity-100"
            }`}
            aria-label={
              d.favorite ? `Remove ${d.name} from favorites` : `Add ${d.name} to favorites`
            }
            aria-pressed={d.favorite}
            title={d.favorite ? "Favorite" : "Add to favorites"}
            onClick={() => void actions.setFavorite([d.id], !d.favorite)}
          >
            <Icon name={d.favorite ? "star" : "starOutline"} size={18} />
          </button>
        )}
      </div>

      <div className={`flex flex-1 flex-col gap-2 ${size === "s" ? "p-3" : "p-4"}`}>
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium" title={d.name}>
              {d.name}
            </div>
            <div className="truncate text-xs text-muted">{secondLine(d, p.now)}</div>
          </div>
          {attention.length > 0 && (
            <span
              className="mt-0.5 text-warning"
              title={attention.map((a) => attentionText[a]).join("\n")}
              aria-label={attention.map((a) => attentionText[a]).join(". ")}
              role="img"
            >
              <Icon name="warning" size={18} />
            </span>
          )}
          <Menu
            label={`Actions for ${d.name}`}
            items={actions.menu(d)}
            className="btn-text btn-sm -mr-2 -mt-1 px-1.5"
          >
            <Icon name="moreVert" size={18} />
          </Menu>
        </div>
        {size !== "s" && <HealthBars device={d} now={p.now} />}
        {tagList.length > 0 && (
          <div className="flex min-w-0 flex-wrap gap-1">
            {tagList.slice(0, maxTags).map((t) => (
              <TagChip key={t.id} name={t.name} color={t.color} />
            ))}
            {tagList.length > maxTags && (
              <span
                className="text-xs text-muted"
                title={tagList
                  .slice(maxTags)
                  .map((t) => t.name)
                  .join(", ")}
              >
                +{tagList.length - maxTags}
              </span>
            )}
          </div>
        )}
        <div className="mt-auto flex min-h-0 flex-wrap items-center justify-between gap-2 empty:hidden">
          <DeviceUpdateBadge device={d} />
          <UpdateButton
            device={d}
            request={(force) => api.devices.requestUpdate(d.id, force)}
            onRequested={p.onKick}
          />
        </div>
      </div>
    </article>
  );
}
