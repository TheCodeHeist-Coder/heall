# heall contracts

heall has three parts in three languages. This folder is what they agree on.
When a contract changes, change it here first, then in all three mirrors.

| Part | Language | Role | Mirror of the contract |
|---|---|---|---|
| `cli/` | Go | Runs the pipeline; owns the sandbox, bisect and guardrails | `cli/internal/events`, `cli/internal/agentio`, `cli/internal/config` |
| `agent/` | Python | The heal stage: an LLM tool loop that proposes a fix or escalates | `agent/heall_agent/contracts.py`, `agent/heall_agent/events.py` |
| `web/` | Next.js | Live dashboard; reads the event stream | `web/lib/events.ts` |

`examples/` holds one sample of every message. The test suite of each part
parses those same files strictly, so a field renamed in one language fails the
tests of that language. Run `make test` from the repo root.

## 1. Event stream

Everything the pipeline does is reported as an event: one JSON object per
line (JSONL). The CLI writes the stream to a file and serves it to the
dashboard; a saved file can be replayed later.

```json
{"v":1,"seq":10,"ts":"2026-10-01T10:00:06.300Z","run_id":"r-demo","stage":"locate","kind":"commit_tested","data":{"sha":"b2c3d4e","round":1,"worker":0,"verdict":"fail","duration_ms":1000}}
```

| Field | Meaning |
|---|---|
| `v` | Contract version, currently `1` |
| `seq` | Starts at 1 and increases by 1; gives a total order within a run |
| `ts` | UTC time, RFC 3339 |
| `run_id` | Same for every event of one run |
| `stage` | `run`, `triage`, `reproduce`, `locate`, `heal` or `deliver` |
| `kind` | One of the kinds below |
| `data` | Payload for that kind; every listed field is always present |

A verdict is `pass`, `fail`, `flaky` or `skipped` (did not build or timed out).
A commit is `{sha, subject, author, date}`.

| Kind | Payload fields | Notes |
|---|---|---|
| `run_started` | `repo, branch, good, bad, dry_run` | First event |
| `stage_started` | none | The stage is in the envelope |
| `stage_done` | `status, duration_ms` | Status: `ok`, `escalated`, `error` |
| `triage_done` | `test_name, test_file, suspect_files, excerpt` | |
| `reproduce_run` | `sha, attempt, verdict, duration_ms` | One per run of the failing test |
| `reproduce_done` | `reproduced, flaky, runs, failures, good_passes` | `runs` counts completed runs on the bad commit; the stage stops early once the answer is known |
| `locate_started` | `commits, workers` | Commits oldest first: first is known good, last is bad |
| `round_started` | `round, lo, hi, probes` | `lo`/`hi` index into `commits`: newest known good, oldest known bad |
| `commit_testing` | `sha, round, worker` | A worker picked the commit up |
| `commit_tested` | `sha, round, worker, verdict, duration_ms` | |
| `round_done` | `round, lo, hi` | The narrowed range |
| `culprit_found` | `commit, diff, rounds, tested, duration_ms` | |
| `agent_started` | `model, max_attempts` | |
| `agent_thought` | `text` | The agent's reasoning between tool calls |
| `tool_call` | `id, name, input` | |
| `tool_result` | `id, name, ok, summary` | `id` matches the call |
| `patch_submitted` | `attempt, diff` | |
| `guardrail_checked` | `attempt, passed, checks` | Each check is `{name, passed, detail}` |
| `verify_done` | `attempt, status, target_passed, suite_passed, new_failures` | Status below |
| `agent_done` | `outcome, attempts, root_cause, patch, reason` | Outcome: `fixed`, `escalated` |
| `deliver_done` | `outcome, url, path` | Outcome: `pr`, `patch_file`, `diagnosis` |
| `escalated` | `reason, diagnosis` | Any stage may stop the pipeline with this |
| `run_done` | `outcome, duration_ms` | Last event. Outcome: `fixed`, `escalated`, `error` |
| `log` | `level, message` | Level: `info`, `warn`, `error` |

Verify status:

- `verified`: guardrails passed, the target test passes, and the full suite has
  no failures that were not already failing at the bad commit.
- `rejected`: a guardrail blocked the patch; it was never applied.
- `failed`: the patch did not apply, or tests still fail.

## 2. CLI to agent

The CLI starts the agent once per heal stage:

```
python -m heall_agent heal --request <path to HealRequest JSON>
```

`examples/heal_request.json` shows the request: the failing test and its
output, the culprit commit and diff, the allow and protect lists, the attempt
limit, the model, and a `worktree` checked out at the bad commit for reading.

The agent replies on **stdout** with event lines that carry only `kind` and
`data`. The CLI validates each one, adds the envelope and relays it:

```json
{"kind": "agent_thought", "data": {"text": "The culprit swapped ceil for floor."}}
```

- The agent may emit only `agent_started`, `agent_thought`, `tool_call`,
  `tool_result`, `patch_submitted`, `guardrail_checked`, `verify_done`,
  `agent_done` and `log`.
- The last line is always `agent_done`. That payload is the result.
- **stderr** is free-form logging.
- Exit code 0 means the agent finished, whether it fixed or escalated. Any
  other exit code is a crash, and the CLI escalates.

## 3. Agent to CLI

The agent cannot run tests or accept its own patch. It calls back into the CLI,
which prints one JSON object on stdout:

```
heall _runtest --request <HealRequest> [--test <name>]
heall _verify  --request <HealRequest> --patch <diff file> --attempt <n>
```

- `_runtest` runs the suite, or one test, at the bad commit in the sandbox.
  Reply: `examples/runtest_result.json`.
- `_verify` checks a patch against the guardrails, applies it to a fresh
  worktree and runs the tests. Reply: `examples/verify_result.json`. The agent
  reports the reply as `guardrail_checked` and `verify_done` events.

Two rules keep the trust layer out of the agent's hands:

1. Guardrails and test runs live in Go. Edits the agent makes in its worktree
   are ignored; only a patch that went through `_verify` counts.
2. Before delivering, the CLI verifies the winning patch again itself. It does
   not take the agent's `agent_done` at its word.

## 4. Exit codes

Every `heall` command uses the same exit codes:

| Code | Meaning |
|---|---|
| 0 | The stage succeeded and the pipeline can continue |
| 1 | An error: bad arguments, Docker not running, a git failure |
| 3 | Escalated: heall stopped on purpose because it could not prove something, and said why in an `escalated` event |

## 5. Repo config

Each repository being healed has a `.heall.yaml` (`examples/heall.yaml`). It
holds the build and test commands and the allow and protect lists, so the CLI
has no knowledge of the repository's language.

- `build_cmd` must exit non-zero when a commit cannot be built. Bisect marks
  such commits `skipped` and routes around them.
- `test_one_cmd` runs a single test. `{{test}}` is replaced by the test name,
  `{{test_re}}` by the name escaped for use in a regular expression.
- A patch may touch only paths matching `allow`, and never a path matching
  `protect`, even if it is also allowed.
