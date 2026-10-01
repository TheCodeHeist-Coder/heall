# heall dashboard

A live view of a heall run: the stages, the parallel bisect over the commit
range, the agent's attempts with their guardrail verdicts, and the result.

It is a static site. `heall serve` hands it out together with the runs it
shows, so on a demo machine nothing but the `heall` binary has to be running.

```
make web      # build the static site into web/out (needs Node and pnpm, once)
make serve    # http://localhost:7777
```

Start a run in another terminal and it appears by itself: every `heall run`
records its events under `.heall/<run id>/`, and the server follows that file.

The site has two pages in one. A newcomer lands on the **guide**: what heall
is and the five steps to use it, each with a copy button. The **Dashboard**
button opens the dashboard; "How to use" goes back. Where there are runs on
the machine, the dashboard opens first. With no runs it says so and shows
nothing: example runs open only when asked for.

## What it can show

| Source | How |
|---|---|
| The latest run on this machine | The default when `heall serve` is running |
| Any earlier run | Pick it from the list |
| A recorded sample | Pick it from the list; works with no server at all |
| An events file | "Open events file", for an `events.jsonl` from anywhere |

"Replay" plays a finished run again with its recorded pacing; long waits are
shortened. The slider moves to any moment of it.

Links can open a given view:

| Query | Effect |
|---|---|
| `?run=<run id>` | Open that run |
| `?sample=off-by-one`, `outdated-test` or `flaky` | Open a recorded sample |
| `?at=42` | Open paused after that many events |
| `?view=dashboard` or `guide` | Open that page instead of the default |
| `?theme=dark` or `light` | Use that theme instead of the system's |

## Development

```
bin/heall serve          # the API, on port 7777
cd web && pnpm dev       # the dashboard, on port 3000, with reloading
pnpm test                # the state logic, against the recorded samples
```

The samples in `public/samples/` are real runs on the demo repository. If the
event contract changes, record them again with `heall run --dry-run --out`.

## How it is put together

- `lib/events.ts` is the event contract (see `contracts/README.md`).
- `lib/run-state.ts` turns events into the state on screen. It is a pure
  function, so replaying to a moment is folding the first N events.
- `lib/use-run.ts` loads a run and controls playback.
- `components/` draws it.

Status colours never stand alone: every verdict also has a glyph and a word,
and the tested commits are available as a table.
