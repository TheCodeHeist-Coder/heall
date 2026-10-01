"""The tools the model can call, and what stands behind them.

Reading is done here, against a checkout of the failing commit. Running
tests and judging patches is not: both go back to the heall CLI, so the
agent never decides for itself whether its own patch is good.
"""

from __future__ import annotations

import difflib
import json
import os
import re
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from .contracts import ContractError, HealRequest, RunTestResult, VerifyResult, parse
from .redact import redact

MAX_FILE_LINES = 400
MAX_FILE_BYTES = 200_000
MAX_MATCHES = 60
MAX_LISTED = 300
MAX_TEST_OUTPUT = 6_000


class ToolError(Exception):
    """A tool call that could not be carried out; the text goes to the model."""


class BackendError(Exception):
    """The CLI behind the agent failed. Nothing can be proved without it, so
    this ends the run instead of being reported to the model."""


class Backend(Protocol):
    """What the CLI does on the agent's behalf."""

    def run_test(self, name: str) -> RunTestResult: ...

    def verify(self, diff: str) -> VerifyResult: ...


class HeallBackend:
    """Calls back into the heall CLI that started the agent."""

    def __init__(self, request: HealRequest, request_path: str, timeout: float = 900) -> None:
        self._base = [request.heall_bin]
        self._request_path = request_path
        self._timeout = timeout

    def _call(self, args: list[str], stdin: str | None = None) -> str:
        try:
            proc = subprocess.run(
                [*self._base, *args, "--request", self._request_path],
                input=stdin,
                capture_output=True,
                text=True,
                timeout=self._timeout,
            )
        except (OSError, subprocess.TimeoutExpired) as e:
            raise BackendError(f"heall {args[0]} could not be run: {e}") from e
        if proc.returncode != 0:
            raise BackendError(f"heall {args[0]} failed: {proc.stderr.strip() or proc.stdout.strip()}")
        return proc.stdout

    def run_test(self, name: str) -> RunTestResult:
        args = ["_runtest"] + (["--test", name] if name else [])
        return self._parse(RunTestResult, self._call(args))

    def verify(self, diff: str) -> VerifyResult:
        return self._parse(VerifyResult, self._call(["_verify", "--patch", "-"], stdin=diff))

    @staticmethod
    def _parse(tp, raw: str):
        try:
            return parse(tp, raw)
        except ContractError as e:
            raise BackendError(f"heall replied with something unexpected: {e}") from e


class Workspace:
    """A read-only view of the repository at the failing commit."""

    def __init__(self, root: str) -> None:
        self.root = Path(root).resolve()
        if not root or not self.root.is_dir():
            raise BackendError(f"the worktree {root!r} given in the request does not exist")

    def resolve(self, path: str) -> Path:
        if not path or path.startswith(("/", "~")):
            raise ToolError(f"{path!r}: give a path relative to the repository root")
        target = (self.root / path).resolve()
        if target != self.root and self.root not in target.parents:
            raise ToolError(f"{path!r} is outside the repository")
        rel = target.relative_to(self.root)
        if rel.parts and (rel.parts[0] == ".git" or rel.name.startswith(".env")):
            raise ToolError(f"{path!r} may not be read")
        return target

    def exists(self, path: str) -> bool:
        try:
            return self.resolve(path).is_file()
        except ToolError:
            return False

    def read(self, path: str) -> str:
        target = self.resolve(path)
        if not target.is_file():
            raise ToolError(f"{path!r} does not exist; use list_files to see what does")
        if target.stat().st_size > MAX_FILE_BYTES:
            raise ToolError(f"{path!r} is too large to read")
        try:
            return target.read_text(encoding="utf-8")
        except UnicodeDecodeError as e:
            raise ToolError(f"{path!r} is not a text file") from e

    def files(self) -> list[str]:
        """Tracked files, as git lists them."""
        try:
            out = subprocess.run(
                ["git", "ls-files", "-z"], cwd=self.root, capture_output=True, text=True, check=True, timeout=30
            ).stdout
            return sorted(f for f in out.split("\0") if f)
        except (subprocess.SubprocessError, OSError):
            found = []
            for base, dirs, names in os.walk(self.root):
                dirs[:] = [d for d in dirs if d not in (".git", "node_modules")]
                found += [str((Path(base) / n).relative_to(self.root)) for n in names]
            return sorted(found)


