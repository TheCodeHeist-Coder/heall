import io
import json
import pathlib
import unittest

from heall_agent import contracts, events
from heall_agent.__main__ import main

EXAMPLES = pathlib.Path(__file__).resolve().parents[2] / "contracts" / "examples"


class ContractTests(unittest.TestCase):
    def test_examples_parse(self):
        req = contracts.load_request(str(EXAMPLES / "heal_request.json"))
        self.assertEqual(req.culprit.commit.sha, "b2c3d4e")
        self.assertEqual(req.max_attempts, 3)
        self.assertIn("package.json", req.protect)

        ver = contracts.parse(
            contracts.VerifyResult, (EXAMPLES / "verify_result.json").read_text()
        )
        self.assertEqual(ver.status, "rejected")
        self.assertFalse(ver.guardrails[0].passed)

        run = contracts.parse(
            contracts.RunTestResult, (EXAMPLES / "runtest_result.json").read_text()
        )
        self.assertEqual(run.verdict, "fail")

    def test_drift_is_rejected(self):
        good = json.loads((EXAMPLES / "runtest_result.json").read_text())
        cases = {
            "unknown field": {**good, "extra": 1},
            "missing field": {k: v for k, v in good.items() if k != "output"},
            "bad enum": {**good, "verdict": "maybe"},
            "wrong type": {**good, "exit_code": "1"},
            "bool as int": {**good, "exit_code": True},
        }
        for name, value in cases.items():
            with self.subTest(name), self.assertRaises(contracts.ContractError):
                contracts.parse(contracts.RunTestResult, json.dumps(value))


class EventTests(unittest.TestCase):
    def test_agent_kinds_match_the_shared_example_stream(self):
        """Every agent kind must have the same fields as the shared example."""
        seen = {}
        for line in (EXAMPLES / "events.jsonl").read_text().splitlines():
            ev = json.loads(line)
            seen[ev["kind"]] = set(ev["data"])
        for kind, fields in events.AGENT_KINDS.items():
            self.assertEqual(seen[kind], set(fields), kind)

    def test_emit_rejects_wrong_shape(self):
        out = events.Emitter(io.StringIO())
        with self.assertRaises(ValueError):
            out.emit("run_done", outcome="fixed", duration_ms=1)
        with self.assertRaises(ValueError):
            out.emit("agent_thought", txt="hi")

    def test_verify_result_becomes_two_events(self):
        buf = io.StringIO()
        ver = contracts.parse(
            contracts.VerifyResult, (EXAMPLES / "verify_result.json").read_text()
        )
        events.Emitter(buf).verify(ver)
        lines = [json.loads(line) for line in buf.getvalue().splitlines()]
        self.assertEqual([e["kind"] for e in lines], ["guardrail_checked", "verify_done"])
        self.assertFalse(lines[0]["data"]["passed"])
        self.assertEqual(lines[1]["data"]["status"], "rejected")


class CliTests(unittest.TestCase):
    def test_bad_request_exits_2(self):
        self.assertEqual(main(["heal", "--request", "/nonexistent.json"]), 2)


if __name__ == "__main__":
    unittest.main()
