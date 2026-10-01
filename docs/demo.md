# Demo guide

A three-minute demo of heall on the demo repository, with a fallback for
every part that depends on the network.

## Before you present

```
make build web demo     # once; "make demo" keeps the GitHub remote if one is set
scripts/preflight.sh    # checks Docker, the image, the key, the recordings, the port
```

Then set the screen up:

1. Terminal A, in the project root: `make serve`. Leave it running.
2. Browser: `http://localhost:7777`. Pick the theme that reads best on the
   projector with "Switch theme".
3. Browser, second tab: the Actions page of the demo repository on GitHub,
   showing `main` green and the scenario branches red.
4. Terminal B, in the project root, with a large font. This is where you type.

Close any open heall pull request on the demo repository first, so the one
you open on stage is the only one.

## The script

**0:00 The problem.** Show the Actions tab. "CI is red on this branch. 120
commits went in since it was last green. Someone now has to find which one
broke it, work out why, and fix it."

**0:20 Run heall.** In terminal B:

```
bin/heall run --repo demo/out/repo --good main --bad bug/off-by-one \
  --log demo/out/logs/off-by-one.log --inject-bad-patch
```

Switch to the dashboard as soon as it starts.

**0:30 Locate.** Point at the commit grid. "It tests six commits at a time,
each in its own sandbox. Three rounds instead of seven. The grey ones do not
even build; it routes around them." The culprit gets a ring: commit 53.

**1:00 Guardrails.** Point at patch 1 in the agent panel. "This first patch
is staged by us, and labelled so: it skips the failing test instead of fixing
it. The guardrails reject it before it is ever run. The agent cannot make a
test pass by editing the test."

**1:20 The fix.** Patch 2 is the model's. "One line. heall applied it to a
clean checkout, ran the failing test and the whole suite in a fresh
container, then checked it a second time itself."

**1:40 The pull request.** Click "Open the draft pull request". Show the root
cause, the culprit, the patch and the evidence. "It is a draft. A person
still merges it."

**2:00 Refusing.** Back in terminal B:

```
bin/heall run --repo demo/out/repo --good main --bad change/price-format
```

"Here the commit changed behaviour on purpose and one test was left behind.
Two tests now demand different results from the same call. The only fix is to
edit a test, which heall will not do. So it stops and says why." Show the
result card, then the comment it posted on the failing commit.

**2:40 Numbers.** Show the tables in `docs/results.md` and
`docs/benchmark.md`. Say only what was measured.

**2:55 Close.** "It finds the commit, proves the fix, and refuses when it
cannot."

## If something fails on stage

| What fails | What to do |
|---|---|
| The network or the model | Add `HEALL_LLM_REPLAY=demo/recordings/off-by-one.jsonl` (or `outdated-test.jsonl`) in front of the command. The model's replies come from a recording of a real run; everything else still runs for real. Keep `--inject-bad-patch` on the first command, since that recording was made with it. |
| GitHub, or `gh` | Add `--dry-run`. The run ends the same way and saves the patch and report under `.heall/`. |
| Docker | Add `--sandbox local`. Say that isolation is off. |
| The live run altogether | In the dashboard, pick "Fixed: off-by-one bug" from the list and press Replay. It plays a recorded real run with its real pacing, with no server needed. |
| Everything | Play the backup video. |

## Other things worth showing if there is time

- `bin/heall run --repo demo/out/repo --good main --bad flaky/retry-jitter`:
  a flaky test is caught in Reproduce in about two seconds, before any
  bisecting, with the `Math.random` lines named in the diagnosis.
- `bin/heall run --repo demo/out/repo --good main --bad bug/slug-regex --dry-run`
  and the same for `bug/renamed-export`: two more bugs it fixes.
- `bin/heall locate --repo demo/out/repo --good main --bad bug/off-by-one --test "paginate returns a full page" --workers 1`:
  the same search one commit at a time, to compare.

## After the demo

Each real run leaves a `heall/fix-…` branch and a draft pull request on the
demo repository. Close the pull request and delete the branch before the next
rehearsal.
