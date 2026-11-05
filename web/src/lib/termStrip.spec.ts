import { describe, expect, test } from "vitest";
import { stripItems } from "./termStrip";
import type { TermSource, TermSourceId } from "./terminal";

/** Deterministic pseudo-random numbers (mulberry32). */
function rng(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const id = (s: string) => s as TermSourceId;
const source = (s: string): TermSource => ({
  id: id(s),
  kind: "tmux",
  title: s,
  detail: "",
  attached: 0,
  admin: false,
  fidelity: "full",
  created: 0,
});
type FakeTab = { sourceId: TermSourceId };
const tab = (s: string): FakeTab => ({ sourceId: id(s) });
const idOf = (t: FakeTab) => t.sourceId;

describe("stripItems", () => {
  test("keeps the first-seen order, then adds attached and listed terminals", () => {
    const items = stripItems(
      [id("tmux:b"), id("tmux:a")],
      [tab("shell:1")],
      idOf,
      [source("tmux:a"), source("tmux:b"), source("tmux:c")],
      new Set(),
    );
    expect(items.map((i) => i.id)).toEqual(["tmux:b", "tmux:a", "shell:1", "tmux:c"]);
  });

  test("an attached tab carries its listing, an unattached one only the listing", () => {
    const t = tab("tmux:a");
    expect(stripItems([], [t], idOf, [source("tmux:a"), source("tmux:b")], new Set())).toEqual([
      { id: "tmux:a", tab: t, source: source("tmux:a") },
      { id: "tmux:b", tab: null, source: source("tmux:b") },
    ]);
  });

  test("hides closed terminals unless attached, and drops ones gone from the device", () => {
    const items = stripItems(
      [id("tmux:gone"), id("tmux:a"), id("tmux:b")],
      [tab("tmux:b")],
      idOf,
      [source("tmux:a"), source("tmux:b")],
      new Set([id("tmux:a"), id("tmux:b")]),
    );
    expect(items.map((i) => i.id)).toEqual(["tmux:b"]);
  });

  test("every attached tab shows exactly once, whatever the order and listing", () => {
    const r = rng(5);
    const names = ["a", "b", "c", "d", "e"].map((s) => `tmux:${s}`);
    const pick = () => names.filter(() => r() < 0.5);
    for (let i = 0; i < 300; i++) {
      const attached = pick();
      const ids = stripItems(
        [...pick(), ...pick()].map(id),
        attached.map(tab),
        idOf,
        [...pick(), ...pick()].map(source),
        new Set(pick().map(id)),
      ).map((x) => x.id);
      expect(new Set(ids).size).toBe(ids.length);
      expect(attached.filter((a) => ids.includes(id(a)))).toEqual(attached);
    }
  });
});
