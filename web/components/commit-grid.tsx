"use client";

import { useState } from "react";

import type { Verdict } from "@/lib/events";
import type { LocateState } from "@/lib/run-state";

import { seconds, short, VERDICT } from "./status";

type CellKind = Verdict | "testing" | "candidate" | "ruled-out";

type Cell = {
  index: number;
  kind: CellKind;
  culprit: boolean;
  // Why the cell has its verdict when the search did not test it.
  note?: string;
};

function cells(locate: LocateState): Cell[] {
  const last = locate.commits.length - 1;
  const culpritAt = locate.culprit ? locate.index[locate.culprit.commit.sha] : -1;
  return locate.commits.map((commit, index) => {
    const tested = locate.tested[commit.sha];
    let kind: CellKind;
    let note: string | undefined;
    if (tested) kind = tested.verdict;
    else if (commit.sha in locate.testing) kind = "testing";
    else if (index === 0) [kind, note] = ["pass", "the known-good commit"];
    else if (index === last) [kind, note] = ["fail", "the failing commit"];
    else if (index > locate.lo && index <= locate.hi) kind = "candidate";
    else kind = "ruled-out";
    return { index, kind, culprit: index === culpritAt, note };
  });
}

const CELL_STYLE: Record<CellKind, string> = {
  pass: "bg-good text-black",
  fail: "bg-bad text-white",
  flaky: "bg-warn text-black",
  skipped: "bg-skip text-ink",
  testing: "bg-raised ring-2 ring-accent testing",
  candidate: "bg-raised ring-1 ring-inset ring-muted",
  "ruled-out": "bg-line",
};

function describe(cell: Cell, locate: LocateState): string {
  const tested = locate.tested[locate.commits[cell.index].sha];
  if (cell.culprit) return "the culprit: first commit where the test fails";
  if (tested) return `${VERDICT[tested.verdict].label} in round ${tested.round}, ${seconds(tested.durationMs)}`;
  if (cell.note) return cell.note;
  if (cell.kind === "testing") return "being tested now";
  if (cell.kind === "candidate") return "not tested yet; the failure may start here";
  return "ruled out without being tested";
}

export function CommitGrid({ locate }: { locate: LocateState }) {
  const [hovered, setHovered] = useState<number | null>(null);
  const all = cells(locate);
  const culpritAt = locate.culprit ? locate.index[locate.culprit.commit.sha] : null;
  // With nothing hovered, the line below the grid shows the culprit.
  const focus = hovered ?? culpritAt;
  const commit = focus === null ? null : locate.commits[focus];

  return (
    <div>
      <div
        className="grid grid-cols-[repeat(auto-fill,minmax(20px,1fr))] gap-[3px]"
        role="list"
        aria-label={`${locate.commits.length - 1} commits between the last good build and the failing one, oldest first`}
        onPointerLeave={() => setHovered(null)}
      >
        {all.map((cell) => {
          const c = locate.commits[cell.index];
          const glyph = cell.kind in VERDICT ? VERDICT[cell.kind as Verdict].glyph : "";
          return (
            <button
              key={c.sha}
              type="button"
              role="listitem"
              // Only commits with something to say take a tab stop.
              tabIndex={glyph || cell.culprit ? 0 : -1}
              aria-label={`Commit ${cell.index}, ${c.subject}: ${describe(cell, locate)}`}
              onPointerEnter={() => setHovered(cell.index)}
              onFocus={() => setHovered(cell.index)}
              onBlur={() => setHovered(null)}
              className={`grid aspect-square place-items-center rounded-[4px] text-[11px] leading-none font-bold transition-colors duration-300 hover:brightness-110 focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ink ${CELL_STYLE[cell.kind]} ${
                cell.culprit ? "outline-2 outline-offset-2 outline-ink" : ""
              }`}
            >
              {glyph}
            </button>
          );
        })}
      </div>

      {/* The hover layer: one fixed line, so nothing jumps or covers the grid. */}
      <div className="mt-3 flex min-h-11 items-start gap-3 rounded-md border border-line bg-surface px-3 py-2 text-sm" aria-live="off">
        {commit && focus !== null ? (
          <>
            <span className="font-mono text-ink-2 tabular-nums">#{focus}</span>
            <span className="min-w-0 flex-1">
              <span className="font-medium">{commit.subject}</span>
              <span className="block text-xs text-ink-2">
                <span className="font-mono">{short(commit.sha)}</span> · {commit.author} · {commit.date.slice(0, 10)} ·{" "}
                {describe(all[focus], locate)}
              </span>
            </span>
          </>
        ) : (
          <span className="text-muted">Point at a commit to see what it is and how it was judged.</span>
        )}
      </div>

      <Legend />
    </div>
  );
}

