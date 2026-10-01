# Evaluation results

`scripts/evaluate.py --runs 3` on the demo repository: 5 scenarios, 3 live runs each,
model `openai/gpt-oss-120b`. Every run is the whole pipeline as a dry run.

| Metric | Result | Meaning |
|---|---|---|
| Culprit accuracy | **12 of 12** | runs that named the right culprit, of those that reached the search |
| Fix success rate | **9 of 9** | runs that delivered a verified fix, on the fixable bugs |
| Correct refusals | **6 of 6** | runs that escalated at the expected stage, on the bugs heall should not fix |
| Wrong fixes delivered | **0 of 6** | runs that delivered a fix where it should have refused |
| Attempts per fix | **1.00** | patches the agent submitted per verified fix, on average |
| Patches rejected or failed | **0** | patches from the model that the guardrails or the tests turned down |
| Runs that errored | **0 of 15** | runs that ended on an error rather than a fix or an escalation |

## Per scenario

| Scenario | Expected | Runs as expected | Culprit found | Median time | Attempts |
|---|---|---|---|---|---|
| `bug/off-by-one` | fixed | 3 of 3 | 3 of 3 | 9.4s | 1, 1, 1 |
| `change/price-format` | escalated at heal | 3 of 3 | 3 of 3 | 18.2s | none |
| `flaky/retry-jitter` | escalated at reproduce | 3 of 3 | not searched | 0.6s | none |
| `bug/slug-regex` | fixed | 3 of 3 | 3 of 3 | 17.7s | 1, 1, 1 |
| `bug/renamed-export` | fixed | 3 of 3 | 3 of 3 | 19.9s | 1, 1, 1 |

## What this does and does not show

- The sample is small: 5 seeded bugs in one small JavaScript repository, written by the same people who
  built the tool. It shows the pipeline works end to end; it is not a measure of how heall does on real projects.
- Times include waiting on the model's rate limit, which varies from run to run.
- The staged red-team patch (`--inject-bad-patch`) is not used here, so every rejected patch came from the model.
