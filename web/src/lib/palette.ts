// Fuzzy matching for the command palette: the query's characters must appear
// in order; matches at the start, at word starts and in runs score higher.

/** null when q is not a subsequence of text; higher is better. */
export function fuzzyScore(q: string, text: string): number | null {
  const query = q.trim().toLowerCase();
  if (!query) return 0;
  const t = text.toLowerCase();
  let score = 0;
  let ti = 0;
  let prev = -2;
  for (const ch of query) {
    if (ch === " ") continue;
    const at = t.indexOf(ch, ti);
    if (at < 0) return null;
    const wordStart = at === 0 || /[\s\-_./()]/.test(t[at - 1] ?? "");
    score += 1;
    if (at === 0) score += 6;
    else if (wordStart) score += 4;
    if (at === prev + 1) score += 3;
    score -= Math.min(at - ti, 4) * 0.25;
    prev = at;
    ti = at + 1;
  }
  return score - t.length * 0.01;
}

export type Ranked<T> = { item: T; score: number };

/** Items whose best field matches, best first; ties keep the input order. */
export function rankItems<T>(
  items: T[],
  q: string,
  fields: (item: T) => string[],
  limit = 50,
): T[] {
  const ranked: (Ranked<T> & { i: number })[] = [];
  items.forEach((item, i) => {
    let best: number | null = null;
    for (const f of fields(item)) {
      const s = fuzzyScore(q, f);
      if (s !== null && (best === null || s > best)) best = s;
    }
    if (best !== null) ranked.push({ item, score: best, i });
  });
  ranked.sort((a, b) => b.score - a.score || a.i - b.i);
  return ranked.slice(0, limit).map((r) => r.item);
}
