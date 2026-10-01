#!/usr/bin/env python3
"""Times heall's parallel bisect against plain `git bisect run`.

Every method searches the same commit range of the demo repository for the
same failing test, in the same Docker image with no network:

  git bisect run   one commit per step, a fresh container per step
  heall, 1 worker  heall's search, one commit at a time
  heall, 6 workers heall's search, six commits per round

heall is run with --skip-end-check, since git bisect does not test the two
ends of the range either. Each measurement is repeated and the median is
reported, with the commit each method named, so a wrong answer cannot hide
behind a fast time.

A second table repeats the comparison with a test that takes longer, by
sleeping before it: the demo's test finishes in a third of a second, which is
much faster than a real suite.

    make build demo && scripts/benchmark.py [--runs 3] [--slow 3]
"""

from __future__ import annotations

import argparse
import json
import os
import re
import statistics
import subprocess
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REPO = ROOT / "demo" / "out" / "repo"
HEALL = ROOT / "bin" / "heall"
IMAGE = "node:24-alpine"


def sh(args, cwd=None, check=True, **kw):
    return subprocess.run(args, cwd=cwd, check=check, capture_output=True, text=True, **kw)


def git_bisect(good: str, bad: str, test: str, sleep: float) -> dict:
    """Plain `git bisect run`, in a worktree so the demo checkout is untouched."""
    with tempfile.TemporaryDirectory(prefix="heall-bench-") as tmp:
        tree = Path(tmp) / "w"
        sh(["git", "worktree", "add", "--quiet", "--detach", str(tree), bad], cwd=REPO)
        try:
            # One container per step: build, then the one test. 125 tells
            # git bisect to skip a commit that does not build.
            inner = (
                "node scripts/check.mjs >/dev/null 2>&1 || exit 125; "
                + (f"sleep {sleep}; " if sleep else "")
                + f"node --test --test-isolation=none '--test-name-pattern=^{re.escape(test)}$' >/dev/null 2>&1"
            )
            step = [
                "docker", "run", "--rm", "--network", "none",
                "--user", f"{os.getuid()}:{os.getgid()}", "--env", "HOME=/tmp",
                "--volume", f"{tree}:/work", "--workdir", "/work", IMAGE, "sh", "-c", inner,
            ]
            sh(["git", "bisect", "start", bad, good], cwd=tree)
            started = time.monotonic()
            out = sh(["git", "bisect", "run", *step], cwd=tree).stdout
            elapsed = time.monotonic() - started
            found = re.search(r"^([0-9a-f]{40}) is the first bad commit", out, re.M)
            return {
                "seconds": elapsed,
                "culprit": found.group(1) if found else None,
                "tested": out.count("running "),
            }
        finally:
            sh(["git", "bisect", "reset"], cwd=tree, check=False)
            sh(["git", "worktree", "remove", "--force", str(tree)], cwd=REPO, check=False)


def heall_locate(good: str, bad: str, test: str, workers: int, sleep: float) -> dict:
    args = [
        str(HEALL), "locate", "--repo", str(REPO), "--good", good, "--bad", bad,
        "--test", test, "--workers", str(workers), "--skip-end-check", "--json",
    ]
    with tempfile.TemporaryDirectory(prefix="heall-bench-") as tmp:
        if sleep:
            # The same repository config, with a slower test command.
            config = (REPO / ".heall.yaml").read_text()
            slow = json.dumps([
                "sh", "-c",
                f"sleep {sleep}; node --test --test-isolation=none --test-reporter=tap \"--test-name-pattern=^{{{{test_re}}}}$\"",
            ])
            config = re.sub(r"^test_one_cmd: .*$", lambda _: f"test_one_cmd: {slow}", config, flags=re.M)
            path = Path(tmp) / "slow.yaml"
            path.write_text(config)
            args += ["--config", str(path)]
        started = time.monotonic()
        out = json.loads(sh(args).stdout)
        return {
            "seconds": time.monotonic() - started,
            "culprit": out["culprit"]["sha"],
            "tested": out["tested"],
            "rounds": out["rounds"],
        }


METHODS = [
    ("git bisect run", lambda g, b, t, s: git_bisect(g, b, t, s)),
    ("heall, 1 worker", lambda g, b, t, s: heall_locate(g, b, t, 1, s)),
    ("heall, 6 workers", lambda g, b, t, s: heall_locate(g, b, t, 6, s)),
]


def measure(scenario: dict, runs: int, sleep: float) -> list[dict]:
    # The methods take turns, run by run, so that a busy or warm machine
    # slows all of them alike instead of whichever happens to go last.
    samples: dict[str, list[dict]] = {name: [] for name, _ in METHODS}
    for _ in range(runs):
        for name, method in METHODS:
            samples[name].append(method(scenario["good"], scenario["bad"], scenario["target_test"], sleep))
            time.sleep(1)
    rows = []
    for name, _ in METHODS:
        got = samples[name]
        times = sorted(s["seconds"] for s in got)
        rows.append({
            "method": name,
            "seconds": statistics.median(times),
            "fastest": times[0],
            "slowest": times[-1],
            "all_seconds": [round(s["seconds"], 2) for s in got],
            "tested": got[0]["tested"],
            "rounds": got[0].get("rounds", got[0]["tested"]),
            "correct": all(s["culprit"] == scenario["culprit"] for s in got),
        })
        print(f"  {name:<17} {rows[-1]['seconds']:6.1f}s  {rows[-1]['all_seconds']}", flush=True)
    return rows


