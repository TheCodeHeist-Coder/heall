"""JSON exchanged with the Go CLI.

contracts/README.md is the source of truth; cli/internal/agentio and
cli/internal/events hold the Go side. Parsing is strict in both directions so
a renamed field fails loudly instead of being silently dropped.
"""

from __future__ import annotations

import dataclasses
import json
import types
import typing
from dataclasses import dataclass
from typing import Any, Literal

VERSION = 1

Verdict = Literal["pass", "fail", "flaky", "skipped"]
VerifyStatus = Literal["verified", "rejected", "failed"]
Outcome = Literal["fixed", "escalated"]


class ContractError(ValueError):
    pass


@dataclass(frozen=True)
class Commit:
    sha: str
    subject: str
    author: str
    date: str


@dataclass(frozen=True)
class Failure:
    test_name: str
    test_file: str
    output: str


@dataclass(frozen=True)
class Culprit:
    commit: Commit
    # The full commit message: it often says whether a change was intended.
    message: str
    diff: str


@dataclass(frozen=True)
class HealRequest:
    v: int
    run_id: str
    repo_dir: str
    # Checkout of the bad commit to read from. Edits here are ignored; a change
    # only counts when it is sent through `heall _verify`.
    worktree: str
    heall_bin: str
    # The .heall.yaml in use; empty means the one in repo_dir.
    config_path: str
    good: str
    bad: str
    failure: Failure
    # Source files triage picked out as worth reading first.
    suspect_files: list[str]
    culprit: Culprit
    allow: list[str]
    protect: list[str]
    max_attempts: int
    model: str
    inject_bad_patch: bool


@dataclass(frozen=True)
class RunTestResult:
    verdict: Verdict
    exit_code: int
    duration_ms: int
    output: str
    truncated: bool


@dataclass(frozen=True)
class GuardrailCheck:
    name: str
    passed: bool
    detail: str


@dataclass(frozen=True)
class VerifyResult:
    attempt: int
    status: VerifyStatus
    guardrails: list[GuardrailCheck]
    target_passed: bool
    suite_passed: bool
    new_failures: list[str]
    output: str


@dataclass(frozen=True)
class HealResult:
    """Payload of the final agent_done event."""

    outcome: Outcome
    attempts: int
    root_cause: str
    patch: str = ""
    reason: str = ""


def _build(tp: Any, value: Any, path: str) -> Any:
    origin = typing.get_origin(tp)
    if dataclasses.is_dataclass(tp):
        if not isinstance(value, dict):
            raise ContractError(f"{path}: expected an object")
        hints = typing.get_type_hints(tp)
        fields = {f.name: f for f in dataclasses.fields(tp)}
        unknown = sorted(set(value) - set(fields))
        if unknown:
            raise ContractError(f"{path}: unknown fields {unknown}")
        kwargs = {}
        for name, field in fields.items():
            if name in value:
                kwargs[name] = _build(hints[name], value[name], f"{path}.{name}")
            elif field.default is dataclasses.MISSING:
                raise ContractError(f"{path}: missing field {name!r}")
        return tp(**kwargs)
    if origin is list:
        if not isinstance(value, list):
            raise ContractError(f"{path}: expected a list")
        (item,) = typing.get_args(tp)
        return [_build(item, v, f"{path}[{i}]") for i, v in enumerate(value)]
    if origin is Literal:
        if value not in typing.get_args(tp):
            raise ContractError(f"{path}: {value!r} is not one of {typing.get_args(tp)}")
        return value
    if isinstance(tp, type) and not isinstance(tp, types.GenericAlias):
        # bool is a subclass of int; do not let true/false pass as numbers.
        if not isinstance(value, tp) or (tp is int and isinstance(value, bool)):
            raise ContractError(f"{path}: expected {tp.__name__}, got {type(value).__name__}")
        return value
    raise ContractError(f"{path}: unsupported type {tp!r}")


T = typing.TypeVar("T")


def parse(tp: type[T], raw: str | bytes) -> T:
    """Parse JSON into the dataclass tp, rejecting unknown or missing fields."""
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as e:
        raise ContractError(f"{tp.__name__}: invalid JSON: {e}") from e
    return _build(tp, value, tp.__name__)


def load_request(path: str) -> HealRequest:
    with open(path, encoding="utf-8") as f:
        req = parse(HealRequest, f.read())
    if req.v != VERSION:
        raise ContractError(f"HealRequest.v: got {req.v}, this agent speaks {VERSION}")
    return req
