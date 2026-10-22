// Organization dialogs shared by the dashboard and the device page.

import { createContext, useCallback, useContext, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api, errorMessage } from "../../api/client";
import type { Device, Folder, FolderId, Tag, TagId } from "../../api/types";
import { useDirectory } from "../../app/directory";
import { Icon } from "../../components/icons";
import { Modal, TAG_COLORS, TagDot, tagStyle, usePrompt, useToast } from "../../components/ui";
import { buildTree, canMoveFolder, flattenTree } from "../../lib/folders";

export const NOTES_MAX = 4000;

type DialogReq =
  | { kind: "move"; ids: string[] }
  | { kind: "tags"; ids: string[] }
  | { kind: "notes"; id: string }
  | { kind: "tag"; tag: Tag | null }
  | { kind: "moveFolder"; folder: Folder };

const Ctx = createContext<(r: DialogReq) => void>(() => undefined);

/** Opens an organization dialog (see DeviceDialogs). */
export const useOrgDialog = () => useContext(Ctx);

export function DeviceDialogs({ children }: { children: ReactNode }) {
  const [req, setReq] = useState<DialogReq | null>(null);
  const close = useCallback(() => setReq(null), []);
  return (
    <Ctx.Provider value={setReq}>
      {children}
      {req?.kind === "move" && <MoveDialog ids={req.ids} onClose={close} />}
      {req?.kind === "tags" && <TagsDialog ids={req.ids} onClose={close} />}
      {req?.kind === "notes" && <NotesDialog id={req.id} onClose={close} />}
      {req?.kind === "tag" && <TagDialog tag={req.tag} onClose={close} />}
      {req?.kind === "moveFolder" && <MoveFolderDialog folder={req.folder} onClose={close} />}
    </Ctx.Provider>
  );
}

const countLabel = (ids: string[], devices: Device[]) =>
  ids.length === 1
    ? (devices.find((d) => d.id === ids[0])?.name ?? "device")
    : `${ids.length} devices`;

/** Asks for a name and creates a folder; resolves to it, or null. */
export function useCreateFolder() {
  const prompt = usePrompt();
  const toast = useToast();
  const { reloadOrg } = useDirectory();
  return useCallback(
    async (parent: FolderId | null): Promise<Folder | null> => {
      const name = await prompt({
        title: parent ? "New subfolder" : "New folder",
        label: "Name",
        confirm: "Create",
        maxLength: 64,
      });
      if (!name) return null;
      try {
        const { folder } = await api.folders.create(name, parent);
        await reloadOrg();
        return folder;
      } catch (e) {
        toast(errorMessage(e), "error");
        return null;
      }
    },
    [prompt, toast, reloadOrg],
  );
}

/** A radio list of folders, indented as a tree, with "No folder" first. */
export function FolderPicker({
  folders,
  value,
  onChange,
  disabled,
}: {
  folders: Folder[];
  value: FolderId | null;
  onChange: (id: FolderId | null) => void;
  disabled?: (id: FolderId) => boolean;
}) {
  const rows = useMemo(() => flattenTree(buildTree(folders), new Set()), [folders]);
  const opt = (id: FolderId | null, label: ReactNode, depth: number, off = false) => (
    <li key={id ?? "none"}>
      <label
        className={`flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 hover:bg-surface-2 ${off ? "pointer-events-none opacity-40" : ""}`}
        style={{ paddingLeft: 8 + depth * 18 }}
      >
        <input
          type="radio"
          name="folder"
          checked={value === id}
          disabled={off}
          onChange={() => onChange(id)}
        />
        {label}
      </label>
    </li>
  );
  return (
    <ul className="max-h-72 overflow-auto rounded-xl border border-outline p-1">
      {opt(
        null,
        <span className="flex items-center gap-2 text-muted">
          <Icon name="inbox" size={18} /> No folder
        </span>,
        0,
      )}
      {rows.map((n) =>
        opt(
          n.folder.id,
          <span className="flex min-w-0 items-center gap-2">
            <Icon name="folder" size={18} className="shrink-0 text-muted" />
            <span className="truncate">{n.folder.name}</span>
          </span>,
          n.depth,
          disabled?.(n.folder.id) ?? false,
        ),
      )}
    </ul>
  );
}

