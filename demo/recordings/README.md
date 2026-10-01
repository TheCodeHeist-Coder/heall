# Recorded model replies

Each file holds the replies of a real Groq model (`openai/gpt-oss-120b`)
during one `heall heal` run on the demo repository, one reply per line. They
are the fallback for a live demo: replaying one needs no network and no API
key, while the tools, guardrails and verifier still run for real.

```
HEALL_LLM_REPLAY=demo/recordings/off-by-one.jsonl bin/heall heal ... --inject-bad-patch
HEALL_LLM_REPLAY=demo/recordings/outdated-test.jsonl bin/heall heal ...
```

Record a fresh one by running the same command with
`HEALL_LLM_RECORD=<file>` instead. A recording only fits the commit and
flags it was made with; regenerate the demo repository and it should be
recorded again.

| File | Branch | Flags | Outcome |
|---|---|---|---|
| `off-by-one.jsonl` | `bug/off-by-one` | `--inject-bad-patch` | Fixed on the model's first attempt |
| `outdated-test.jsonl` | `change/price-format` | none | Escalated without submitting a patch |
