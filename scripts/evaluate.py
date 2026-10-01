#!/usr/bin/env python3
"""Runs heall on every scenario of the demo repository and scores it.

Each scenario in demo/out/scenarios.json says what should happen: which
commit is the culprit, and whether heall should deliver a fix or escalate
(and at which stage). This script runs the whole pipeline live, with the real
model, several times per scenario, and reports only what it observed.

Nothing is pushed or posted: every run is a dry run.

    make build demo && scripts/evaluate.py [--runs 3]

It needs a Groq key (see .env.example). The free tier allows few tokens per
minute, so the runs wait on the rate limit between them.
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REPO = ROOT / "demo" / "out" / "repo"
HEALL = ROOT / "bin" / "heall"


def run_once(scenario: dict, out: Path, record: Path | None) -> dict:
    env = dict(os.environ)
    if record:
        env["HEALL_LLM_RECORD"] = str(record)
    proc = subprocess.run(
        [
            str(HEALL), "run", "--repo", str(REPO), "--good", scenario["good"], "--bad", scenario["branch"],
            "--log", str(ROOT / "demo" / "out" / scenario["log"]), "--dry-run", "--json", "--out", str(out),
        ],
        capture_output=True, text=True, env=env, cwd=ROOT,
    )
    if proc.returncode not in (0, 3):
        return {"error": (proc.stderr.strip().splitlines() or ["unknown error"])[-1]}
    summary = json.loads(proc.stdout)
    events = [json.loads(line) for line in (out / "events.jsonl").read_text().splitlines() if line.strip()]
    verifies = [e["data"] for e in events if e["kind"] == "verify_done"]
    usage = [e["data"]["message"] for e in events if e["kind"] == "log" and e["data"]["message"].startswith("model usage")]
    return {
        "outcome": summary["outcome"],
        "stage": summary.get("stage", ""),
        "culprit": (summary.get("culprit") or {}).get("sha"),
        "rounds": summary.get("rounds", 0),
        "attempts": summary.get("attempts", 0),
        "seconds": summary["duration_ms"] / 1000,
        "rejected": sum(v["status"] == "rejected" for v in verifies),
        "failed": sum(v["status"] == "failed" for v in verifies),
        "usage": usage[0] if usage else "",
        "reason": summary.get("reason", ""),
    }


def judge(scenario: dict, run: dict) -> dict:
    """What the run got right, against the scenario's ground truth."""
    if "error" in run:
        return {"ok": False, "culprit_ok": None}
    located = run["culprit"] is not None
    culprit_ok = run["culprit"] == scenario["culprit"] if located else None
    if scenario["expected"] == "fixed":
        ok = run["outcome"] == "fixed" and culprit_ok is True
    else:
        ok = run["outcome"] == "escalated" and run["stage"] == scenario["escalate_stage"]
    return {"ok": ok, "culprit_ok": culprit_ok}