function Legend() {
  const swatch = "grid size-[15px] place-items-center rounded-[3px] text-[9px] font-bold leading-none";
  return (
    <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-2">
      <li className="flex items-center gap-1.5">
        <span className={`${swatch} bg-good text-black`}>{VERDICT.pass.glyph}</span> test passes
      </li>
      <li className="flex items-center gap-1.5">
        <span className={`${swatch} bg-bad text-white`}>{VERDICT.fail.glyph}</span> test fails
      </li>
      <li className="flex items-center gap-1.5">
        <span className={`${swatch} bg-skip text-ink`}>{VERDICT.skipped.glyph}</span> does not build, skipped
      </li>
      <li className="flex items-center gap-1.5">
        <span className={`${swatch} bg-raised ring-1 ring-inset ring-muted`} /> could be the culprit
      </li>
      <li className="flex items-center gap-1.5">
        <span className={`${swatch} bg-line`} /> ruled out, never tested
      </li>
    </ul>
  );
}

// Narrowing shows the search window after each round as a bar over the same
// commit axis: how much of the history each round ruled out.
export function Narrowing({ locate }: { locate: LocateState }) {
  const last = locate.commits.length - 1;
  if (last < 1) return null;
  const rows = [
    { label: "start", lo: 0, hi: last },
    ...locate.rounds
      .filter((r) => r.round > 0 && r.after)
      .map((r) => ({ label: `round ${r.round}`, lo: r.after!.lo, hi: r.after!.hi })),
  ];
  const culpritAt = locate.culprit ? locate.index[locate.culprit.commit.sha] : null;
  return (
    <div className="space-y-1.5" role="img" aria-label={`The search window after each round: ${rows.map((r) => `${r.label}, ${r.hi - r.lo} commits`).join("; ")}`}>
      {rows.map((row) => (
        <div key={row.label} className="arrive flex items-center gap-3 text-xs">
          <span className="w-14 shrink-0 text-ink-2">{row.label}</span>
          <div className="relative h-2 flex-1 rounded-full bg-raised">
            <div
              className="absolute inset-y-0 min-w-1 rounded-full bg-ink-2 transition-all duration-500"
              style={{ left: `${(row.lo / last) * 100}%`, width: `${((row.hi - row.lo) / last) * 100}%` }}
            />
            {culpritAt !== null && (
              <div className="absolute -inset-y-0.5 w-0.5 bg-ink" style={{ left: `${((culpritAt - 0.5) / last) * 100}%` }} />
            )}
          </div>
          <span className="w-24 shrink-0 text-right text-ink tabular-nums">
            {row.hi - row.lo} {row.hi - row.lo === 1 ? "commit" : "commits"}
          </span>
        </div>
      ))}
    </div>
  );
}

// TestedTable is the same information as the grid without colour or hover.
export function TestedTable({ locate }: { locate: LocateState }) {
  const rows = locate.commits
    .map((commit, index) => ({ commit, index, tested: locate.tested[commit.sha] }))
    .filter((r) => r.tested);
  if (rows.length === 0) return null;
  return (
    <details className="text-sm">
      <summary className="cursor-pointer text-xs text-ink-2 hover:text-ink">
        Show the {rows.length} tested commits as a table
      </summary>
      <table className="mt-2 w-full text-left text-xs">
        <thead className="text-muted">
          <tr>
            <th className="py-1 pr-3 font-normal">#</th>
            <th className="py-1 pr-3 font-normal">Commit</th>
            <th className="py-1 pr-3 font-normal">Verdict</th>
            <th className="py-1 pr-3 font-normal">Round</th>
            <th className="py-1 text-right font-normal">Time</th>
          </tr>
        </thead>
        <tbody className="tabular-nums">
          {rows.map(({ commit, index, tested }) => (
            <tr key={commit.sha} className="border-t border-line">
              <td className="py-1 pr-3 font-mono text-ink-2">{index}</td>
              <td className="py-1 pr-3">
                <span className="font-mono text-ink-2">{short(commit.sha)}</span> {commit.subject}
              </td>
              <td className="py-1 pr-3">
                {VERDICT[tested.verdict].glyph} {VERDICT[tested.verdict].label}
              </td>
              <td className="py-1 pr-3">{tested.round}</td>
              <td className="py-1 text-right">{seconds(tested.durationMs)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}