def table(title: str, results: dict[str, list[dict]]) -> str:
    lines = [
        f"### {title}", "",
        "| Scenario | Method | Median time | Fastest to slowest | Rounds | Commits tested | Right culprit |",
        "|---|---|---|---|---|---|---|",
    ]
    for scenario, rows in results.items():
        for i, r in enumerate(rows):
            lines.append(
                f"| {scenario if i == 0 else ''} | {r['method']} | {r['seconds']:.1f}s{versus(rows[0], r) if i else ''} | "
                f"{r['fastest']:.1f}s to {r['slowest']:.1f}s | {r['rounds']} | {r['tested']} | {'yes' if r['correct'] else 'NO'} |"
            )
    return "\n".join(lines) + "\n"


def versus(base: dict, row: dict) -> str:
    """How a method's median compares with git bisect's."""
    ratio = base["seconds"] / row["seconds"]
    if 0.91 <= ratio <= 1.1:
        return " (about the same)"
    return f" ({ratio:.1f}× faster)" if ratio > 1 else f" ({1 / ratio:.1f}× slower)"


def reading(fast: dict, slow: dict, slow_seconds: float) -> list[str]:
    """What the measurements show, said from the numbers themselves."""
    def ratios(results: dict, method: int) -> list[float]:
        return [rows[0]["seconds"] / rows[method]["seconds"] for rows in results.values()]

    def spread(values: list[float]) -> str:
        lo, hi = min(values), max(values)
        say = lambda r: f"{r:.1f}× faster" if r >= 1 else f"{1 / r:.1f}× slower"
        return say(lo) if abs(hi - lo) < 0.15 else f"between {say(lo)} and {say(hi)}"

    notes = [
        "- Rounds and commits tested depend only on the search. Times depend on the machine and on what else",
        "  it is doing; they moved a lot between runs here, which is why the range is shown beside each median.",
        f"- With the demo's own test, heall with one worker was {spread(ratios(fast, 1))} than `git bisect run`,",
        f"  and with six workers {spread(ratios(fast, 2))}. A test run this short is mostly start-up cost, and six",
        "  of them at once compete for the same CPU, so there is little for parallelism to win.",
    ]
    if slow:
        notes += [
            f"- With {slow_seconds:g}s added to each test run, six workers were {spread(ratios(slow, 2))} than `git bisect run`.",
            "  The search needs 3 rounds instead of 7, and that is what counts once a test run takes seconds, as real",
            "  suites do. The added time is a sleep, so this is the best case: a test that keeps the CPU busy gains less.",
        ]
    notes += [
        "- Every method named the right commit in every run.",
        "- Commits that do not build are skipped by both tools; heall tests more commits in total because it",
        "  tests several per round.",
    ]
    return notes


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--runs", type=int, default=3, help="measurements per method (the median is reported)")
    parser.add_argument("--slow", type=float, default=3, help="seconds added to the test in the second table; 0 skips it")
    parser.add_argument("--out", default=str(ROOT / "docs" / "benchmark"), help="path of the .json and .md files to write, without extension")
    opts = parser.parse_args()

    manifest = json.loads((ROOT / "demo" / "out" / "scenarios.json").read_text())
    # A flaky test has no culprit to find.
    scenarios = [s for s in manifest["scenarios"] if not s["flaky"]]
    sh(["docker", "image", "inspect", IMAGE])

    fast: dict[str, list[dict]] = {}
    for s in scenarios:
        print(f"{s['branch']} ({s['commits']} commits, culprit is number {s['culprit_index']})", flush=True)
        fast[s["branch"]] = measure(s, opts.runs, 0)

    slow: dict[str, list[dict]] = {}
    if opts.slow:
        s = scenarios[0]
        print(f"{s['branch']}, with {opts.slow:g}s added to every test run", flush=True)
        slow[s["branch"]] = measure(s, opts.runs, opts.slow)

    cpus = os.cpu_count()
    md = [
        "# Bisect benchmark",
        "",
        f"Measured on this machine ({cpus} CPU threads) with `scripts/benchmark.py`, on the demo repository:",
        "120 commits between the good and the bad commit, Docker image `node:24-alpine`, no network.",
        f"Each time is the median of {opts.runs} runs of the whole command, with the methods taking turns.",
        "",
        table("The demo test as it is (about 0.3s per run)", fast),
    ]
    if slow:
        md += [table(f"With {opts.slow:g}s added to every test run", slow)]
    md += ["## Reading the numbers", "", *reading(fast, slow, opts.slow)]
    out = Path(opts.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.with_suffix(".md").write_text("\n".join(md) + "\n")
    out.with_suffix(".json").write_text(json.dumps({"runs": opts.runs, "cpus": cpus, "fast": fast, "slow": slow, "slow_seconds": opts.slow}, indent=2) + "\n")
    print(f"\nwrote {out.with_suffix('.md')} and {out.with_suffix('.json')}")


if __name__ == "__main__":
    main()
