"""The heal loop: a model with tools, working towards a proven fix.

The loop ends in one of two ways. Either a patch comes back "verified" from
the CLI, or the agent escalates with a diagnosis. There is no third outcome:
when the model runs out of attempts, stops using tools or cannot be reached,
the agent escalates on its behalf with what was learned so far.
"""

from __future__ import annotations

import json
import os
import re
from dataclasses import dataclass, field
from typing import Any

from . import tools
from .contracts import HealRequest, HealResult, VerifyResult
from .events import Emitter
from .llm import ChatModel, LLMError, Message, RequestTooLarge
from .redact import redact
from .tools import Backend, ToolError, Workspace

MAX_STEPS = 30
MAX_NUDGES = 2
MAX_THOUGHT = 1500
MAX_CONTEXT_OUTPUT = 3000
MAX_CONTEXT_DIFF = 8000
# Source shown to the model up front, to save it a round trip per file.
MAX_PRELOAD_CHARS = 8000
MAX_PRELOAD_LINES = 200
# Rough size, in tokens, the conversation is kept under. Groq's free tier
# allows 8000 tokens a minute, and every call resends the conversation.
CONTEXT_TOKENS = int(os.environ.get("HEALL_CONTEXT_TOKENS", "5500"))

SYSTEM = """\
You are the heal stage of heall, a tool that repairs a failing CI build. A test \
started failing. A bisect has already found the commit that introduced the \
failure. Find the root cause, then either submit a patch that is a real fix, or \
escalate with a clear diagnosis when no legitimate fix is open to you.

How your work is judged
- Every patch you submit is checked by an independent verifier that you cannot \
influence. It applies the patch to a clean checkout of the failing commit and \
runs the failing test and the whole test suite in a sandbox.
- A patch is accepted only if the failing test passes and no test that passed \
before now fails.
- You have a limited number of patch attempts. A rejected patch still uses one.

What you may change
- Only files matching: {allow}
- Never: {protect}. Tests and their configuration are evidence, not something \
to edit. A patch that touches them is rejected without being run.
- Never make a test pass by special-casing its inputs, detecting the test \
runner, or exiting early. Such patches are rejected.

How to work
1. Study the failing test and the code it exercises before changing anything. \
The files most likely to matter are included in the first message, shown as \
<number>|<text>; you need not read those again.
2. Work out why the culprit commit broke the test. Read its message as well as \
its diff: the message says what the author intended.
3. Before you submit, search for the other callers and tests of whatever you \
plan to change (search takes a regular expression, so escape brackets). The \
obvious edit often breaks them. Prefer the smallest change \
that fixes the cause. Do not simply revert the culprit if later code builds on it.
4. Submit the patch. If it fails, read the verifier's output and think again; \
do not resubmit a variation of the same idea.

When to escalate instead
- The culprit changed behaviour on purpose and the failing test asserts the old \
behaviour, or two tests demand different results from the same code. Then the \
right fix is to update a test, which only a person may do.
- The intended behaviour is unclear and a patch would be a guess.
- You are out of ideas or attempts.
An honest escalation with a precise diagnosis is a good outcome. A patch that \
games the tests is the worst one.

Use the tools; do not answer in prose. End by calling submit_patch or escalate.\
"""

