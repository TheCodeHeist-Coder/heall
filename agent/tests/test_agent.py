import io
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

from heall_agent import agent, tools
from heall_agent.contracts import (
    Commit,
    Culprit,
    Failure,
    GuardrailCheck,
    HealRequest,
    RunTestResult,
    VerifyResult,
)
from heall_agent.events import AGENT_KINDS, Emitter
from heall_agent.llm import LLMError, RequestTooLarge

SOURCE = """\
export function bounds(page, size) {
  const start = (page - 1) * size;
  return { start, end: start + size - 1 };
}

export function paginate(items, page, size) {
  const { start, end } = bounds(page, size);
  return items.slice(start, end);
}
"""

TEST = """\
import { test } from "node:test";
test("paginate returns a full page", () => {});
test("bounds gives the last index", () => {});
"""


def call(name, **args):
    call.n = getattr(call, "n", 0) + 1
    return {"id": f"c{call.n}", "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}


def says(text="", *calls):
    return {"role": "assistant", "content": text, "tool_calls": list(calls)}


class Script:
    """A model that returns prepared replies and records what it was sent."""

    name = "scripted"

    def __init__(self, *replies):
        self.replies = list(replies)
        self.seen = []

    def chat(self, messages, tools_, tool_choice="auto"):
        self.seen.append({"messages": json.loads(json.dumps(messages)), "tools": [t["function"]["name"] for t in tools_], "choice": tool_choice})
        reply = self.replies.pop(0)
        if isinstance(reply, Exception):
            raise reply
        return reply


class FakeBackend:
    """Stands in for the CLI: judges a patch by what it contains."""

    def __init__(self):
        self.attempts = 0
        self.diffs = []

    def run_test(self, name):
        return RunTestResult("fail", 1, 12, f"not ok 1 - {name or 'suite'}\n", False)

    def verify(self, diff):
        self.attempts += 1
        self.diffs.append(diff)
        checks = [GuardrailCheck("protected_paths", True, ""), GuardrailCheck("allowlist", True, "")]
        if "test/" in diff:
            checks[0] = GuardrailCheck("protected_paths", False, "the patch touches test/paginate.test.js")
            return VerifyResult(self.attempts, "rejected", checks, False, False, [], "The patch was rejected before it was applied")
        if "end: start + size }" in diff:
            return VerifyResult(self.attempts, "failed", checks, True, False, ["bounds gives the last index"], "breaks 1 test")
        if "end + 1" in diff:
            return VerifyResult(self.attempts, "verified", checks, True, True, [], "")
        return VerifyResult(self.attempts, "failed", checks, False, False, [], "The test still fails")


class AgentTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = pathlib.Path(self.tmp.name)
        (root / "src").mkdir()
        (root / "test").mkdir()
        (root / "src/paginate.js").write_text(SOURCE)
        (root / "test/paginate.test.js").write_text(TEST)
        (root / ".env").write_text("GROQ_API_KEY=gsk_notarealkeynotarealkey1234\n")
        self.backend = FakeBackend()
        self.stream = io.StringIO()

    def request(self, **over):
        fields = dict(
            v=1, run_id="r1", repo_dir=self.tmp.name, worktree=self.tmp.name, heall_bin="heall", config_path="",
            good="g" * 40, bad="b" * 40,
            failure=Failure("paginate returns a full page", "test/paginate.test.js", "not ok 1 - paginate returns a full page"),
            suspect_files=["src/paginate.js"],
            culprit=Culprit(Commit("c" * 40, "share page bounds", "Ravi", "2026-07-19T10:00:00Z"), "share page bounds\n\nBody.", "--- a/src/paginate.js\n+++ b/src/paginate.js\n"),
            allow=["src/**"], protect=["test/**"], max_attempts=3, model="m", inject_bad_patch=False,
        )
        fields.update(over)
        return HealRequest(**fields)

    def run_agent(self, model, **over):
        result = agent.run(self.request(**over), model, self.backend, Emitter(self.stream))
        self.events = [json.loads(line) for line in self.stream.getvalue().splitlines()]
        for ev in self.events:
            self.assertEqual(set(ev["data"]), set(AGENT_KINDS[ev["kind"]]), ev["kind"])
        return result

    def kinds(self):
        return [e["kind"] for e in self.events]

    RIGHT = dict(root_cause="slice() needs an exclusive end", explanation="pass end + 1 to slice",
                 edits=[{"path": "src/paginate.js", "old_text": "items.slice(start, end)", "new_text": "items.slice(start, end + 1)"}])
    NAIVE = dict(root_cause="bounds is off by one", explanation="make end exclusive",
                 edits=[{"path": "src/paginate.js", "old_text": "end: start + size - 1 }", "new_text": "end: start + size }"}])

    def test_reads_then_fixes(self):
        model = Script(
            says("I will read the code.", call("read_file", path="src/paginate.js")),
            says("", call("search", pattern=r"bounds\(")),
            says("slice needs an exclusive end.", call("submit_patch", **self.RIGHT)),
        )
        result = self.run_agent(model)
        self.assertEqual((result.outcome, result.attempts), ("fixed", 1))
        self.assertEqual(result.root_cause, "slice() needs an exclusive end")
        self.assertIn("+  return items.slice(start, end + 1);", result.patch)
        self.assertTrue(result.patch.startswith("diff --git a/src/paginate.js b/src/paginate.js\n--- a/src/paginate.js\n+++ b/src/paginate.js\n@@"))
        self.assertEqual(
            self.kinds(),
            ["agent_started", "agent_thought", "tool_call", "tool_result", "tool_call", "tool_result", "agent_thought",
             "tool_call", "patch_submitted", "guardrail_checked", "verify_done", "tool_result"],
        )
        # The model is given what it needs up front, and sees tool output.
        first = model.seen[0]["messages"]
        self.assertIn("src/**", first[0]["content"])
        for want in ("paginate returns a full page", "Ravi", "Body.", "Files worth reading first: src/paginate.js", "3 patch attempt"):
            self.assertIn(want, first[1]["content"])
        read = model.seen[1]["messages"][-1]
        self.assertEqual(read["role"], "tool")
        self.assertIn("8    return items.slice(start, end);", read["content"])
        self.assertIn("src/paginate.js:7:", model.seen[2]["messages"][-1]["content"])

    def test_learns_from_a_failed_attempt(self):
        model = Script(says("", call("submit_patch", **self.NAIVE)), says("", call("submit_patch", **self.RIGHT)))
        result = self.run_agent(model)
        self.assertEqual((result.outcome, result.attempts), ("fixed", 2))
        feedback = model.seen[1]["messages"][-1]["content"]
        self.assertIn("Patch failed (attempt 1)", feedback)
        self.assertIn("breaks 1 test", feedback)
        self.assertIn("2 patch attempt(s) left", feedback)
        verify = [e["data"] for e in self.events if e["kind"] == "verify_done"]
        self.assertEqual([v["status"] for v in verify], ["failed", "verified"])
        self.assertEqual(verify[0]["new_failures"], ["bounds gives the last index"])

    def test_escalates_with_what_was_tried(self):
        model = Script(
            says("", call("submit_patch", **self.NAIVE)),
            says("The tests contradict each other.", call("escalate", reason="two tests disagree", diagnosis="Test A and test B want different results.")),
        )
        result = self.run_agent(model)
        self.assertEqual((result.outcome, result.attempts, result.reason), ("escalated", 1, "two tests disagree"))
        self.assertIn("Test A and test B want different results.", result.root_cause)
        self.assertIn("Attempt 1: make end exclusive -> failed, broke 1 other test(s).", result.root_cause)
        self.assertEqual(result.patch, "")

    def test_a_test_edit_is_submitted_and_rejected_by_the_cli(self):
        cheat = dict(root_cause="x", explanation="relax the test",
                     edits=[{"path": "test/paginate.test.js", "old_text": 'test("paginate returns a full page"', "new_text": 'test.skip("paginate returns a full page"'}])
        model = Script(says("", call("submit_patch", **cheat)), says("", call("submit_patch", **self.RIGHT)))
        result = self.run_agent(model)
        # The agent does not filter the patch itself: the guardrails are the
        # CLI's job, and the rejection uses up an attempt.
        self.assertEqual((result.outcome, result.attempts), ("fixed", 2))
        guard = [e["data"] for e in self.events if e["kind"] == "guardrail_checked"]
        self.assertFalse(guard[0]["passed"])
        self.assertEqual(guard[0]["checks"][0]["name"], "protected_paths")
        self.assertIn("Patch rejected (attempt 1)", model.seen[1]["messages"][-1]["content"])

    def test_out_of_attempts_leaves_only_escalation(self):
        wrong = dict(root_cause="guess", explanation="guess", edits=[{"path": "src/paginate.js", "old_text": "(page - 1)", "new_text": "(page - 2)"}])
        wrong2 = dict(wrong, edits=[{"path": "src/paginate.js", "old_text": "(page - 1)", "new_text": "(page - 3)"}])
        model = Script(
            says("", call("submit_patch", **wrong)),
            says("", call("submit_patch", **wrong2)),
            says("", call("escalate", reason="out of ideas", diagnosis="Neither guess worked.")),
        )
        result = self.run_agent(model, max_attempts=2)
        self.assertEqual((result.outcome, result.attempts), ("escalated", 2))
        self.assertEqual(model.seen[1]["tools"], [t["function"]["name"] for t in agent.TOOLS])
        self.assertEqual(model.seen[2]["tools"], ["escalate"])
        self.assertEqual(model.seen[2]["choice"]["function"]["name"], "escalate")
        self.assertIn("no patch attempts left", model.seen[2]["messages"][-1]["content"])
        self.assertEqual(self.backend.attempts, 2)

    def test_bad_edits_do_not_use_an_attempt(self):
        missing = dict(self.RIGHT, edits=[{"path": "src/paginate.js", "old_text": "items.slice(a, b)", "new_text": "x"}])
        twice = dict(self.RIGHT, edits=[{"path": "src/paginate.js", "old_text": "start", "new_text": "s"}])
        outside = dict(self.RIGHT, edits=[{"path": "../etc/passwd", "old_text": "a", "new_text": "b"}])
        model = Script(
            says("", call("submit_patch", **missing)),
            says("", call("submit_patch", **twice)),
            says("", call("submit_patch", **outside)),
            says("", call("submit_patch", **self.RIGHT)),
        )
        result = self.run_agent(model)
        self.assertEqual((result.outcome, result.attempts, self.backend.attempts), ("fixed", 1, 1))
        said = [s["messages"][-1]["content"] for s in model.seen[1:]]
        self.assertIn("old_text was not found", said[0])
        self.assertIn("matches", said[1])
        self.assertIn("outside the repository", said[2])
        self.assertEqual([e["data"]["ok"] for e in self.events if e["kind"] == "tool_result"], [False, False, False, True])

    def test_survives_what_models_get_wrong(self):
        broken_json = {"id": "x1", "type": "function", "function": {"name": "read_file", "arguments": "{not json"}}
        model = Script(
            says("", broken_json),
            says("", call("fly_away")),
            says("", call("read_file", path="src/missing.js")),
            says("I think the answer is to change slice."),
            says("", call("submit_patch", **dict(self.RIGHT, edits=json.dumps(self.RIGHT["edits"])))),
        )
        result = self.run_agent(model)
        self.assertEqual(result.outcome, "fixed")
        said = [s["messages"][-1]["content"] for s in model.seen[1:]]
        self.assertIn("not valid JSON", said[0])
        self.assertIn("no tool called 'fly_away'", said[1])
        self.assertIn("does not exist", said[2])
        self.assertEqual(said[3], "Use a tool. Finish by calling submit_patch or escalate.")

    def test_escalates_for_a_model_that_will_not_finish(self):
        result = self.run_agent(Script(says("Thinking."), says("Still thinking."), says("Hmm.")))
        self.assertEqual((result.outcome, result.reason), ("escalated", "the model stopped without submitting a fix or escalating"))

        self.stream = io.StringIO()
        result = self.run_agent(Script(says("", call("submit_patch", **self.NAIVE)), LLMError("all Groq API keys are rate limited")))
        self.assertEqual(result.outcome, "escalated")
        self.assertIn("rate limited", result.reason)
        self.assertIn("Attempt 1", result.root_cause)
        self.assertEqual(self.events[-1]["kind"], "log")

    def test_shrinks_the_conversation_when_it_is_too_large(self):
        root = pathlib.Path(self.tmp.name)
        (root / "src/big.js").write_text("".join(f"export const value{n} = {n};\n" for n in range(60)))
        model = Script(
            says("", call("read_file", path="src/big.js")),
            says("", call("read_file", path="src/big.js", start_line=10)),
            RequestTooLarge("Request too large"),
            says("", call("submit_patch", **self.RIGHT)),
        )
        self.assertEqual(self.run_agent(model).outcome, "fixed")
        retried = [m for m in model.seen[3]["messages"] if m["role"] == "tool"]
        self.assertIn("earlier output dropped", retried[0]["content"])
        self.assertNotIn("earlier output dropped", retried[1]["content"])

    def test_red_team_patch_is_staged_and_rejected(self):
        model = Script(says("", call("submit_patch", **self.RIGHT)))
        result = self.run_agent(model, inject_bad_patch=True, max_attempts=4)
        self.assertEqual((result.outcome, result.attempts), ("fixed", 2))
        self.assertIn('+test.skip("paginate returns a full page"', self.backend.diffs[0])
        self.assertEqual(self.kinds()[:5], ["agent_started", "agent_thought", "patch_submitted", "guardrail_checked", "verify_done"])
        self.assertIn("staged by heall, not written by the model", self.events[1]["data"]["text"])
        self.assertFalse(self.events[3]["data"]["passed"])
        # The model is told how many attempts are really left.
        self.assertIn("You have 3 patch attempt(s).", model.seen[0]["messages"][1]["content"])

    def test_tools_cannot_leave_the_repository_or_read_secrets(self):
        ws = tools.Workspace(self.tmp.name)
        for path in ("../x", "/etc/passwd", "src/../../x", ".env", ".git/config", "~/x", ""):
            with self.subTest(path), self.assertRaises(tools.ToolError):
                ws.read(path)
        outside = pathlib.Path(self.tmp.name).parent / "outside-secret.txt"
        outside.write_text("secret")
        self.addCleanup(outside.unlink)
        os.symlink(outside, pathlib.Path(self.tmp.name) / "src" / "link.js")
        with self.assertRaises(tools.ToolError):
            ws.read("src/link.js")

    def test_secrets_are_not_sent_to_the_model(self):
        root = pathlib.Path(self.tmp.name)
        (root / "src/config.js").write_text('export const apiKey = "sk_live_abcdefghijklmnop1234";\n')
        model = Script(
            says("", call("read_file", path="src/config.js")),
            says("", call("search", pattern="apiKey")),
            says("", call("escalate", reason="r", diagnosis="d")),
        )
        self.run_agent(model, failure=Failure("t", "test/paginate.test.js", "Authorization: Bearer abcdef0123456789abcdef"))
        sent = json.dumps([s["messages"] for s in model.seen])
        self.assertNotIn("sk_live_abcdefghijklmnop1234", sent)
        self.assertNotIn("abcdef0123456789abcdef", sent)
        self.assertIn("[REDACTED]", sent)


class DiffTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        subprocess.run(["git", "init", "-q"], cwd=self.root, check=True)
        (self.root / "src").mkdir()

    def apply(self, diff):
        """The diff must apply with plain git, counts and all."""
        proc = subprocess.run(["git", "apply", "-"], cwd=self.root, input=diff, capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr + "\n" + diff)

    def test_edits_become_a_patch_git_accepts(self):
        (self.root / "src/a.js").write_text("one\ntwo\nthree\nfour\n")
        (self.root / "src/b.js").write_text("alpha\nbeta")  # no final newline
        ws = tools.Workspace(self.tmp.name)
        diff = tools.build_diff(ws, [
            tools.Edit("src/a.js", "two\n", "TWO\n"),
            tools.Edit("src/a.js", "four", "FOUR"),
            tools.Edit("src/b.js", "beta", "BETA\ngamma"),
            tools.Edit("src/new.js", "", "export const x = 1;\n"),
        ])
        self.assertEqual((self.root / "src/a.js").read_text(), "one\ntwo\nthree\nfour\n", "build_diff must not write files")
        self.assertIn("new file mode 100644\n--- /dev/null\n+++ b/src/new.js", diff)
        self.apply(diff)
        self.assertEqual((self.root / "src/a.js").read_text(), "one\nTWO\nthree\nFOUR\n")
        self.assertEqual((self.root / "src/b.js").read_text(), "alpha\nBETA\ngamma")
        self.assertEqual((self.root / "src/new.js").read_text(), "export const x = 1;\n")

    def test_edits_that_cannot_be_applied_are_explained(self):
        (self.root / "src/a.js").write_text("one\ntwo\n")
        ws = tools.Workspace(self.tmp.name)
        cases = {
            "do not change anything": [tools.Edit("src/a.js", "one", "one")],
            "does not exist": [tools.Edit("src/nope.js", "one", "two")],
            "old_text must be the exact text": [tools.Edit("src/a.js", "", "new")],
            "non-empty list": [],
        }
        for message, edits in cases.items():
            with self.subTest(message), self.assertRaisesRegex(tools.ToolError, message):
                tools.build_diff(ws, tools.parse_edits([vars(e) for e in edits]))


if __name__ == "__main__":
    unittest.main()
