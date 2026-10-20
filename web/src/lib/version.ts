// SemVer 2.0 validation and precedence, ported from proto/version.go
// (PROTOCOL §7 "Versions", vectors `versions`).

type Parsed = { core: bigint[]; pre: string[] };

const UINT64_MAX = 2n ** 64n - 1n;
const NUMERIC = /^[0-9]+$/;
const IDENT = /^[0-9A-Za-z-]+$/;

function validIdents(s: string, pre: boolean): boolean {
  if (s === "") return false;
  return s
    .split(".")
    .every((id) => IDENT.test(id) && !(pre && NUMERIC.test(id) && id.length > 1 && id[0] === "0"));
}

function parse(input: string): Parsed | null {
  let s = input.startsWith("v") ? input.slice(1) : input;
  const plus = s.indexOf("+");
  if (plus >= 0) {
    if (!validIdents(s.slice(plus + 1), false)) return null;
    s = s.slice(0, plus);
  }
  let pre: string[] = [];
  const dash = s.indexOf("-");
  if (dash >= 0) {
    if (!validIdents(s.slice(dash + 1), true)) return null;
    pre = s.slice(dash + 1).split(".");
    s = s.slice(0, dash);
  }
  const parts = s.split(".");
  if (parts.length !== 3) return null;
  const core: bigint[] = [];
  for (const p of parts) {
    if (!NUMERIC.test(p) || (p.length > 1 && p[0] === "0")) return null;
    const n = BigInt(p);
    if (n > UINT64_MAX) return null;
    core.push(n);
  }
  return { core, pre };
}

/** Reports whether s is a SemVer 2.0 version, optionally with a leading "v". */
export function validVersion(s: string): boolean {
  return parse(s) !== null;
}

const cmp = (a: bigint | number, b: bigint | number): number => (a < b ? -1 : a > b ? 1 : 0);

function cmpIdent(a: string, b: string): number {
  const na = NUMERIC.test(a);
  const nb = NUMERIC.test(b);
  if (na && nb) return cmp(BigInt(a), BigInt(b));
  if (na) return -1;
  if (nb) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

/** SemVer precedence: -1, 0 or 1. Build metadata is ignored; invalid versions sort first. */
export function compareVersions(a: string, b: string): number {
  const va = parse(a);
  const vb = parse(b);
  if (!va || !vb) return va ? 1 : vb ? -1 : 0;
  for (let i = 0; i < 3; i++) {
    const c = cmp(va.core[i] ?? 0n, vb.core[i] ?? 0n);
    if (c !== 0) return c;
  }
  // A release outranks any prerelease of the same core.
  if (!va.pre.length || !vb.pre.length) return cmp(vb.pre.length, va.pre.length);
  for (let i = 0; i < va.pre.length && i < vb.pre.length; i++) {
    const c = cmpIdent(va.pre[i] ?? "", vb.pre[i] ?? "");
    if (c !== 0) return c;
  }
  return cmp(va.pre.length, vb.pre.length);
}
