// Hand-rolled SVG charts; colors come from theme tokens so both themes work.

import type { ReactElement } from "react";

type Point = { hour: string; sessions: number; relay: number; failed: number };

export function SessionsBarChart({ series }: { series: Point[] }) {
  const W = 720;
  const H = 200;
  const padL = 32;
  const padB = 22;
  const padT = 8;
  const max = Math.max(1, ...series.map((p) => p.sessions + p.failed));
  const ticks = niceTicks(max);
  const top = ticks[ticks.length - 1] ?? max;
  const n = Math.max(1, series.length);
  const slot = (W - padL) / n;
  const bw = Math.max(2, slot * 0.68);
  const y = (v: number) => padT + (H - padT - padB) * (1 - v / top);
  const total = series.reduce((a, p) => a + p.sessions, 0);
  const relay = series.reduce((a, p) => a + p.relay, 0);

  return (
    <figure>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="h-auto w-full"
        role="img"
        aria-label={`Sessions per hour over the last 24 hours: ${total} total, ${relay} relayed`}
      >
        {ticks.map((t) => (
          <g key={t}>
            <line x1={padL} x2={W} y1={y(t)} y2={y(t)} stroke="var(--c-outline)" strokeWidth="1" />
            <text x={padL - 6} y={y(t) + 4} textAnchor="end" fontSize="10" fill="var(--c-muted)">
              {t}
            </text>
          </g>
        ))}
        {series.map((p, i) => {
          const x = padL + i * slot + (slot - bw) / 2;
          const direct = Math.max(0, p.sessions - p.relay);
          const hour = new Date(p.hour);
          const label = hour.toLocaleTimeString(undefined, { hour: "numeric" });
          return (
            <g key={p.hour}>
              <title>{`${label}: ${p.sessions} sessions (${p.relay} relay), ${p.failed} failed`}</title>
              <rect
                x={x}
                y={y(direct)}
                width={bw}
                height={y(0) - y(direct)}
                rx="2"
                fill="var(--c-primary)"
              />
              <rect
                x={x}
                y={y(direct + p.relay)}
                width={bw}
                height={y(direct) - y(direct + p.relay)}
                rx="2"
                fill="var(--c-relay)"
              />
              {p.failed > 0 && (
                <rect
                  x={x}
                  y={y(p.sessions + p.failed)}
                  width={bw}
                  height={y(p.sessions) - y(p.sessions + p.failed)}
                  rx="2"
                  fill="var(--c-error)"
                  opacity="0.75"
                />
              )}
              {i % 3 === 0 && (
                <text
                  x={x + bw / 2}
                  y={H - 6}
                  textAnchor="middle"
                  fontSize="10"
                  fill="var(--c-muted)"
                >
                  {label}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      <figcaption className="mt-2 flex flex-wrap gap-4 text-xs text-muted">
        <Legend color="var(--c-primary)" label="Direct (host / srflx)" />
        <Legend color="var(--c-relay)" label="Relayed (TURN)" />
        <Legend color="var(--c-error)" label="Failed" />
      </figcaption>
    </figure>
  );
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <svg width="10" height="10" aria-hidden="true">
        <rect width="10" height="10" rx="2" fill={color} />
      </svg>
      {label}
    </span>
  );
}

/** Evenly spaced axis ticks from 0 whose last value is the first step ≥ max. */
export function niceTicks(max: number): number[] {
  let step: number;
  if (max <= 5) step = 1;
  else if (max <= 10) step = 2;
  else {
    const mag = Math.pow(10, Math.floor(Math.log10(max)));
    step = max / mag > 5 ? mag * 2 : mag;
  }
  const out: number[] = [];
  for (let v = 0; ; v += step) {
    out.push(v);
    if (v >= max) break;
  }
  return out;
}

/** Horizontal share bar, e.g. ICE path breakdown. */
export function ShareBar({ parts }: { parts: { label: string; value: number; color: string }[] }) {
  const total = parts.reduce((a, p) => a + p.value, 0);
  return (
    <div>
      <svg
        viewBox="0 0 100 8"
        preserveAspectRatio="none"
        className="h-3 w-full overflow-hidden rounded-full"
        role="img"
        aria-label={parts.map((p) => `${p.label} ${p.value}`).join(", ")}
      >
        <rect width="100" height="8" fill="var(--c-surface-2)" />
        {total > 0 &&
          parts.reduce<{ x: number; els: ReactElement[] }>(
            (acc, p) => {
              const w = (p.value / total) * 100;
              acc.els.push(<rect key={p.label} x={acc.x} width={w} height="8" fill={p.color} />);
              acc.x += w;
              return acc;
            },
            { x: 0, els: [] },
          ).els}
      </svg>
      <div className="mt-2 flex flex-wrap gap-4 text-xs text-muted">
        {parts.map((p) => (
          <span key={p.label} className="inline-flex items-center gap-1.5">
            <Legend
              color={p.color}
              label={`${p.label}: ${p.value}${total ? ` (${Math.round((p.value / total) * 100)}%)` : ""}`}
            />
          </span>
        ))}
      </div>
    </div>
  );
}