def numbered(lines: list[str], start: int = 1) -> str:
    """Lines as "12|text". Nothing separates the bar from the text, so the
    indentation of each line can be read off exactly."""
    return "\n".join(f"{n}|{line}" for n, line in enumerate(lines, start))


def read_file(ws: Workspace, path: str, start_line: int = 1, end_line: int | None = None) -> tuple[str, str]:
    lines = ws.read(path).splitlines()
    start = max(int(start_line or 1), 1)
    end = min(int(end_line) if end_line else len(lines), len(lines), start + MAX_FILE_LINES - 1)
    body = numbered(lines[start - 1 : end], start)
    note = "" if end >= len(lines) else f"\n... {len(lines) - end} more lines; ask for them with start_line={end + 1}"
    header = f"{path}, lines {start}-{end} of {len(lines)}, shown as <number>|<text>:\n"
    return redact(header + body + note), f"{path}: lines {start}-{end} of {len(lines)}"


def list_files(ws: Workspace, directory: str = "") -> tuple[str, str]:
    prefix = directory.strip("/")
    if prefix:
        ws.resolve(prefix)
    files = [f for f in ws.files() if not prefix or f == prefix or f.startswith(prefix + "/")]
    if not files:
        raise ToolError(f"no tracked files under {directory!r}")
    shown = files[:MAX_LISTED]
    note = "" if len(files) <= MAX_LISTED else f"\n... {len(files) - MAX_LISTED} more"
    return "\n".join(shown) + note, f"{len(files)} files"


def search(ws: Workspace, pattern: str, path: str = "") -> tuple[str, str]:
    if not pattern:
        raise ToolError("give a pattern to search for")
    literal = ""
    try:
        regex = re.compile(pattern)
    except re.error:
        # Models often search for code such as "paginate(" as it is written.
        regex = re.compile(re.escape(pattern))
        literal = " (searched as plain text; it is not a valid regular expression)"
    prefix = path.strip("/")
    matches: list[str] = []
    total = 0
    for file in ws.files():
        if prefix and file != prefix and not file.startswith(prefix + "/"):
            continue
        try:
            text = ws.read(file)
        except ToolError:
            continue
        for n, line in enumerate(text.splitlines(), 1):
            if regex.search(line):
                total += 1
                if len(matches) < MAX_MATCHES:
                    matches.append(f"{file}:{n}: {line.strip()[:200]}")
    if not matches:
        return f"no matches for {pattern!r}{literal}", "0 matches"
    note = "" if total <= MAX_MATCHES else f"\n... {total - MAX_MATCHES} more matches; narrow the pattern or path"
    return redact(f"{total} matches for {pattern!r}{literal}:\n" + "\n".join(matches) + note), f"{total} matches"


def run_test(backend: Backend, name: str = "") -> tuple[str, str]:
    res = backend.run_test(name)
    output = res.output
    if len(output) > MAX_TEST_OUTPUT:
        half = MAX_TEST_OUTPUT // 2
        output = output[:half] + "\n... output truncated ...\n" + output[-half:]
    what = f"test {name!r}" if name else "the full suite"
    text = f"Ran {what} on the failing commit, without any patch: {res.verdict} (exit code {res.exit_code}).\n{output}"
    return redact(text), f"{res.verdict} (exit {res.exit_code})"


@dataclass(frozen=True)
class Edit:
    path: str
    old_text: str
    new_text: str


def parse_edits(raw: object) -> list[Edit]:
    # Some models send the list as a JSON string.
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except json.JSONDecodeError as e:
            raise ToolError(f"edits is not valid JSON: {e}") from e
    if not isinstance(raw, list) or not raw:
        raise ToolError("edits must be a non-empty list of {path, old_text, new_text}")
    edits = []
    for i, item in enumerate(raw):
        if not isinstance(item, dict) or not isinstance(item.get("path"), str):
            raise ToolError(f"edits[{i}] must be an object with path, old_text and new_text")
        old, new = item.get("old_text", ""), item.get("new_text", "")
        if not isinstance(old, str) or not isinstance(new, str):
            raise ToolError(f"edits[{i}]: old_text and new_text must be strings")
        edits.append(Edit(item["path"], old, new))
    return edits


_LINE_NUMBER = re.compile(r"^\s*\d+\|")


def _indent(line: str) -> str:
    return line[: len(line) - len(line.lstrip())]


