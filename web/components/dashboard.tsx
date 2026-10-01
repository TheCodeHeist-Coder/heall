"use client";

import { useEffect, useRef, useState } from "react";

import { parseEvent, type Stage } from "@/lib/events";
import { parseStream, STAGES, type RunState, type StageStatus } from "@/lib/run-state";
import {
  SAMPLES,
  sourceKey,
  useEvents,
  usePlayback,
  useQuery,
  useRuns,
  useRunState,
  type Playback,
  type RunInfo,
  type Source,
} from "@/lib/use-run";

import { AgentTimeline } from "./agent-timeline";
import { CommitGrid, Narrowing, TestedTable } from "./commit-grid";
import { Command, GetStarted } from "./get-started";
import { Mark, Panel, seconds, short, VERDICT } from "./status";

const STAGE_LABEL: Record<Stage, string> = {
  run: "Run",
  triage: "Triage",
  reproduce: "Reproduce",
  locate: "Locate",
  heal: "Heal",
  deliver: "Deliver",
};

const STAGE_HINT: Record<Stage, string> = {
  run: "",
  triage: "which test failed",
  reproduce: "is it real and steady",
  locate: "which commit caused it",
  heal: "can a fix be proved",
  deliver: "pull request or diagnosis",
};

export function Dashboard() {
  const { runs, online } = useRuns();
  // Until the reader picks something, show the newest run on this machine.
  // With no runs there is nothing to show: example runs are only opened on
  // request, so the dashboard never looks as if it held someone's real data.
  const [picked, setPicked] = useState<Source | null>(null);
  const query = useQuery();
  const linked: Source | null = query.get("run")
    ? { kind: "server", id: query.get("run")! }
    : query.get("sample")
      ? { kind: "sample", name: query.get("sample")! }
      : null;
  const [unlinked, setUnlinked] = useState(false);
  const chosen = picked ?? (unlinked ? null : linked);
  const source: Source | null = chosen ?? (runs.length > 0 ? { kind: "server", id: runs[0].id } : null);
  const pick = (s: Source | null) => {
    setUnlinked(true);
    setPicked(s);
  };

  // Two pages in one: the guide, and the dashboard. A newcomer gets the
  // guide. Someone with runs on this machine, or following a link to a run,
  // gets the dashboard.
  const [view, setView] = useState<"guide" | "dashboard" | null>(null);
  const asked = query.get("view");
  const page = view ?? (asked === "dashboard" || asked === "guide" ? asked : linked || runs.length > 0 ? "dashboard" : "guide");

  const [theme, setTheme] = useState<"dark" | "light" | null>(null);
  const forced = theme ?? (query.get("theme") === "dark" || query.get("theme") === "light" ? (query.get("theme") as "dark" | "light") : null);
  useEffect(() => {
    if (forced) document.documentElement.dataset.theme = forced;
    else delete document.documentElement.dataset.theme;
  }, [forced]);

  const { events, live, error } = useEvents(source);
  const at = Number.parseInt(query.get("at") ?? "", 10);
  const playback = usePlayback(events, sourceKey(source), Number.isFinite(at) && !unlinked ? at : null);
  const state = useRunState(events, playback.shown);

  const switchTheme = () => {
    const dark = forced ? forced === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
    setTheme(dark ? "light" : "dark");
  };

  if (page === "guide") {
    return (
      <div className="mx-auto flex min-h-screen max-w-3xl flex-col gap-6 p-4">
        <header className="flex items-center gap-3">
          <h1 className="font-mono text-xl font-bold tracking-tight">heall</h1>
          <span className="flex-1" />
          <button type="button" onClick={switchTheme} className="rounded-md border border-line bg-surface px-2.5 py-1.5 text-sm hover:bg-raised">
            Switch theme
          </button>
          <button type="button" onClick={() => setView("dashboard")} className="rounded-md bg-ink px-3.5 py-1.5 text-sm font-medium text-page hover:opacity-90">
            Dashboard
          </button>
        </header>
        {/* Nothing is drawn until it is known whether this machine has runs,
            so the guide does not flash by on the way to the dashboard. */}
        {(online !== null || linked) && <GetStarted onDashboard={() => setView("dashboard")} />}
      </div>
    );
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-[1500px] flex-col gap-4 p-4">
      <Header
        runs={runs}
        source={source}
        following={chosen === null}
        onPick={pick}
        onTheme={switchTheme}
        onGuide={() => setView("guide")}
        // Back undoes the last step: out of an example or an opened file to
        // the dashboard as it was, and from there to the home page.
        onBack={() => (chosen ? pick(null) : setView("guide"))}
        live={live}
        state={state}
        playback={playback}
        total={events.length}
      />
      {error && <p className="rounded-md bg-bad-wash px-3 py-2 text-sm">{error}</p>}
      {source && <StageRail state={state} />}

      {!source ? (
        <NoRuns online={online} onGuide={() => setView("guide")} onExample={() => pick({ kind: "sample", name: SAMPLES[0].name })} />
      ) : events.length === 0 ? (
        <p className="py-20 text-center text-sm text-muted">{live ? "Waiting for the run to start…" : "Loading…"}</p>
      ) : (
        <div className="grid flex-1 grid-cols-[minmax(0,1fr)] items-start gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          <div className="space-y-4">
            <Failure state={state} />
            <Locate state={state} />
            <Outcome state={state} />
          </div>
          <Panel
            title="Heal: the agent"
            aside={state.agent && `${state.agent.model} · up to ${state.agent.maxAttempts} patch attempts`}
            className="lg:sticky lg:top-4"
          >
            {state.agent ? (
              <AgentTimeline agent={state.agent} following={!playback.paused} />
            ) : (
              <p className="p-4 text-sm text-muted">
                {state.escalated
                  ? "The run stopped before the agent was needed."
                  : "The agent starts once the culprit commit is known."}
              </p>
            )}
          </Panel>
        </div>
      )}
    </div>
  );
}