def fraction(n: int, d: int) -> str:
    return f"{n} of {d}" if d else "n/a"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--runs", type=int, default=3, help="runs per scenario")
    parser.add_argument("--out", default=str(ROOT / "docs" / "results"), help="path of the .json and .md files to write, without extension")
    parser.add_argument("--record", action="store_true", help="save the model's replies of each scenario's first run in demo/recordings/")
    opts = parser.parse_args()

    manifest = json.loads((ROOT / "demo" / "out" / "scenarios.json").read_text())
    results = []
    with tempfile.TemporaryDirectory(prefix="heall-eval-") as tmp:
        for scenario in manifest["scenarios"]:
            print(f"{scenario['branch']}: expect {scenario['expected']}" + (f" at {scenario['escalate_stage']}" if scenario["escalate_stage"] else ""), flush=True)
            runs = []
            for n in range(opts.runs):
                record = None
                recording = ROOT / "demo" / "recordings" / f"{scenario['name']}.jsonl"
                # An existing recording is kept: it may have been made with
                # other flags, such as --inject-bad-patch.
                if opts.record and n == 0 and not scenario["flaky"] and not recording.exists():
                    record = recording
                run = run_once(scenario, Path(tmp) / f"{scenario['name']}-{n}", record)
                run.update(judge(scenario, run))
                runs.append(run)
                what = run.get("error") or f"{run['outcome']}{' at ' + run['stage'] if run['stage'] else ''}, {run['seconds']:.1f}s, attempts {run['attempts']}"
                print(f"  run {n + 1}: {'ok  ' if run['ok'] else 'MISS'} {what}", flush=True)
            results.append({"scenario": scenario, "runs": runs})

    every = [(r["scenario"], run) for r in results for run in r["runs"]]
    good = [(s, run) for s, run in every if "error" not in run]
    located = [(s, run) for s, run in good if run["culprit_ok"] is not None]
    fixable = [(s, run) for s, run in good if s["expected"] == "fixed"]
    refusable = [(s, run) for s, run in good if s["expected"] == "escalated"]
    fixes = [run for _, run in fixable if run["outcome"] == "fixed"]

    metrics = [
        ("Culprit accuracy", fraction(sum(run["culprit_ok"] for _, run in located), len(located)), "runs that named the right culprit, of those that reached the search"),
        ("Fix success rate", fraction(len(fixes), len(fixable)), "runs that delivered a verified fix, on the fixable bugs"),
        ("Correct refusals", fraction(sum(run["ok"] for _, run in refusable), len(refusable)), "runs that escalated at the expected stage, on the bugs heall should not fix"),
        ("Wrong fixes delivered", fraction(sum(run["outcome"] == "fixed" for _, run in refusable), len(refusable)), "runs that delivered a fix where it should have refused"),
        ("Attempts per fix", f"{statistics.mean(run['attempts'] for run in fixes):.2f}" if fixes else "n/a", "patches the agent submitted per verified fix, on average"),
        ("Patches rejected or failed", str(sum(run["rejected"] + run["failed"] for _, run in good)), "patches from the model that the guardrails or the tests turned down"),
        ("Runs that errored", fraction(len(every) - len(good), len(every)), "runs that ended on an error rather than a fix or an escalation"),
    ]

    md = [
        "# Evaluation results",
        "",
        f"`scripts/evaluate.py --runs {opts.runs}` on the demo repository: {len(results)} scenarios, {opts.runs} live runs each,",
        f"model `{os.environ.get('HEALL_MODEL', 'openai/gpt-oss-120b')}`. Every run is the whole pipeline as a dry run.",
        "",
        "| Metric | Result | Meaning |",
        "|---|---|---|",
        *[f"| {name} | **{value}** | {meaning} |" for name, value, meaning in metrics],
        "",
        "## Per scenario",
        "",
        "| Scenario | Expected | Runs as expected | Culprit found | Median time | Attempts |",
        "|---|---|---|---|---|---|",
    ]
    for r in results:
        s, runs = r["scenario"], [run for run in r["runs"] if "error" not in run]
        expected = "fixed" if s["expected"] == "fixed" else f"escalated at {s['escalate_stage']}"
        searched = [run for run in runs if run["culprit_ok"] is not None]
        md.append(
            f"| `{s['branch']}` | {expected} | {fraction(sum(run['ok'] for run in runs), len(r['runs']))} | "
            f"{fraction(sum(run['culprit_ok'] for run in searched), len(searched)) if searched else 'not searched'} | "
            f"{statistics.median(run['seconds'] for run in runs):.1f}s | "
            f"{', '.join(str(run['attempts']) for run in runs) if s['expected'] == 'fixed' else 'none'} |"
            if runs
            else f"| `{s['branch']}` | {expected} | 0 of {len(r['runs'])} | | | |"
        )
    md += [
        "",
        "## What this does and does not show",
        "",
        f"- The sample is small: {len(results)} seeded bugs in one small JavaScript repository, written by the same people who",
        "  built the tool. It shows the pipeline works end to end; it is not a measure of how heall does on real projects.",
        "- Times include waiting on the model's rate limit, which varies from run to run.",
        "- The staged red-team patch (`--inject-bad-patch`) is not used here, so every rejected patch came from the model.",
    ]
    out = Path(opts.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.with_suffix(".md").write_text("\n".join(md) + "\n")
    out.with_suffix(".json").write_text(json.dumps({"runs": opts.runs, "metrics": {n: v for n, v, _ in metrics}, "results": results}, indent=2) + "\n")
    print()
    for name, value, _ in metrics:
        print(f"{name:<28} {value}")
    print(f"\nwrote {out.with_suffix('.md')} and {out.with_suffix('.json')}")


if __name__ == "__main__":
    main()
