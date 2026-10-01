# Demo repository

`generate.mjs` builds the repository heall is demonstrated on: a small
JavaScript library called shopkit, with no npm dependencies, tested with
`node --test`.

```
make demo                              # or: node demo/generate.mjs
node demo/generate.mjs --verify all    # check every commit (about 5 minutes)
```

Output goes to `demo/out/`, which is not committed:

| Path | Contents |
|---|---|
| `out/repo` | The git repository |
| `out/logs/<scenario>.log` | Test output at the tip of each scenario branch, as CI would show it |
| `out/scenarios.json` | Ground truth: good, bad and culprit commits, and the expected outcome |

The history is deterministic, so commit hashes are the same on every run, and
a remote set on the repository is kept when it is rebuilt. New scenarios are
only ever added at the end of the list in `generate.mjs`: a branch's hashes
depend on the branches built before it, and earlier ones may already be pushed.

## Branches

`main` has 7 commits and a green test suite. It is the known-good commit for
every scenario. Each scenario branch adds 120 commits on top of it.

| Branch | What went wrong | What heall should do |
|---|---|---|
| `bug/off-by-one` | Commit 53 refactors `paginate()` to use a new `bounds()` helper and drops the last item of every page. Commits 17 and 68 have syntax errors (each fixed by the next commit); a 6-worker bisect lands on both in its first round. | Bisect to commit 53, skipping the broken commits, then fix and open a PR. |
| `change/price-format` | Commit 62 changes `formatPrice()` from `$12.50` to `12.50 USD` on purpose and updates its tests, but a test in `test/cart.test.js` still asserts the old format. | Bisect to commit 62, then escalate: two tests now demand different results from the same call, so only editing a test can fix it. |
| `flaky/retry-jitter` | Commit 67 adds a test that depends on `Math.random()` and fails about half the time. | Stop at Reproduce and escalate as flaky, without bisecting. |
| `bug/slug-regex` | Commit 75 tidies `slugify()` and drops the `+` from its pattern, so every unsafe character becomes its own dash. | Bisect to commit 75, then fix and open a PR. |
| `bug/renamed-export` | Commit 82 renames `percentOff` to `applyPercent` and updates its own test, but `cart.js` still calls the old name, which fails at run time. | Bisect to commit 82, then fix and open a PR. |

Two details make `bug/off-by-one` a fair test of the agent:

- The obvious fix, changing `bounds()` to return an exclusive end, makes the
  failing test pass but breaks three others. The right fix is one line in
  `paginate()`.
- A later commit (`hasNextPage`) builds on `bounds()`, so reverting the
  culprit is not a clean fix either.

## Checks

The generator checks its own output with the commands from the repository's
`.heall.yaml`. By default it checks the commits that matter: the first and
last of each branch, the culprit and its parent, and the broken commits. With
`--verify all` it checks every commit:

- before the culprit, the whole suite passes;
- from the culprit on, the target test fails and every other test passes;
- broken commits fail the build command.

For the flaky branch it runs the target test 40 times at the tip and requires
a mix of passes and failures.
