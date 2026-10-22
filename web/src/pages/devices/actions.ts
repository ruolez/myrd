import { useCallback } from "react";
import { useNavigate } from "react-router";
import { api } from "../../api/client";
import type { Device } from "../../api/types";
import { useDirectory } from "../../app/directory";
import { useWorkspace, useWorkspaceState } from "../../app/workspace";
import type { MenuEntry } from "../../components/menu";
import { usePrompt, useToast } from "../../components/ui";
import { canUpdate } from "../../lib/update";
import { useOrgDialog } from "./dialogs";

export const canConnect = (d: Device) => d.online && d.status !== "disabled" && !d.busy;

export function connectTitle(d: Device): string {
  if (d.status === "disabled") return "Device is disabled";
  if (!d.online) return "Device is offline";
  if (d.busy) return "Device is in another session";
  return `Connect to ${d.name}`;
}

/** Device actions for tiles, rows, menus and the bulk bar. */
export function useDeviceActions() {
  const navigate = useNavigate();
  const prompt = usePrompt();
  const toast = useToast();
  const dialog = useOrgDialog();
  const { editDevices, organization, kickUpdates } = useDirectory();
  const ws = useWorkspace();
  const wsState = useWorkspaceState();
  const openHere = useCallback((d: Device) => d.id in wsState.entries, [wsState]);

  const connect = useCallback((d: Device) => navigate(`/session/${d.id}`), [navigate]);
  /** Connects without leaving the page; the session waits in the workspace. */
  const openInBackground = useCallback(
    (d: Device) => {
      const r = ws.open(d.id);
      if (r.ok) toast(`Connecting to ${d.name} in the background`);
      else toast("Too many sessions are open. Close one first.", "error");
    },
    [ws, toast],
  );
  const details = useCallback((d: Device) => navigate(`/devices/${d.id}`), [navigate]);

  const setFavorite = useCallback(
    (ids: string[], favorite: boolean) =>
      editDevices(
        ids,
        (d) => ({ ...d, favorite }),
        () =>
          ids.length === 1
            ? api.devices.update(ids[0] as string, { favorite })
            : api.devices.bulk(ids, { favorite }),
      ),
    [editDevices],
  );

  const rename = useCallback(
    async (d: Device) => {
      const name = await prompt({
        title: `Rename ${d.name}`,
        label: "Name",
        initial: d.name,
        maxLength: 64,
      });
      if (!name || name === d.name) return;
      await editDevices(
        [d.id],
        (x) => ({ ...x, name }),
        () => api.devices.update(d.id, { name }),
      );
    },
    [prompt, editDevices],
  );

  const update = useCallback(
    async (d: Device) => {
      try {
        const { version } = await api.devices.requestUpdate(d.id, false);
        toast(
          d.busy
            ? `${d.name} installs ${version} when the session ends`
            : `${d.name} is updating to ${version}`,
          "success",
        );
        kickUpdates();
      } catch (e) {
        toast(e instanceof Error ? e.message : String(e), "error");
      }
    },
    [toast, kickUpdates],
  );

  const menu = useCallback(
    (d: Device): MenuEntry[] => {
      const org: MenuEntry[] = organization
        ? [
            {
              label: d.favorite ? "Remove from favorites" : "Add to favorites",
              icon: d.favorite ? "star" : "starOutline",
              onSelect: () => void setFavorite([d.id], !d.favorite),
            },
            {
              label: "Move to folder…",
              icon: "folder",
              onSelect: () => dialog({ kind: "move", ids: [d.id] }),
            },
            { label: "Tags…", icon: "tag", onSelect: () => dialog({ kind: "tags", ids: [d.id] }) },
            { label: "Notes…", icon: "notes", onSelect: () => dialog({ kind: "notes", id: d.id }) },
            "separator",
          ]
        : [];
      const here = openHere(d);
      return [
        {
          label: here ? "Switch to session" : "Connect",
          icon: "sessions",
          disabled: !here && !canConnect(d),
          onSelect: () => connect(d),
        },
        ...(here
          ? [
              {
                label: "End session",
                icon: "callEnd" as const,
                onSelect: () => ws.close(d.id),
              },
            ]
          : [
              {
                label: "Open in background",
                icon: "open" as const,
                disabled: !canConnect(d),
                onSelect: () => openInBackground(d),
              },
            ]),
        { label: "Details", icon: "info", onSelect: () => details(d) },
        "separator",
        ...org,
        { label: "Rename…", icon: "edit", onSelect: () => void rename(d) },
        ...(canUpdate(d) && !d.busy
          ? [{ label: "Update now", icon: "release" as const, onSelect: () => void update(d) }]
          : []),
        {
          label: "Copy device ID",
          icon: "copy",
          onSelect: () =>
            void navigator.clipboard?.writeText(d.id).then(() => toast("Device ID copied")),
        },
      ];
    },
    [
      organization,
      setFavorite,
      dialog,
      connect,
      details,
      rename,
      update,
      toast,
      openHere,
      openInBackground,
      ws,
    ],
  );

  return { connect, details, setFavorite, rename, menu, dialog, openHere, openInBackground };
}
