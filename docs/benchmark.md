# Bisect benchmark

Measured on this machine (12 CPU threads) with `scripts/benchmark.py`, on the demo repository:
120 commits between the good and the bad commit, Docker image `node:24-alpine`, no network.
Each time is the median of 5 runs of the whole command, with the methods taking turns.

### The demo test as it is (about 0.3s per run)

| Scenario | Method | Median time | Fastest to slowest | Rounds | Commits tested | Right culprit |
|---|---|---|---|---|---|---|
| bug/off-by-one | git bisect run | 7.4s | 2.7s to 13.5s | 7 | 7 | yes |
|  | heall, 1 worker | 7.8s (about the same) | 2.9s to 9.3s | 7 | 7 | yes |
|  | heall, 6 workers | 7.0s (about the same) | 4.5s to 8.5s | 3 | 15 | yes |
| change/price-format | git bisect run | 8.6s | 5.7s to 15.8s | 7 | 7 | yes |
|  | heall, 1 worker | 9.3s (about the same) | 3.8s to 14.6s | 7 | 7 | yes |
|  | heall, 6 workers | 6.4s (1.3× faster) | 6.2s to 9.7s | 3 | 14 | yes |
| bug/slug-regex | git bisect run | 3.5s | 3.3s to 9.0s | 7 | 7 | yes |
|  | heall, 1 worker | 2.7s (1.3× faster) | 2.5s to 3.7s | 7 | 7 | yes |
|  | heall, 6 workers | 4.6s (1.3× slower) | 3.9s to 5.5s | 3 | 14 | yes |
| bug/renamed-export | git bisect run | 4.4s | 3.0s to 10.6s | 7 | 7 | yes |
|  | heall, 1 worker | 3.0s (1.5× faster) | 2.5s to 5.4s | 7 | 7 | yes |
|  | heall, 6 workers | 3.4s (1.3× faster) | 2.8s to 9.0s | 3 | 13 | yes |

### With 3s added to every test run

| Scenario | Method | Median time | Fastest to slowest | Rounds | Commits tested | Right culprit |
|---|---|---|---|---|---|---|
| bug/off-by-one | git bisect run | 23.1s | 23.1s to 24.3s | 7 | 7 | yes |
|  | heall, 1 worker | 22.9s (about the same) | 22.8s to 24.5s | 7 | 7 | yes |
|  | heall, 6 workers | 10.2s (2.3× faster) | 10.2s to 10.8s | 3 | 15 | yes |

## Reading the numbers

- Rounds and commits tested depend only on the search. Times depend on the machine and on what else
  it is doing; they moved a lot between runs here, which is why the range is shown beside each median.
- With the demo's own test, heall with one worker was between 1.1× slower and 1.5× faster than `git bisect run`,
  and with six workers between 1.3× slower and 1.3× faster. A test run this short is mostly start-up cost, and six
  of them at once compete for the same CPU, so there is little for parallelism to win.
- With 3s added to each test run, six workers were 2.3× faster than `git bisect run`.
  The search needs 3 rounds instead of 7, and that is what counts once a test run takes seconds, as real
  suites do. The added time is a sleep, so this is the best case: a test that keeps the CPU busy gains less.
- Every method named the right commit in every run.
- Commits that do not build are skipped by both tools; heall tests more commits in total because it
  tests several per round.
