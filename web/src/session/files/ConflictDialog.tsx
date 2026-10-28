// "Already exists" question for transfers, answered one file at a time or
// for all remaining conflicts of the batch. Questions from several batches
// wait their turn.

import { useCallback, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Modal } from "../../components/ui";
import type { Choice, ConflictAnswer } from "../../lib/transferPlan";
import type { Ask, ConflictQuestion } from "./batches";

type Pending = { id: number; q: ConflictQuestion; resolve: (a: ConflictAnswer) => void };

export function useConflictPrompt(): { ask: Ask; dialog: ReactNode } {
  const [queue, setQueue] = useState<readonly Pending[]>([]);
  const seq = useRef(0);
  const ask = useCallback<Ask>(
    (q) => new Promise((resolve) => setQueue((qs) => [...qs, { id: ++seq.current, q, resolve }])),
    [],
  );
  const head = queue[0];
  const answer = (a: ConflictAnswer) => {
    head?.resolve(a);
    setQueue((qs) => qs.slice(1));
  };
  const dialog = head ? <ConflictDialog key={head.id} question={head.q} onAnswer={answer} /> : null;
  return { ask, dialog };
}

function ConflictDialog({
  question: q,
  onAnswer,
}: {
  question: ConflictQuestion;
  onAnswer: (a: ConflictAnswer) => void;
}) {
  const [all, setAll] = useState(false);
  const pick = (choice: Choice) => onAnswer({ choice, all });
  const side = q.direction === "upload" ? "on the device" : "on this computer";
  return (
    <Modal
      title="Already exists"
      onClose={() => pick("skip")}
      footer={
        <>
          <button className="btn-text" onClick={() => pick("skip")}>
            Skip
          </button>
          <button className="btn-tonal" onClick={() => pick("keep")}>
            Keep both
          </button>
          <button className="btn-primary" onClick={() => pick("overwrite")}>
            Replace
          </button>
        </>
      }
    >
      <p>
        <span className="font-medium break-all">{q.name}</span> is already in{" "}
        <span className="break-all">{q.where}</span> {side}.
      </p>
      <p className="mt-2 text-muted">
        Replace overwrites it. Keep both saves the new one with a number added to its name.
      </p>
      {q.remaining > 1 && (
        <label className="mt-4 flex items-center gap-2">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
          Do this for all {q.remaining} conflicts
        </label>
      )}
    </Modal>
  );
}