def replace_once(path: str, content: str, old: str, new: str) -> str:
    """Replace the one place in content that old refers to.

    An exact match is used when there is one. Otherwise the lines are compared
    without their indentation and without any "12|" line numbers copied from
    read_file, since models reproduce the words of a line far more reliably
    than its leading whitespace. The replacement is then indented the way the
    file is.
    """
    count = content.count(old)
    if count == 1:
        return content.replace(old, new, 1)
    if count > 1:
        raise ToolError(f"{path}: old_text matches {count} places; include more surrounding lines so it matches one")

    strip = lambda text: [_LINE_NUMBER.sub("", line) for line in text.strip("\n").split("\n")]
    old_lines, new_lines = strip(old), strip(new)
    wanted = [line.strip() for line in old_lines]
    if not any(wanted):
        raise ToolError(f"{path}: old_text is empty")
    lines = content.split("\n")
    hits = [
        i
        for i in range(len(lines) - len(wanted) + 1)
        if [line.strip() for line in lines[i : i + len(wanted)]] == wanted
    ]
    if not hits:
        raise ToolError(
            f"{path}: old_text was not found, even ignoring indentation. Copy whole lines from the file, "
            "without the line numbers."
        )
    if len(hits) > 1:
        raise ToolError(f"{path}: old_text matches {len(hits)} places; include more surrounding lines so it matches one")

    at = hits[0]
    # Shift the replacement by the difference between how the model indented
    # the first line and how the file does.
    given, actual = _indent(old_lines[0]), _indent(lines[at])
    shifted = [
        "" if not line.strip() else actual + line[len(given) :] if line.startswith(given) else actual + line.lstrip()
        for line in new_lines
    ]
    return "\n".join(lines[:at] + shifted + lines[at + len(wanted) :])


def build_diff(ws: Workspace, edits: list[Edit]) -> str:
    """Turn search-and-replace edits into one unified diff.

    The model says what to replace; the diff is computed here, because models
    are unreliable at writing hunk headers and line counts by hand. Nothing
    is written to disk.
    """
    before: dict[str, str | None] = {}
    after: dict[str, str] = {}
    for edit in edits:
        path = str(Path(edit.path))
        ws.resolve(path)
        if path not in before:
            before[path] = ws.read(path) if ws.exists(path) else None
            after[path] = before[path] or ""
        current = after[path]
        if before[path] is None and edit.old_text == "":
            if current:
                raise ToolError(f"{path}: give a new file's whole content in one edit")
            after[path] = edit.new_text
            continue
        if before[path] is None:
            raise ToolError(f"{path} does not exist; to create it, use an empty old_text")
        if edit.old_text == "":
            raise ToolError(f"{path} exists; old_text must be the exact text to replace")
        after[path] = replace_once(path, current, edit.old_text, edit.new_text)

    parts = []
    for path in sorted(after):
        old, new = before[path], after[path]
        if old == new:
            continue
        header = [f"diff --git a/{path} b/{path}\n"]
        if old is None:
            header.append("new file mode 100644\n")
        body = difflib.unified_diff(
            (old or "").splitlines(keepends=True),
            new.splitlines(keepends=True),
            fromfile="/dev/null" if old is None else f"a/{path}",
            tofile=f"b/{path}",
        )
        lines = []
        for line in body:
            # difflib leaves a final line without a newline as it is; a patch
            # needs the newline and a marker saying the file has none.
            lines.append(line if line.endswith("\n") else line + "\n\\ No newline at end of file\n")
        parts.append("".join(header + lines))
    if not parts:
        raise ToolError("the edits do not change anything")
    return "".join(parts)


def describe_verify(res: VerifyResult, attempts_left: int) -> tuple[str, str]:
    """What the model is told about a submitted patch."""
    blocked = [c for c in res.guardrails if not c.passed]
    if res.status == "verified":
        return "Verified: the failing test passes and no other test broke. The fix is accepted.", "verified"
    if res.status == "rejected":
        summary = "rejected: " + ", ".join(c.name for c in blocked)
    elif res.new_failures:
        summary = f"failed: breaks {len(res.new_failures)} other test(s)"
    elif res.target_passed:
        summary = "failed"
    else:
        summary = "failed: the test still fails"
    left = (
        f"You have {attempts_left} patch attempt(s) left."
        if attempts_left > 0
        else "You have no patch attempts left. Call escalate with your diagnosis."
    )
    return redact(f"Patch {res.status} (attempt {res.attempt}).\n{res.output}\n\n{left}"), summary
