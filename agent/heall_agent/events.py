"""Events the agent writes to stdout, one JSON object per line.

The agent sends only {"kind", "data"}; the CLI adds the envelope (seq, ts,
run_id, stage) and rejects any payload that does not match the contract.
stdout carries nothing but these lines; free-form logging goes to stderr.
"""

from __future__ import annotations

import dataclasses
import json
import sys
from typing import Any, TextIO

from .contracts import HealResult, VerifyResult

# Required payload fields for each kind the agent may emit.
AGENT_KINDS: dict[str, frozenset[str]] = {
    "agent_started": frozenset({"model", "max_attempts"}),
    "agent_thought": frozenset({"text"}),
    "tool_call": frozenset({"id", "name", "input"}),
    "tool_result": frozenset({"id", "name", "ok", "summary"}),
    "patch_submitted": frozenset({"attempt", "diff"}),
    "guardrail_checked": frozenset({"attempt", "passed", "checks"}),
    "verify_done": frozenset(
        {"attempt", "status", "target_passed", "suite_passed", "new_failures"}
    ),
    "agent_done": frozenset({"outcome", "attempts", "root_cause", "patch", "reason"}),
    "log": frozenset({"level", "message"}),
}


class Emitter:
    def __init__(self, out: TextIO | None = None) -> None:
        self._out = out if out is not None else sys.stdout

    def emit(self, kind: str, **data: Any) -> None:
        fields = AGENT_KINDS.get(kind)
        if fields is None:
            raise ValueError(f"the agent may not emit {kind!r}")
        if set(data) != fields:
            raise ValueError(
                f"{kind}: got fields {sorted(data)}, contract says {sorted(fields)}"
            )
        self._out.write(json.dumps({"kind": kind, "data": data}) + "\n")
        self._out.flush()

    def verify(self, result: VerifyResult) -> None:
        """Report a `heall _verify` result as its two events."""
        self.emit(
            "guardrail_checked",
            attempt=result.attempt,
            passed=all(c.passed for c in result.guardrails),
            checks=[dataclasses.asdict(c) for c in result.guardrails],
        )
        self.emit(
            "verify_done",
            attempt=result.attempt,
            status=result.status,
            target_passed=result.target_passed,
            suite_passed=result.suite_passed,
            new_failures=result.new_failures,
        )

    def done(self, result: HealResult) -> None:
        self.emit("agent_done", **dataclasses.asdict(result))
