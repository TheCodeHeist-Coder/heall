import email.message
import io
import json
import os
import pathlib
import tempfile
import unittest
import urllib.error
from unittest import mock

from heall_agent import llm
from heall_agent.redact import redact


class Reply:
    def __init__(self, body):
        self.body = json.dumps(body).encode()

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def read(self):
        return self.body


def ok(content="", tool_calls=None, **extra):
    return Reply({"choices": [{"message": {"role": "assistant", "content": content, "tool_calls": tool_calls, **extra}}]})


def http_error(status, code="", message="", headers=None):
    hdrs = email.message.Message()
    for k, v in (headers or {}).items():
        hdrs[k] = v
    body = io.BytesIO(json.dumps({"error": {"code": code, "message": message}}).encode())
    return urllib.error.HTTPError("https://api.groq.com", status, "error", hdrs, body)


class Server:
    """Answers each request from a list, and records what was sent."""

    def __init__(self, *answers):
        self.answers = list(answers)
        self.requests = []

    def __call__(self, req, timeout):
        self.requests.append(req)
        answer = self.answers.pop(0)
        if isinstance(answer, Exception):
            raise answer
        return answer

    def keys(self):
        return [r.get_header("Authorization").removeprefix("Bearer ") for r in self.requests]


class Clock:
    def __init__(self):
        self.now = 0.0
        self.slept = []

    def time(self):
        return self.now

    def sleep(self, seconds):
        self.slept.append(seconds)
        self.now += seconds


def client(server, keys=("k1", "k2"), **kw):
    clock = Clock()
    chat = llm.GroqChat(list(keys), "some-model", opener=server, sleep=clock.sleep, clock=clock.time, **kw)
    return chat, clock


TOOLS = [{"type": "function", "function": {"name": "escalate", "parameters": {"type": "object", "properties": {}}}}]


class GroqChatTest(unittest.TestCase):
    def test_sends_an_openai_style_request_and_cleans_the_reply(self):
        calls = [{"id": "c1", "type": "function", "function": {"name": "escalate", "arguments": "{}"}, "index": 0}]
        server = Server(ok("thinking", calls, reasoning="private", executed_tools=None))
        chat, _ = client(server)
        reply = chat.chat([{"role": "user", "content": "hi"}], TOOLS, "auto")

        req = server.requests[0]
        self.assertEqual(req.full_url, "https://api.groq.com/openai/v1/chat/completions")
        self.assertEqual(req.get_header("Authorization"), "Bearer k1")
        self.assertNotIn("python", req.get_header("User-agent").lower())
        body = json.loads(req.data)
        self.assertEqual((body["model"], body["temperature"], body["tool_choice"]), ("some-model", 0, "auto"))
        self.assertEqual(body["tools"], TOOLS)
        self.assertEqual(reply["tool_calls"], [{"id": "c1", "type": "function", "function": {"name": "escalate", "arguments": "{}"}}])
        self.assertEqual((reply["content"], reply["reasoning"]), ("thinking", "private"))
        self.assertNotIn("executed_tools", reply)

    def test_rate_limited_key_is_set_aside_for_the_next(self):
        server = Server(http_error(429, "rate_limit_exceeded", "slow down", {"retry-after": "30"}), ok("a"), ok("b"))
        chat, clock = client(server)
        chat.chat([], [])
        chat.chat([], [])
        self.assertEqual(server.keys(), ["k1", "k2", "k2"])
        self.assertEqual(clock.slept, [], "another key was free, so there was nothing to wait for")

    def test_waits_for_the_reset_when_every_key_is_limited(self):
        limited = lambda s: http_error(429, "rate_limit_exceeded", f"Please try again in {s}s")
        server = Server(limited("7.5"), limited("3"), ok("done"))
        chat, clock = client(server)
        self.assertEqual(chat.chat([], [])["content"], "done")
        self.assertEqual(server.keys(), ["k1", "k2", "k2"])
        self.assertEqual(clock.slept, [3.5], "wait for the key that resets first")

        server = Server(limited("400"), limited("500"))
        chat, _ = client(server)
        with self.assertRaisesRegex(llm.LLMError, "rate limited"):
            chat.chat([], [])

    def test_invalid_keys_are_dropped(self):
        server = Server(http_error(401, "invalid_api_key", "bad key"), ok("fine"))
        chat, _ = client(server)
        self.assertEqual(chat.chat([], [])["content"], "fine")
        self.assertEqual(server.keys(), ["k1", "k2"])

        server = Server(http_error(401), http_error(401))
        chat, _ = client(server)
        with self.assertRaisesRegex(llm.LLMError, "rejected as invalid"):
            chat.chat([], [])

    def test_malformed_tool_call_is_retried(self):
        failed = lambda: http_error(400, "tool_use_failed", "Failed to call a function")
        server = Server(failed(), ok("second try"))
        chat, _ = client(server)
        self.assertEqual(chat.chat([], TOOLS)["content"], "second try")

        server = Server(failed(), failed(), failed())
        chat, _ = client(server)
        with self.assertRaisesRegex(llm.LLMError, "Failed to call a function"):
            chat.chat([], TOOLS)

    def test_server_errors_and_dropped_connections_are_retried(self):
        server = Server(http_error(503, message="overloaded"), urllib.error.URLError("reset"), ok("up again"))
        chat, clock = client(server)
        self.assertEqual(chat.chat([], [])["content"], "up again")
        self.assertEqual(clock.slept, [2, 4])

    def test_unknown_model_lists_what_is_available(self):
        server = Server(http_error(404, "model_not_found", "no such model"), Reply({"data": [{"id": "model-b"}, {"id": "model-a"}]}))
        chat, _ = client(server)
        with self.assertRaisesRegex(llm.LLMError, "'some-model' is not available.*model-a, model-b"):
            chat.chat([], [])

    def test_oversized_requests_are_reported_as_such(self):
        for err in (http_error(413, "rate_limit_exceeded", "Request too large for model"), http_error(400, "context_length_exceeded", "too long")):
            chat, _ = client(Server(err))
            with self.assertRaises(llm.RequestTooLarge):
                chat.chat([], [])

    def test_needs_a_key(self):
        with self.assertRaisesRegex(llm.LLMError, "GROQ_API_KEY"):
            llm.GroqChat([], "m")