// NoRuns is the dashboard with nothing in it yet. It says where runs come
// from instead of showing someone else's.
function NoRuns({ online, onGuide, onExample }: { online: boolean | null; onGuide: () => void; onExample: () => void }) {
  return (
    <section className="mx-auto mt-10 w-full max-w-xl rounded-lg border border-line bg-surface p-6 text-center">
      <h2 className="text-lg font-semibold">No runs yet</h2>
      {online ? (
        <>
          <p className="mt-2 text-sm text-ink-2">
            Start a run in this project and it will appear here by itself, as it happens.
          </p>
          <div className="mt-4 text-left">
            <Command text="heall run --good LAST_GREEN_COMMIT --dry-run" />
          </div>
        </>
      ) : (
        <p className="mt-2 text-sm text-ink-2">
          This website does not store anyone&apos;s runs. Your runs appear on the dashboard heall opens on your own
          machine: run <span className="font-mono">heall serve</span> in your project and open{" "}
          <span className="font-mono">http://localhost:7777</span>.
        </p>
      )}
      <div className="mt-5 flex flex-wrap justify-center gap-2">
        <button type="button" onClick={onGuide} className="rounded-md bg-ink px-3.5 py-1.5 text-sm font-medium text-page hover:opacity-90">
          How to use heall
        </button>
        <button type="button" onClick={onExample} className="rounded-md border border-line px-3.5 py-1.5 text-sm hover:bg-raised">
          Watch an example run
        </button>
      </div>
    </section>
  );
}

