# Scripted model replies

These files are hand-written stand-ins for the model, one reply per line.
They are **not** recordings of a real model. They exist so the whole heal
path (agent, tools, CLI callbacks, guardrails, verifier) can be exercised
without an API key:

```
HEALL_LLM_REPLAY=agent/tests/replays/off-by-one.jsonl bin/heall heal ...
```

To save a real run for replay, set `HEALL_LLM_RECORD=<file>` instead.