class KeysTest(unittest.TestCase):
    def test_keys_come_from_both_variables_without_duplicates(self):
        with mock.patch.dict(os.environ, {"GROQ_API_KEYS": "a, b\nc", "GROQ_API_KEY": "b"}, clear=True):
            self.assertEqual(llm.groq_keys(), ["a", "b", "c"])
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(llm.groq_keys(), [])

    def test_env_file_fills_in_what_the_environment_lacks(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "keys.env"
            path.write_text('# keys\nexport GROQ_API_KEYS="x1,x2"\nHEALL_MODEL=from-file\n\nnot a setting\n')
            with mock.patch.dict(os.environ, {"HEALL_ENV_FILE": str(path), "HEALL_MODEL": "from-env"}, clear=True):
                llm.load_env_file()
                self.assertEqual(os.environ["GROQ_API_KEYS"], "x1,x2")
                self.assertEqual(os.environ["HEALL_MODEL"], "from-env", "the environment wins over the file")


class ReplayTest(unittest.TestCase):
    def test_a_recorded_run_replays_in_order(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = str(pathlib.Path(tmp) / "run.jsonl")
            inner, _ = client(Server(ok("one"), ok("two")))
            recorder = llm.RecordingChat(inner, path)
            recorder.chat([], [])
            recorder.chat([], [])

            replay = llm.ReplayChat(path, "some-model")
            self.assertEqual(replay.name, "some-model (replayed)")
            self.assertEqual([replay.chat([], [])["content"] for _ in range(2)], ["one", "two"])
            with self.assertRaisesRegex(llm.LLMError, "no more replies"):
                replay.chat([], [])


class RedactTest(unittest.TestCase):
    def test_credentials_are_masked(self):
        cases = {
            "GROQ_API_KEY=gsk_abcdefghijklmnopqrstuvwx": "GROQ_API_KEY=[REDACTED]",
            "export DB_PASSWORD=hunter2hunter2": "export DB_PASSWORD=[REDACTED]",
            'apiKey: "abcd1234efgh5678",': 'apiKey: "[REDACTED]",',
            "Authorization: Bearer abcdef123456789": "Authorization: Bearer [REDACTED]",
            "postgres://user:s3cretpw@db.example.com/x": "postgres://user:[REDACTED]@db.example.com/x",
            "token ghp_" + "a" * 36: "token [REDACTED]",
        }
        for text, want in cases.items():
            self.assertEqual(redact(text), want)

    def test_source_code_is_left_exactly_as_it_is(self):
        # The model has to copy text out of files to edit them, so anything
        # that is not a credential must come through untouched.
        for text in (
            "const token = generateToken();",
            'const password = "short";',
            "return items.slice(start, end);",
            "export function apiKeyFor(user) { return user.key; }",
            "if (tokens.length === 0) return secretOrPublicKey;",
        ):
            self.assertEqual(redact(text), text)


if __name__ == "__main__":
    unittest.main()