TOOLS: list[dict[str, Any]] = [
    {
        "type": "function",
        "function": {
            "name": "read_file",
            "description": "Read a file from the repository at the failing commit. Lines are shown with numbers.",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Path relative to the repository root"},
                    "start_line": {"type": "integer", "description": "First line to show (default 1)"},
                    "end_line": {"type": "integer", "description": "Last line to show (default: end of file)"},
                },
                "required": ["path"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "list_files",
            "description": "List the tracked files of the repository, or of one directory.",
            "parameters": {
                "type": "object",
                "properties": {"directory": {"type": "string", "description": "Directory to list (default: everything)"}},
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "search",
            "description": "Search the repository with a regular expression. Use it to find every caller and test of code you plan to change.",
            "parameters": {
                "type": "object",
                "properties": {
                    "pattern": {"type": "string", "description": "Regular expression, matched line by line"},
                    "path": {"type": "string", "description": "Limit the search to this file or directory"},
                },
                "required": ["pattern"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "run_test",
            "description": "Run one test, or the whole suite, on the failing commit as it is. It does not apply any change of yours; use submit_patch for that.",
            "parameters": {
                "type": "object",
                "properties": {"name": {"type": "string", "description": "Exact test name (default: the whole suite)"}},
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "submit_patch",
            "description": "Submit a fix for verification. Uses one attempt. Give the change as exact search-and-replace edits; the diff is built for you.",
            "parameters": {
                "type": "object",
                "properties": {
                    "root_cause": {"type": "string", "description": "Why the test fails, in one or two sentences"},
                    "explanation": {"type": "string", "description": "What the patch changes and why that is the right fix"},
                    "edits": {
                        "type": "array",
                        "description": "Replacements to make, in order",
                        "items": {
                            "type": "object",
                            "properties": {
                                "path": {"type": "string"},
                                "old_text": {
                                    "type": "string",
                                    "description": "Exact text to replace, copied from the file without line numbers. It must occur exactly once. Empty to create a new file.",
                                },
                                "new_text": {"type": "string", "description": "Text to put in its place"},
                            },
                            "required": ["path", "old_text", "new_text"],
                        },
                    },
                },
                "required": ["root_cause", "explanation", "edits"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "escalate",
            "description": "Stop and hand the problem to a person, because no legitimate fix is open to you.",
            "parameters": {
                "type": "object",
                "properties": {
                    "reason": {"type": "string", "description": "One line: why this cannot be fixed automatically"},
                    "diagnosis": {
                        "type": "string",
                        "description": "What you found: the root cause, the evidence, and what a person should do next",
                    },
                },
                "required": ["reason", "diagnosis"],
            },
        },
    },
]

ESCALATE_ONLY = [t for t in TOOLS if t["function"]["name"] == "escalate"]


def _clip(text: str, limit: int) -> str:
    text = text.strip()
    return text if len(text) <= limit else text[:limit] + "\n... (cut)"


def preload(req: HealRequest, ws: Workspace) -> list[str]:
    """The failing test's file and the suspect files, as far as they fit."""
    parts: list[str] = []
    room = MAX_PRELOAD_CHARS
    for path in dict.fromkeys([req.failure.test_file, *req.suspect_files]):
        if not path or not ws.exists(path):
            continue
        try:
            lines = ws.read(path).splitlines()
        except ToolError:
            continue
        text = tools.numbered(lines[:MAX_PRELOAD_LINES])
        if len(lines) > MAX_PRELOAD_LINES:
            text += f"\n... {len(lines) - MAX_PRELOAD_LINES} more lines; use read_file for them"
        if len(text) > room:
            continue
        room -= len(text)
        label = "The failing test's file" if path == req.failure.test_file else "File"
        parts += ["", f"{label} {path}:", text]
    return parts


def context_message(req: HealRequest, ws: Workspace, attempts_left: int) -> str:
    c = req.culprit
    parts = [
        f"Failing test: {req.failure.test_name}",
        f"Test file: {req.failure.test_file}",
        "",
        "Test output on the failing commit:",
        _clip(req.failure.output, MAX_CONTEXT_OUTPUT),
        "",
        f"Culprit commit {c.commit.sha[:10]} by {c.commit.author} on {c.commit.date[:10]}:",
        _clip(c.message or c.commit.subject, 2000),
        "",
        "Its diff:",
        _clip(c.diff, MAX_CONTEXT_DIFF),
    ]
    shown = preload(req, ws)
    if shown:
        parts += ["", "The files below are as they are at the failing commit, shown as <number>|<text>."] + shown
    parts += ["", f"You have {attempts_left} patch attempt(s)."]
    return redact("\n".join(parts))


@dataclass
class _Run:
    req: HealRequest
    model: ChatModel
    backend: Backend
    ws: Workspace
    out: Emitter
    used: int = 0
    submitted: int = 0
    history: list[str] = field(default_factory=list)
    last_root_cause: str = ""
    result: HealResult | None = None

    @property
    def attempts_left(self) -> int:
        return max(self.req.max_attempts - self.used, 0)

    def submit(self, diff: str, note: str) -> VerifyResult:
        """Send a patch to the verifier and report what came back."""
        self.submitted += 1
        self.out.emit("patch_submitted", attempt=self.submitted, diff=diff)
        res = self.backend.verify(diff)
        # The CLI keeps the count; trust it over our own.
        self.used = max(self.used + 1, res.attempt)
        self.out.verify(res)
        blocked = ", ".join(c.name for c in res.guardrails if not c.passed)
        outcome = res.status + (f" by {blocked}" if blocked else "")
        if res.new_failures:
            outcome += f", broke {len(res.new_failures)} other test(s)"
        self.history.append(f"Attempt {res.attempt}: {note.strip() or 'patch'} -> {outcome}.")
        return res

    def call(self, name: str, args: dict[str, Any]) -> tuple[bool, str, str]:
        """Run one tool. Returns (ok, text for the model, summary for the log)."""
        try:
            if name == "read_file":
                text, summary = tools.read_file(self.ws, args.get("path", ""), args.get("start_line") or 1, args.get("end_line"))
            elif name == "list_files":
                text, summary = tools.list_files(self.ws, args.get("directory") or "")
            elif name == "search":
                text, summary = tools.search(self.ws, args.get("pattern", ""), args.get("path") or "")
            elif name == "run_test":
                text, summary = tools.run_test(self.backend, args.get("name") or "")
            elif name == "submit_patch":
                return self.submit_patch(args)
            elif name == "escalate":
                return self.escalate(args)
            else:
                raise ToolError(f"there is no tool called {name!r}")
            return True, text, summary
        except ToolError as e:
            return False, f"Error: {e}", str(e)[:120]
        except (TypeError, ValueError) as e:
            return False, f"Error: bad arguments for {name}: {e}", "bad arguments"

    def submit_patch(self, args: dict[str, Any]) -> tuple[bool, str, str]:
        if self.attempts_left <= 0:
            return False, "You have no patch attempts left. Call escalate with your diagnosis.", "no attempts left"
        # Problems with the edits themselves are reported without using an
        # attempt: nothing was submitted.
        diff = tools.build_diff(self.ws, tools.parse_edits(args.get("edits")))
        root_cause = str(args.get("root_cause", "")).strip()
        explanation = str(args.get("explanation", "")).strip()
        self.last_root_cause = root_cause or self.last_root_cause
        res = self.submit(diff, explanation)
        text, summary = tools.describe_verify(res, self.attempts_left)
        if res.status == "verified":
            self.result = HealResult(outcome="fixed", attempts=self.used, root_cause=root_cause or explanation, patch=diff)
        return res.status == "verified", text, summary

    def escalate(self, args: dict[str, Any]) -> tuple[bool, str, str]:
        reason = str(args.get("reason", "")).strip() or "the agent could not find a legitimate fix"
        diagnosis = str(args.get("diagnosis", "")).strip() or self.last_root_cause
        if self.history:
            diagnosis += "\n\nWhat was tried:\n" + "\n".join(self.history)
        self.result = HealResult(outcome="escalated", attempts=self.used, root_cause=diagnosis.strip(), reason=reason)
        return True, "Escalated.", "escalated"

    def give_up(self, reason: str) -> HealResult:
        """Escalate on the model's behalf."""
        diagnosis = self.last_root_cause
        if self.history:
            diagnosis += "\n\nWhat was tried:\n" + "\n".join(self.history)
        return HealResult(outcome="escalated", attempts=self.used, root_cause=diagnosis.strip(), reason=reason)


def red_team_patch(req: HealRequest, ws: Workspace) -> str | None:
    """A patch that cheats: it skips the failing test instead of fixing it."""
    path = req.failure.test_file
    if not path or not ws.exists(path):
        return None
    source = ws.read(path)
    name = re.escape(req.failure.test_name)
    declared = re.search(rf"\b(test|it)\(\s*([\"'`]){name}\2", source)
    if declared:
        old = declared.group(0)
        new = old.replace(f"{declared.group(1)}(", f"{declared.group(1)}.skip(", 1)
    else:
        first = source.split("\n", 1)[0]
        old, new = first, "// failing test disabled\n" + first
    try:
        return tools.build_diff(ws, [tools.Edit(path, old, new)])
    except ToolError:
        return None


def _arguments(raw: Any) -> dict[str, Any]:
    if isinstance(raw, dict):
        return raw
    try:
        value = json.loads(raw or "{}")
    except json.JSONDecodeError as e:
        raise ToolError(f"the tool arguments are not valid JSON: {e}") from e
    if not isinstance(value, dict):
        raise ToolError("the tool arguments must be a JSON object")
    return value


DROPPED = "\n... (earlier output dropped to save space)"


def _tokens(messages: list[Message]) -> int:
    """A rough count; about 3.5 characters make a token."""
    chars = sum(len(m.get("content") or "") + len(json.dumps(m.get("tool_calls") or "")) for m in messages)
    return int(chars / 3.5)


def _implied_call(content: str) -> dict | None:
    """The tool call a model meant when it wrote the arguments as its answer.

    Some models reply with the JSON arguments of submit_patch or escalate as
    plain text instead of calling the tool. The intent is unambiguous, so it
    is honoured rather than spending another model call on a reminder.
    """
    text = content.strip()
    if text.startswith("```"):
        text = re.sub(r"^```[a-z]*\n|\n```$", "", text)
    try:
        args = json.loads(text)
    except json.JSONDecodeError:
        return None
    if not isinstance(args, dict):
        return None
    if isinstance(args.get("edits"), list):
        name = "submit_patch"
    elif isinstance(args.get("diagnosis"), str):
        name = "escalate"
    else:
        return None
    return {"id": "implied", "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}


def _shrink(messages: list[Message], target: int | None = None) -> bool:
    """Drop the body of older tool results, oldest first, to make the
    conversation smaller. The latest result is always kept whole. With a
    target, stop as soon as the conversation is under it."""
    tool_messages = [m for m in messages if m.get("role") == "tool"]
    changed = False
    for m in tool_messages[:-1]:
        if target is not None and _tokens(messages) <= target:
            break
        if len(m.get("content", "")) > 300 and not m["content"].endswith(DROPPED):
            m["content"] = m["content"][:200] + DROPPED
            changed = True
    return changed


def run(req: HealRequest, model: ChatModel, backend: Backend, out: Emitter) -> HealResult:
    ws = Workspace(req.worktree)
    state = _Run(req, model, backend, ws, out)
    out.emit("agent_started", model=model.name, max_attempts=req.max_attempts)

    if req.inject_bad_patch:
        diff = red_team_patch(req, ws)
        if diff:
            out.emit(
                "agent_thought",
                text="Red-team check (staged by heall, not written by the model): submitting a patch that "
                "skips the failing test instead of fixing it. The guardrails should reject it.",
            )
            state.submit(diff, "red-team patch that skips the failing test")
        else:
            out.emit("log", level="warn", message="red-team check skipped: the failing test's file could not be read")

    messages: list[Message] = [
        {"role": "system", "content": SYSTEM.format(allow=", ".join(req.allow), protect=", ".join(req.protect))},
        {"role": "user", "content": context_message(req, ws, state.attempts_left)},
    ]
    nudges = 0
    for _ in range(MAX_STEPS):
        # Out of attempts: the only move left is to escalate.
        exhausted = state.attempts_left <= 0
        offered = ESCALATE_ONLY if exhausted else TOOLS
        choice: Any = {"type": "function", "function": {"name": "escalate"}} if exhausted else "auto"
        _shrink(messages, CONTEXT_TOKENS)
        try:
            reply = model.chat(messages, offered, choice)
        except RequestTooLarge as e:
            if _shrink(messages):
                continue
            out.emit("log", level="error", message=f"model call failed: {e}")
            return state.give_up("the conversation grew too large for the model")
        except LLMError as e:
            out.emit("log", level="error", message=f"model call failed: {e}")
            return state.give_up(f"the language model could not be used: {e}")

        calls = reply.get("tool_calls") or []
        content = reply.get("content") or ""
        if not calls and not exhausted and (implied := _implied_call(content)):
            calls, content = [implied], ""
        thought = (content or reply.get("reasoning") or "").strip()
        if thought:
            out.emit("agent_thought", text=_clip(thought, MAX_THOUGHT))
        entry: Message = {"role": "assistant", "content": content}
        if calls:
            entry["tool_calls"] = calls
        messages.append(entry)

        if not calls:
            nudges += 1
            if nudges > MAX_NUDGES:
                return state.give_up("the model stopped without submitting a fix or escalating")
            messages.append({"role": "user", "content": "Use a tool. Finish by calling submit_patch or escalate."})
            continue

        for call in calls:
            name = call["function"]["name"]
            try:
                args = _arguments(call["function"].get("arguments"))
            except ToolError as e:
                args, ok, text, summary = {}, False, f"Error: {e}", "invalid arguments"
                out.emit("tool_call", id=call["id"], name=name, input=args)
            else:
                out.emit("tool_call", id=call["id"], name=name, input=args)
                ok, text, summary = state.call(name, args)
            out.emit("tool_result", id=call["id"], name=name, ok=ok, summary=summary)
            messages.append({"role": "tool", "tool_call_id": call["id"], "content": text})
            if state.result is not None:
                return state.result

    return state.give_up(f"no verified fix after {MAX_STEPS} steps")