function MoveDialog({ ids, onClose }: { ids: string[]; onClose: () => void }) {
  const { devices, folders, editDevices } = useDirectory();
  const createFolder = useCreateFolder();
  const first = devices.find((d) => d.id === ids[0]);
  const same = ids.every((id) => devices.find((d) => d.id === id)?.folder_id === first?.folder_id);
  const [target, setTarget] = useState<FolderId | null>(same ? (first?.folder_id ?? null) : null);
  const save = async () => {
    onClose();
    await editDevices(
      ids,
      (d) => ({ ...d, folder_id: target }),
      () =>
        ids.length === 1
          ? api.devices.update(ids[0] as string, { folder_id: target })
          : api.devices.bulk(ids, { folder_id: target }),
    );
  };
  return (
    <Modal
      title={`Move ${countLabel(ids, devices)}`}
      onClose={onClose}
      footer={
        <>
          <button
            className="btn-text mr-auto"
            onClick={() => void createFolder(target).then((f) => f && setTarget(f.id))}
          >
            <Icon name="newFolder" size={18} />
            New folder
          </button>
          <button className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" onClick={() => void save()}>
            Move
          </button>
        </>
      }
    >
      <FolderPicker folders={folders} value={target} onChange={setTarget} />
    </Modal>
  );
}

function MoveFolderDialog({ folder, onClose }: { folder: Folder; onClose: () => void }) {
  const { folders, reloadOrg } = useDirectory();
  const toast = useToast();
  const [target, setTarget] = useState<FolderId | null>(folder.parent_id);
  const save = async () => {
    onClose();
    try {
      await api.folders.update(folder.id, { parent_id: target });
    } catch (e) {
      toast(errorMessage(e), "error");
    }
    await reloadOrg().catch(() => undefined);
  };
  return (
    <Modal
      title={`Move folder ${folder.name}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" onClick={() => void save()}>
            Move
          </button>
        </>
      }
    >
      <FolderPicker
        folders={folders}
        value={target}
        onChange={setTarget}
        disabled={(id) => !canMoveFolder(folders, folder.id, id)}
      />
    </Modal>
  );
}

type Mark = "all" | "some" | "none";

function TagsDialog({ ids, onClose }: { ids: string[]; onClose: () => void }) {
  const { devices, tags, editDevices, reloadOrg } = useDirectory();
  const toast = useToast();
  const selected = devices.filter((d) => ids.includes(d.id));
  const initial = (t: TagId): Mark => {
    const n = selected.filter((d) => d.tag_ids.includes(t)).length;
    return n === 0 ? "none" : n === selected.length ? "all" : "some";
  };
  const [marks, setMarks] = useState<Map<TagId, Mark>>(
    () => new Map(tags.map((t) => [t.id, initial(t.id)])),
  );
  const [filter, setFilter] = useState("");
  const shown = tags.filter((t) => t.name.toLowerCase().includes(filter.trim().toLowerCase()));
  const exact = tags.some((t) => t.name.toLowerCase() === filter.trim().toLowerCase());

  const create = async () => {
    const name = filter.trim();
    try {
      const { tag } = await api.tags.create(name, tags.length % TAG_COLORS);
      await reloadOrg();
      setMarks((m) => new Map(m).set(tag.id, "all"));
      setFilter("");
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const save = async () => {
    onClose();
    const add = [...marks].filter(([t, m]) => m === "all" && initial(t) !== "all").map(([t]) => t);
    const remove = [...marks]
      .filter(([t, m]) => m === "none" && initial(t) !== "none")
      .map(([t]) => t);
    if (!add.length && !remove.length) return;
    await editDevices(
      ids,
      (d) => ({
        ...d,
        tag_ids: [...new Set([...d.tag_ids.filter((t) => !remove.includes(t)), ...add])],
      }),
      () => api.devices.bulk(ids, { add_tag_ids: add, remove_tag_ids: remove }),
    );
  };

  return (
    <Modal
      title={`Tags for ${countLabel(ids, devices)}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" onClick={() => void save()}>
            Save
          </button>
        </>
      }
    >
      <form
        className="mb-3 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (filter.trim() && !exact) void create();
        }}
      >
        <input
          className="input"
          placeholder="Find or create a tag"
          value={filter}
          maxLength={32}
          onChange={(e) => setFilter(e.target.value)}
          aria-label="Find or create a tag"
        />
        {filter.trim() && !exact && (
          <button className="btn-tonal shrink-0" type="submit">
            <Icon name="add" size={18} />
            Create
          </button>
        )}
      </form>
      {tags.length === 0 && !filter && (
        <p className="text-muted">No tags yet. Type a name above to create one.</p>
      )}
      <ul className="max-h-72 space-y-0.5 overflow-auto">
        {shown.map((t) => {
          const m = marks.get(t.id) ?? "none";
          return (
            <li key={t.id}>
              <label className="flex cursor-pointer items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-surface-2">
                <input
                  type="checkbox"
                  checked={m === "all"}
                  ref={(el) => {
                    if (el) el.indeterminate = m === "some";
                  }}
                  onChange={() =>
                    setMarks((cur) => new Map(cur).set(t.id, m === "all" ? "none" : "all"))
                  }
                />
                <TagDot color={t.color} />
                <span className="truncate">{t.name}</span>
              </label>
            </li>
          );
        })}
      </ul>
    </Modal>
  );
}

function NotesDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const { devices, editDevices } = useDirectory();
  const d = devices.find((x) => x.id === id);
  const [notes, setNotes] = useState(d?.notes ?? "");
  const save = async () => {
    onClose();
    const n = notes.trim();
    await editDevices(
      [id],
      (x) => ({ ...x, notes: n }),
      () => api.devices.update(id, { notes: n }),
    );
  };
  return (
    <Modal
      title={`Notes for ${d?.name ?? "device"}`}
      onClose={onClose}
      wide
      footer={
        <>
          <span className="mr-auto self-center text-xs text-muted">
            {notes.length}/{NOTES_MAX}
          </span>
          <button className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" onClick={() => void save()}>
            Save
          </button>
        </>
      }
    >
      <textarea
        className="input min-h-48 font-sans"
        value={notes}
        maxLength={NOTES_MAX}
        placeholder="Who uses it, where it is, what to watch out for…"
        aria-label="Notes"
        onChange={(e) => setNotes(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void save();
        }}
      />
      <p className="mt-2 text-xs text-muted">Notes are searchable from the dashboard.</p>
    </Modal>
  );
}

function TagDialog({ tag, onClose }: { tag: Tag | null; onClose: () => void }) {
  const { tags, reloadOrg } = useDirectory();
  const toast = useToast();
  const [name, setName] = useState(tag?.name ?? "");
  const [color, setColor] = useState(tag?.color ?? tags.length % TAG_COLORS);
  const save = async () => {
    try {
      if (tag) await api.tags.update(tag.id, { name: name.trim(), color });
      else await api.tags.create(name.trim(), color);
      onClose();
      await reloadOrg();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  return (
    <Modal
      title={tag ? `Edit tag ${tag.name}` : "New tag"}
      onClose={onClose}
      footer={
        <>
          <button className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" disabled={!name.trim()} onClick={() => void save()}>
            Save
          </button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) void save();
        }}
      >
        <label className="block">
          <span className="label">Name</span>
          <input
            className="input"
            value={name}
            maxLength={32}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
      </form>
      <div className="mt-4">
        <div className="label">Color</div>
        <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Color">
          {Array.from({ length: TAG_COLORS }, (_, i) => (
            <button
              key={i}
              type="button"
              role="radio"
              aria-checked={color === i}
              aria-label={`Color ${i + 1}`}
              onClick={() => setColor(i)}
              className={`flex h-8 w-8 items-center justify-center rounded-full ring-offset-2 ring-offset-surface ${color === i ? "ring-2 ring-primary" : ""}`}
              style={{ backgroundColor: tagStyle(i).color }}
            >
              {color === i && <Icon name="check" size={16} className="text-white" />}
            </button>
          ))}
        </div>
        {name.trim() && (
          <div className="mt-4 text-xs text-muted">
            Preview:{" "}
            <span
              className="ml-1 inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
              style={tagStyle(color)}
            >
              {name.trim()}
            </span>
          </div>
        )}
      </div>
    </Modal>
  );
}