function Header(props: {
  runs: RunInfo[];
  source: Source | null;
  following: boolean;
  onPick: (s: Source | null) => void;
  onTheme: () => void;
  onGuide: () => void;
  onBack: () => void;
  live: boolean;
  state: RunState;
  playback: Playback;
  total: number;
}) {
  const { runs, source, onPick, live, state, playback, total } = props;
  const file = useRef<HTMLInputElement>(null);
  const value = props.following ? (runs.length > 0 ? "latest" : "none") : sourceKey(source);

  const choose = (v: string) => {
    if (v === "latest" || v === "none") return onPick(null);
    const [kind, ...rest] = v.split(":");
    const name = rest.join(":");
    if (kind === "server") onPick({ kind: "server", id: name });
    if (kind === "sample") onPick({ kind: "sample", name });
  };
  const open = async (f: File | undefined) => {
    if (!f) return;
    onPick({ kind: "file", name: f.name, events: parseStream(await f.text(), parseEvent) });
  };

  return (
    <header className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <button
        type="button"
        onClick={props.onBack}
        className="flex items-center gap-1.5 rounded-md border border-line bg-surface px-2.5 py-1.5 text-sm hover:bg-raised"
      >
        <svg viewBox="0 0 16 16" className="size-4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
          <path d="M9.5 3.5L5 8l4.5 4.5" />
        </svg>
        Back
      </button>
      <h1 className="font-mono text-xl font-bold tracking-tight">heall</h1>
      <button type="button" onClick={props.onGuide} className="rounded-md border border-line bg-surface px-2.5 py-1.5 text-sm hover:bg-raised">
        How to use
      </button>

      <label className="flex items-center gap-2 text-sm">
        <span className="sr-only">Run to show</span>
        <select
          value={value}
          onChange={(e) => choose(e.target.value)}
          className="max-w-80 rounded-md border border-line bg-surface px-2 py-1.5 text-sm"
        >
          {runs.length > 0 ? <option value="latest">Latest run on this machine</option> : <option value="none">No run selected</option>}
          {source?.kind === "file" && <option value={sourceKey(source)}>File: {source.name}</option>}
          {runs.length > 0 && (
            <optgroup label="Runs on this machine">
              {runs.map((r) => (
                <option key={r.id} value={`server:${r.id}`}>
                  {r.started.slice(11, 19)} · {r.branch || short(r.bad)} · {r.outcome || "running"}
                </option>
              ))}
            </optgroup>
          )}
          <optgroup label="Example runs (recorded)">
            {SAMPLES.map((s) => (
              <option key={s.name} value={`sample:${s.name}`}>
                {s.label}
              </option>
            ))}
          </optgroup>
        </select>
      </label>
      <button type="button" onClick={() => file.current?.click()} className="rounded-md border border-line bg-surface px-2.5 py-1.5 text-sm hover:bg-raised">
        Open events file
      </button>
      <input ref={file} type="file" accept=".jsonl,.json,.txt" className="hidden" onChange={(e) => open(e.target.files?.[0])} />

      <RunStatus live={live} state={state} replaying={playback.replaying} />
      <span className="flex-1" />

      <div className="flex items-center gap-2 text-sm">
        {playback.replaying && (
          <>
            <input
              type="range"
              min={0}
              max={total}
              value={playback.shown}
              onChange={(e) => playback.seek(Number(e.target.value))}
              aria-label="Position in the replay"
              className="w-40 accent-[var(--accent)]"
            />
            <span className="w-16 text-xs text-ink-2 tabular-nums">
              {playback.shown} / {total}
            </span>
            <button type="button" onClick={playback.togglePause} className="rounded-md border border-line bg-surface px-2.5 py-1.5 hover:bg-raised">
              {playback.paused ? "Resume" : "Pause"}
            </button>
          </>
        )}
        <label className="flex items-center gap-1.5 text-xs text-ink-2">
          Speed
          <select
            value={playback.speed}
            onChange={(e) => playback.setSpeed(Number(e.target.value))}
            className="rounded-md border border-line bg-surface px-1.5 py-1.5 text-sm text-ink"
          >
            {[0.5, 1, 2, 4].map((s) => (
              <option key={s} value={s}>
                {s}×
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          onClick={props.onTheme}
          className="rounded-md border border-line bg-surface px-2.5 py-1.5 hover:bg-raised"
        >
          Switch theme
        </button>
        <button
          type="button"
          onClick={playback.replay}
          disabled={total === 0 || live}
          title={live ? "Available once the run has finished" : undefined}
          className="rounded-md bg-ink px-3 py-1.5 font-medium text-page disabled:opacity-40"
        >
          Replay
        </button>
      </div>
    </header>
  );
}

function RunStatus({ live, state, replaying }: { live: boolean; state: RunState; replaying: boolean }) {
  if (state.done?.outcome === "fixed") return <Mark tone="good">Fixed in {seconds(state.done.duration_ms)}</Mark>;
  if (state.done?.outcome === "escalated") return <Mark tone="warn">Escalated in {seconds(state.done.duration_ms)}</Mark>;
  if (state.done?.outcome === "error") return <Mark tone="bad">Stopped on an error</Mark>;
  if (replaying) return <Mark tone="idle">Replaying</Mark>;
  if (live) {
    return (
      <span className="inline-flex items-center gap-1.5 rounded-full bg-raised px-2 py-0.5 text-xs font-medium">
        <span className="size-2 animate-pulse rounded-full bg-accent" /> Live
      </span>
    );
  }
  return null;
}

const STAGE_TONE: Record<StageStatus, { mark: string; ring: string; word: string }> = {
  pending: { mark: "bg-raised text-muted", ring: "border-line", word: "waiting" },
  running: { mark: "bg-accent text-white animate-pulse", ring: "border-accent", word: "running" },
  ok: { mark: "bg-good text-black", ring: "border-line", word: "done" },
  escalated: { mark: "bg-warn text-black", ring: "border-warn", word: "escalated here" },
  error: { mark: "bg-bad text-white", ring: "border-bad", word: "error" },
};
const STAGE_GLYPH: Record<StageStatus, string> = { pending: "", running: "…", ok: "✓", escalated: "!", error: "✕" };

function StageRail({ state }: { state: RunState }) {
  return (
    <ol className="grid grid-cols-2 gap-2 sm:grid-cols-5">
      {STAGES.map((stage, i) => {
        const s = state.stages[stage];
        const tone = STAGE_TONE[s.status];
        return (
          <li key={stage} className={`flex items-center gap-2.5 rounded-lg border bg-surface px-3 py-2 ${tone.ring}`}>
            <span className={`grid size-6 shrink-0 place-items-center rounded-full text-xs font-bold ${tone.mark}`}>
              {STAGE_GLYPH[s.status] || i + 1}
            </span>
            <span className="min-w-0">
              <span className="block text-sm font-semibold">{STAGE_LABEL[stage]}</span>
              <span className="block truncate text-xs text-ink-2">
                {s.status === "pending" ? STAGE_HINT[stage] : tone.word}
                {s.durationMs !== undefined && ` · ${seconds(s.durationMs)}`}
              </span>
            </span>
          </li>
        );
      })}
    </ol>
  );
}

// Failure shows what triage found and what reproducing it showed.
function Failure({ state }: { state: RunState }) {
  const { triage, reproduce, run } = state;
  if (!triage) return null;
  const bad = reproduce.runs.filter((r) => !r.good);
  const good = reproduce.runs.find((r) => r.good);
  return (
    <Panel title="The failure" aside={run && `${run.branch || short(run.bad)}${run.dry_run ? " · dry run" : ""}`}>
      <div className="space-y-3 p-4">
        <div>
          <p className="font-medium">{triage.test_name}</p>
          <p className="text-xs text-ink-2">
            <span className="font-mono">{triage.test_file}</span>
            {triage.suspect_files.length > 0 && (
              <>
                {" "}
                · look first at <span className="font-mono">{triage.suspect_files.join(", ")}</span>
              </>
            )}
          </p>
        </div>
        <pre className="max-h-44 overflow-auto rounded-md border border-line bg-page px-3 py-2 font-mono text-xs leading-5 text-ink-2">
          {triage.excerpt}
        </pre>

        {reproduce.runs.length > 0 && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs">
            <span className="font-semibold">Reproduce</span>
            <span className="flex items-center gap-1" aria-label={`${bad.length} runs on the failing commit`}>
              {bad.map((r, i) => (
                <span
                  key={i}
                  title={`run ${r.attempt} on the failing commit: ${VERDICT[r.verdict].label}`}
                  className={`arrive grid size-4 place-items-center rounded-[3px] text-[9px] font-bold ${
                    r.verdict === "fail" ? "bg-bad text-white" : r.verdict === "pass" ? "bg-good text-black" : "bg-skip text-ink"
                  }`}
                >
                  {VERDICT[r.verdict].glyph}
                </span>
              ))}
            </span>
            <span className="text-ink-2">
              {reproduce.done
                ? `failed ${reproduce.done.failures} of ${reproduce.done.runs} runs on the failing commit`
                : "running the test on the failing commit"}
              {good && `; ${VERDICT[good.verdict].label} on the last good commit`}
            </span>
            {reproduce.done?.reproduced && <Mark tone="good">real and steady</Mark>}
            {reproduce.done?.flaky && <Mark tone="warn">flaky</Mark>}
          </div>
        )}
      </div>
    </Panel>
  );
}

function Locate({ state }: { state: RunState }) {
  const { locate } = state;
  if (!locate) return null;
  const rounds = locate.rounds.filter((r) => r.round > 0).length;
  const culprit = locate.culprit;
  return (
    <Panel
      title="Locate: parallel bisect"
      aside={`${locate.commits.length - 1} commits · ${locate.workers} tested at a time`}
    >
      <div className="space-y-4 p-4">
        <CommitGrid locate={locate} />
        <Narrowing locate={locate} />
        <p className="text-sm">
          {culprit ? (
            <>
              <span className="font-semibold">Culprit found</span> in {culprit.rounds} {culprit.rounds === 1 ? "round" : "rounds"}:{" "}
              {culprit.tested} of {locate.commits.length - 1} commits tested in {seconds(culprit.duration_ms)}. A one-at-a-time
              bisect needs about {Math.ceil(Math.log2(locate.commits.length - 1))} rounds.
            </>
          ) : (
            <span className="text-ink-2">
              Round {Math.max(rounds, 1)}: the failure starts somewhere in the {locate.hi - locate.lo} commits still marked.
            </span>
          )}
        </p>
        <TestedTable locate={locate} />
      </div>
    </Panel>
  );
}

// Outcome is the answer of the run: a delivered fix, or why there is none.
function Outcome({ state }: { state: RunState }) {
  const { escalated, deliver, agent, done } = state;
  if (!escalated && !deliver && !done) return null;
  const fixed = done?.outcome === "fixed" || (agent?.done?.outcome === "fixed" && !escalated);
  const link = deliver?.url;

  if (fixed) {
    return (
      <section className="arrive rounded-lg border-2 border-good bg-surface p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Mark tone="good">Fixed and verified</Mark>
          <h2 className="text-base font-semibold">{state.triage?.test_name}</h2>
        </div>
        {agent?.done && (
          <p className="mt-2 text-sm">
            <span className="font-semibold">Root cause:</span> {agent.done.root_cause}
          </p>
        )}
        <p className="mt-2 text-sm text-ink-2">
          {agent?.done && `Verified on attempt ${agent.done.attempts}, then checked again by heall in a fresh sandbox. `}
          {deliver?.outcome === "pr" && "A draft pull request is open for a person to review."}
          {deliver?.outcome === "patch_file" && "The patch and the report were saved; nothing was pushed."}
        </p>
        {link && (
          <a href={link} target="_blank" rel="noreferrer" className="mt-3 inline-block rounded-md bg-ink px-3 py-1.5 text-sm font-medium text-page hover:opacity-90">
            Open the draft pull request
          </a>
        )}
        {deliver?.path && <p className="mt-2 font-mono text-xs break-all text-muted">{deliver.path}</p>}
      </section>
    );
  }

  if (escalated) {
    return (
      <section className="arrive rounded-lg border-2 border-warn bg-surface p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Mark tone="warn">Escalated at {STAGE_LABEL[escalated.stage].toLowerCase()}</Mark>
          <h2 className="text-base font-semibold">No fix was delivered</h2>
        </div>
        <p className="mt-2 text-sm font-medium">{escalated.reason}</p>
        <p className="mt-2 max-h-64 overflow-auto text-sm whitespace-pre-wrap text-ink-2">{escalated.diagnosis}</p>
        <p className="mt-3 text-xs text-ink-2">
          heall only delivers a fix it can prove. It stopped here and wrote down why instead.
        </p>
        {link && (
          <a href={link} target="_blank" rel="noreferrer" className="mt-3 inline-block rounded-md bg-ink px-3 py-1.5 text-sm font-medium text-page hover:opacity-90">
            Open the diagnosis on the failing commit
          </a>
        )}
        {deliver?.path && <p className="mt-2 font-mono text-xs break-all text-muted">{deliver.path}</p>}
      </section>
    );
  }

  if (done?.outcome === "error") {
    return (
      <section className="rounded-lg border-2 border-bad bg-surface p-4">
        <Mark tone="bad">The run stopped on an error</Mark>
        <ul className="mt-2 space-y-1 text-sm text-ink-2">
          {state.notes.map((n, i) => (
            <li key={i}>{n.message}</li>
          ))}
        </ul>
      </section>
    );
  }
  return null;
}
