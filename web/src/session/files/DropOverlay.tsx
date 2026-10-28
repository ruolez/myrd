import { Icon } from "../../components/icons";

/** Covers the stage while files are dragged over it. Purely visual: the stage handles the drop. */
export function DropOverlay({ target, folders }: { target: string | null; folders: boolean }) {
  return (
    <div className="pointer-events-none absolute inset-3 z-40 flex flex-col items-center justify-center gap-3 rounded-2xl border-2 border-dashed border-[#8ab4f8] bg-black/70 text-white">
      <Icon name="upload" size={40} className="text-[#8ab4f8]" />
      <div className="text-lg">
        {folders ? "Drop files or folders to upload" : "Drop files to upload"}
      </div>
      <div className="max-w-md truncate px-4 text-sm text-white/70">
        {target ? `To ${target}` : "To the device's Downloads folder"}
      </div>
      {!folders && (
        <div className="text-xs text-white/50">
          This device's agent can't receive folders yet; zip them first.
        </div>
      )}
    </div>
  );
}
