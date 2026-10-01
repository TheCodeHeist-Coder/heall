"use client";

import { useState } from "react";

import type { AgentState, TimelineItem } from "@/lib/run-state";
import { useStickToBottom } from "@/lib/use-run";

import { Mark } from "./status";

function toolArgument(name: string, input: unknown): string {
  if (typeof input !== "object" || input === null) return "";
  const args = input as Record<string, unknown>;
  const key = { read_file: "path", list_files: "directory", search: "pattern", run_test: "name", escalate: "reason" }[name];
  const value = key ? args[key] : undefined;
  return typeof value === "string" ? value : "";
}

function Thought({ item }: { item: Extract<TimelineItem, { type: "thought" }> }) {
  const [open, setOpen] = useState(false);
  const long = item.text.length > 260;
  return (
    <div className={`arrive rounded-md border-l-2 py-1 pl-3 text-sm ${item.staged ? "border-warn" : "border-line-strong"}`}>
      {item.staged && (
        <div className="mb-1">
          <Mark tone="warn">Staged red-team check, not the model</Mark>
        </div>
      )}
      <p className={`whitespace-pre-wrap text-ink-2 ${long && !open ? "line-clamp-3" : ""}`}>{item.text}</p>
      {long && (
        <button type="button" onClick={() => setOpen(!open)} className="mt-0.5 text-xs text-accent hover:underline">
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}

function Tool({ item }: { item: Extract<TimelineItem, { type: "tool" }> }) {
  // A submitted patch and an escalation are shown by what they cause.
  if (item.name === "submit_patch" && item.ok !== false) return null;
  if (item.name === "submit_patch" && /^(failed|rejected)/.test(item.summary ?? "")) return null;
  if (item.name === "escalate") return null;
  const pending = item.ok === undefined;
  return (
    <div className="arrive flex items-baseline gap-2 font-mono text-xs">
      <span className={pending ? "text-muted" : item.ok ? "text-ink" : "text-ink"} aria-hidden>
        {pending ? "…" : item.ok ? "✓" : "!"}
      </span>
      <span className="font-semibold">{item.name}</span>
      <span className="min-w-0 flex-1 truncate text-ink-2">{toolArgument(item.name, item.input)}</span>
      <span className="shrink-0 text-muted">{pending ? "running" : item.summary}</span>
    </div>
  );
}

function Diff({ diff }: { diff: string }) {
  const lines = diff.replace(/\n$/, "").split("\n");
  return (
    <pre className="overflow-x-auto rounded-md border border-line bg-page py-1.5 font-mono text-xs leading-5">
      {lines.map((line, i) => {
        let style = "text-ink-2";
        if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff ") || line.startsWith("new file")) {
          style = "text-ink font-semibold";
        } else if (line.startsWith("+")) style = "bg-good-wash text-ink";
        else if (line.startsWith("-")) style = "bg-bad-wash text-ink";
        else if (line.startsWith("@@")) style = "text-muted";
        return (
          <span key={i} className={`block px-3 ${style}`}>
            {line || " "}
          </span>
        );
      })}
    </pre>
  );
}

function Patch({ item }: { item: Extract<TimelineItem, { type: "patch" }> }) {
  const { guardrails, verify } = item;
  const blocked = guardrails?.checks.filter((c) => !c.passed) ?? [];
  return (
    <div className="arrive rounded-lg border border-line bg-surface p-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold">Patch {item.attempt}</span>
        {item.staged && <Mark tone="warn">staged red-team patch</Mark>}
        <span className="flex-1" />
        {!verify && <Mark tone="idle">being verified</Mark>}
        {verify?.status === "verified" && <Mark tone="good">Verified</Mark>}
        {verify?.status === "rejected" && <Mark tone="bad">Rejected by a guardrail</Mark>}
        {verify?.status === "failed" && <Mark tone="bad">Failed verification</Mark>}
      </div>
      <Diff diff={item.diff} />

      {guardrails && (
        <div className="mt-2.5 text-xs">
          {guardrails.passed ? (
            <p className="text-ink-2">
              <span className="font-semibold text-ink">Guardrails:</span> all {guardrails.checks.length} checks passed (
              {guardrails.checks.map((c) => c.name.replaceAll("_", " ")).join(", ")}).
            </p>
          ) : (
            <ul className="space-y-1">
              {blocked.map((c) => (
                <li key={c.name} className="rounded-md bg-bad-wash px-2.5 py-1.5">
                  <span className="font-semibold">✕ Blocked by {c.name.replaceAll("_", " ")}:</span> {c.detail}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {verify && verify.status !== "rejected" && (
        <p className="mt-1.5 text-xs text-ink-2">
          <span className="font-semibold text-ink">Tests:</span>{" "}
          {verify.status === "verified"
            ? "the failing test passes and nothing else broke."
            : verify.new_failures.length > 0
              ? `fixes the target but breaks ${verify.new_failures.length} other test(s): ${verify.new_failures.join("; ")}.`
              : verify.target_passed
                ? "the full suite did not pass."
                : "the failing test still fails."}
        </p>
      )}
      {verify?.status === "rejected" && <p className="mt-1.5 text-xs text-ink-2">The patch was never applied or run.</p>}
    </div>
  );
}

export function AgentTimeline({ agent, following }: { agent: AgentState; following: boolean }) {
  const { ref, onScroll } = useStickToBottom<HTMLDivElement>(following ? agent.items.length : -1);
  return (
    <div ref={ref} onScroll={onScroll} className="max-h-[calc(100vh-15rem)] min-h-40 space-y-2.5 overflow-y-auto p-4">
      {agent.items.map((item) => {
        switch (item.type) {
          case "thought":
            return <Thought key={item.seq} item={item} />;
          case "tool":
            return <Tool key={item.seq} item={item} />;
          case "patch":
            return <Patch key={item.seq} item={item} />;
          case "log":
            return (
              <p key={item.seq} className="arrive text-xs text-muted">
                {item.level === "info" ? "" : `${item.level}: `}
                {item.message}
              </p>
            );
        }
      })}
      {agent.items.length === 0 && <p className="text-sm text-muted">The agent has started.</p>}
    </div>
  );
}
