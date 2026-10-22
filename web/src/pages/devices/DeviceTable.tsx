import type { DragEvent, MouseEvent } from "react";
import type { Device, Folder, Tag } from "../../api/types";
import { DeviceUpdateBadge } from "../../components/devices";
import { Icon, OsIcon } from "../../components/icons";
import { Menu } from "../../components/menu";
import { TagChip } from "../../components/ui";
import { attentionReasons, attentionText } from "../../lib/deviceFilters";
import type { SortKey } from "../../lib/deviceFilters";
import { folderPath } from "../../lib/folders";
import { timeAgo } from "../../lib/format";
import { canConnect, connectTitle, useDeviceActions } from "./actions";
import { HealthBars, StatusPill, deviceStatus, secondLine } from "./DeviceTile";
import type { StatusTone } from "./DeviceTile";

export function DeviceTable({
  devices,
  tags,
  folders,
  now,
  selected,
  organization,
  sort,
  onSort,
  onSelect,
  onDragStart,
  statusOf,
  primaryOf,
}: {
  devices: Device[];
  tags: Map<string, Tag>;
  folders: Folder[];
  now: number;
  selected: ReadonlySet<string>;
  organization: boolean;
  sort: SortKey;
  onSort: (s: SortKey) => void;
  onSelect: (d: Device, e: MouseEvent) => void;
  onDragStart: (d: Device, e: DragEvent) => void;
  statusOf: (d: Device) => { label: string; tone: StatusTone } | undefined;
  primaryOf: (
    d: Device,
  ) => { label: string; onClick: () => void; enabled: boolean; title: string } | undefined;
}) {
  const actions = useDeviceActions();
  const sortHeader = (key: SortKey, label: string) => (
    <th
      className="whitespace-nowrap"
      aria-sort={sort === key ? (key === "name" ? "ascending" : "descending") : "none"}
    >
      <button
        className="inline-flex items-center gap-1 uppercase hover:text-text"
        onClick={() => onSort(key)}
      >
        {label}
        {sort === key && <Icon name="chevronDown" size={14} />}
      </button>
    </th>
  );
  return (
    <div className="card overflow-x-auto">
      <table className="data-table">
        <thead>
          <tr>
            {organization && <th className="w-8" aria-label="Selected" />}
            {sortHeader("name", "Device")}
            {sortHeader("status", "Status")}
            {organization && <th>Folder</th>}
            {organization && <th>Tags</th>}
            <th className="min-w-48">Health</th>
            {sortHeader("last_seen", "Last seen")}
            {sortHeader("last_connected", "Last connected")}
            <th className="text-right">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {devices.map((d) => {
            const st = statusOf(d) ?? deviceStatus(d);
            const primary = primaryOf(d) ?? {
              label: "Connect",
              onClick: () => actions.connect(d),
              enabled: canConnect(d),
              title: connectTitle(d),
            };
            const attention = attentionReasons(d, now);
            const path = folderPath(folders, d.folder_id);
            return (
              <tr
                key={d.id}
                draggable={organization}
                onDragStart={(e) => onDragStart(d, e)}
                className={selected.has(d.id) ? "bg-primary-soft/50" : "hover:bg-surface-2/60"}
              >
                {organization && (
                  <td>
                    <input
                      type="checkbox"
                      aria-label={`Select ${d.name}`}
                      checked={selected.has(d.id)}
                      onChange={() => undefined}
                      onClick={(e) => onSelect(d, e)}
                    />
                  </td>
                )}
                <td className="min-w-48">
                  <div className="flex items-center gap-3">
                    <span className={d.online ? "text-primary" : "text-muted"}>
                      <OsIcon os={d.os} size={20} />
                    </span>
                    <div className="min-w-0">
                      <div className="flex items-center gap-1.5">
                        {d.favorite && (
                          <Icon
                            name="star"
                            size={14}
                            className="text-warning"
                            aria-label="Favorite"
                          />
                        )}
                        <button
                          className="truncate font-medium hover:underline"
                          onClick={() => actions.details(d)}
                        >
                          {d.name}
                        </button>
                        {attention.length > 0 && (
                          <span
                            className="text-warning"
                            title={attention.map((a) => attentionText[a]).join("\n")}
                            role="img"
                            aria-label={attention.map((a) => attentionText[a]).join(". ")}
                          >
                            <Icon name="warning" size={16} />
                          </span>
                        )}
                      </div>
                      <div className="truncate text-xs text-muted">{d.hostname}</div>
                    </div>
                  </div>
                </td>
                <td>
                  <div className="flex flex-col items-start gap-1">
                    <StatusPill {...st} />
                    <DeviceUpdateBadge device={d} />
                  </div>
                </td>
                {organization && (
                  <td className="max-w-48 truncate text-muted" title={path.join(" / ")}>
                    {path.length ? path.join(" / ") : "—"}
                  </td>
                )}
                {organization && (
                  <td>
                    <div className="flex max-w-56 flex-wrap gap-1">
                      {d.tag_ids
                        .map((t) => tags.get(t))
                        .filter((t): t is Tag => !!t)
                        .map((t) => (
                          <TagChip key={t.id} name={t.name} color={t.color} />
                        ))}
                    </div>
                  </td>
                )}
                <td>
                  {d.info ? (
                    <div className="space-y-1">
                      <div className="truncate text-xs text-muted">{secondLine(d, now)}</div>
                      <HealthBars device={d} now={now} />
                    </div>
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                </td>
                <td className="whitespace-nowrap text-muted">
                  {d.online ? "now" : timeAgo(d.last_seen_at, now)}
                </td>
                <td className="whitespace-nowrap text-muted">
                  {d.last_connected_at ? timeAgo(d.last_connected_at, now) : "never"}
                </td>
                <td>
                  <div className="flex items-center justify-end gap-1">
                    <button
                      className="btn-tonal btn-sm"
                      disabled={!primary.enabled}
                      title={primary.title}
                      onClick={primary.onClick}
                    >
                      {primary.label}
                    </button>
                    <Menu
                      label={`Actions for ${d.name}`}
                      items={actions.menu(d)}
                      className="btn-text btn-sm px-1.5"
                    >
                      <Icon name="moreVert" size={18} />
                    </Menu>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
