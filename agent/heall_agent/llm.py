"""Chat models the agent can talk to.

The agent needs one thing from a model: given the conversation and the tools,
return the next assistant message. GroqChat does that against Groq's
OpenAI-compatible API using only the standard library. ReplayChat and
RecordingChat let a run be recorded once and replayed without the network,
which is the fallback for a live demo.
"""

from __future__ import annotations

import json
import os
import re
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Callable, Protocol

GROQ_BASE_URL = "https://api.groq.com/openai/v1"

Message = dict[str, Any]


class LLMError(Exception):
    """The model could not produce a reply."""


class RequestTooLarge(LLMError):
    """The conversation is too big for the model or the rate limit."""


class ChatModel(Protocol):
    name: str

    def chat(self, messages: list[Message], tools: list[dict], tool_choice: Any = "auto") -> Message:
        """Return the assistant's next message: {"content", "tool_calls"}."""
        ...


def load_env_file() -> None:
    """Load KEY=VALUE lines from a .env file without overriding the environment.

    Looked for in $HEALL_ENV_FILE, the working directory, ~/.config/heall/env
    (where `heall init` suggests keeping the key), and the project root above
    this package.
    """
    here = Path(__file__).resolve().parent
    config = Path(os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config") / "heall" / "env"
    candidates = [os.environ.get("HEALL_ENV_FILE"), ".env", config, here.parent / ".env", here.parent.parent / ".env"]
    for candidate in candidates:
        if not candidate:
            continue
        path = Path(candidate)
        if not path.is_file():
            continue
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.strip().removeprefix("export ").strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, value = line.partition("=")
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            os.environ.setdefault(key.strip(), value)


def groq_keys() -> list[str]:
    """API keys from GROQ_API_KEYS (comma or space separated) and GROQ_API_KEY."""
    raw = f"{os.environ.get('GROQ_API_KEYS', '')} {os.environ.get('GROQ_API_KEY', '')}"
    keys: list[str] = []
    for key in re.split(r"[,\s]+", raw):
        if key and key not in keys:
            keys.append(key)
    return keys


class GroqChat:
    """Groq chat completions with tool calling.

    Several keys may be given. A key that hits its rate limit is set aside and
    the next one is used; when all are limited the call waits for the shortest
    reset the API reported.
    """

    def __init__(
        self,
        keys: list[str],
        model: str,
        *,
        base_url: str = GROQ_BASE_URL,
        timeout: float = 120,
        max_wait: float = 180,
        reasoning_effort: str | None = None,
        on_wait: Callable[[float], None] | None = None,
        opener: Callable[..., Any] = urllib.request.urlopen,
        sleep: Callable[[float], None] = time.sleep,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        if not keys:
            raise LLMError("no Groq API key: set GROQ_API_KEY or GROQ_API_KEYS (a .env file works)")
        self.name = model
        self._keys = list(keys)
        self._base_url = base_url.rstrip("/")
        self._timeout = timeout
        self._max_wait = max_wait
        # Reasoning models think at length by default. That costs time and
        # counts against the tokens-per-minute limit, so ask for less.
        self._reasoning_effort = reasoning_effort
        self._on_wait = on_wait
        self.calls = 0
        self.prompt_tokens = 0
        self.completion_tokens = 0
        self.waited = 0.0
        self._opener = opener
        self._sleep = sleep
        self._clock = clock
        # When each key may be used again, by the clock above.
        self._ready_at = {key: 0.0 for key in keys}
        self._current = 0

    def _request(self, key: str, path: str, body: dict | None) -> dict:
        req = urllib.request.Request(
            f"{self._base_url}{path}",
            data=None if body is None else json.dumps(body).encode(),
            headers={
                "Authorization": f"Bearer {key}",
                "Content-Type": "application/json",
                # The default Python user agent is blocked by Groq's edge.
                "User-Agent": "heall-agent/0.1",
            },
            method="GET" if body is None else "POST",
        )
        with self._opener(req, timeout=self._timeout) as resp:
            return json.loads(resp.read())

    def available_models(self) -> list[str]:
        try:
            data = self._request(self._keys[self._current % len(self._keys)], "/models", None)
            return sorted(m["id"] for m in data.get("data", []))
        except Exception:
            return []

    def _next_key(self) -> tuple[str | None, float]:
        """The first key that is ready, or the wait until one is."""
        now = self._clock()
        n = len(self._keys)
        for offset in range(n):
            i = (self._current + offset) % n
            if self._ready_at[self._keys[i]] <= now:
                self._current = i
                return self._keys[i], 0.0
        return None, min(self._ready_at.values()) - now

    def chat(self, messages: list[Message], tools: list[dict], tool_choice: Any = "auto") -> Message:
        body: dict[str, Any] = {
            "model": self.name,
            "messages": messages,
            "temperature": 0,
            "max_completion_tokens": 2048,
        }
        if self._reasoning_effort:
            body["reasoning_effort"] = self._reasoning_effort
        if tools:
            body["tools"] = tools
            body["tool_choice"] = tool_choice

        waited = 0.0
        transient = 0
        malformed = 0
        while True:
            if not self._keys:
                raise LLMError("every Groq API key was rejected as invalid")
            key, wait = self._next_key()
            if key is None:
                if waited + wait > self._max_wait:
                    raise LLMError(f"all Groq API keys are rate limited; next reset in {wait:.0f}s")
                if self._on_wait:
                    self._on_wait(wait)
                self._sleep(wait)
                waited += wait
                self.waited += wait
                continue
            try:
                data = self._request(key, "/chat/completions", body)
            except urllib.error.HTTPError as e:
                status, error = e.code, _error_body(e)
                code, message = str(error.get("code", "")), str(error.get("message", "")) or e.reason
                if status == 429:
                    self._ready_at[key] = self._clock() + _retry_after(e, message)
                    self._current += 1
                    continue
                if status == 401:
                    self._keys.remove(key)
                    del self._ready_at[key]
                    continue
                if status == 413 or code == "context_length_exceeded" or "too large" in message.lower():
                    raise RequestTooLarge(message) from e
                if status == 400 and "reasoning_effort" in body and "reasoning" in message.lower():
                    # This model does not take the setting; go on without it.
                    del body["reasoning_effort"]
                    self._reasoning_effort = None
                    continue
                if status == 400 and code == "tool_use_failed" and malformed < 2:
                    # The model wrote a tool call that could not be parsed.
                    # Trying again nearly always gives a valid one.
                    malformed += 1
                    body["temperature"] = 0.3
                    continue
                if status == 404 or code in ("model_not_found", "model_decommissioned"):
                    models = ", ".join(self.available_models()) or "unknown"
                    raise LLMError(f"model {self.name!r} is not available on Groq ({message}). Available: {models}") from e
                if status >= 500 and transient < 4:
                    transient += 1
                    self._sleep(min(2**transient, 20))
                    continue
                raise LLMError(f"Groq API error {status}: {message}") from e
            except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
                if transient < 4:
                    transient += 1
                    self._sleep(min(2**transient, 20))
                    continue
                raise LLMError(f"cannot reach the Groq API: {e}") from e

            self.calls += 1
            usage = data.get("usage") or {}
            self.prompt_tokens += int(usage.get("prompt_tokens") or 0)
            self.completion_tokens += int(usage.get("completion_tokens") or 0)
            try:
                return _clean(data["choices"][0]["message"])
            except (KeyError, IndexError, TypeError) as e:
                raise LLMError(f"unexpected reply from the Groq API: {str(data)[:300]}") from e


def usage_summary(model: ChatModel) -> str | None:
    """One line on what a run cost, for models that keep count."""
    model = getattr(model, "inner", model)
    calls = getattr(model, "calls", 0)
    if not calls:
        return None
    text = f"model usage: {calls} calls, {model.prompt_tokens} tokens in, {model.completion_tokens} out"
    if model.waited >= 1:
        text += f", {model.waited:.0f}s spent waiting for the rate limit"
    return text


def _error_body(e: urllib.error.HTTPError) -> dict:
    try:
        body = json.loads(e.read())
    except Exception:
        return {}
    error = body.get("error", body) if isinstance(body, dict) else {}
    return error if isinstance(error, dict) else {"message": str(error)}


def _retry_after(e: urllib.error.HTTPError, message: str) -> float:
    """Seconds until a rate-limited key may be used again."""
    header = e.headers.get("retry-after") if e.headers else None
    try:
        if header:
            return max(float(header), 1.0)
    except ValueError:
        pass
    # "Please try again in 7.5s" or "... in 1m3.2s"
    m = re.search(r"try again in (?:(\d+)m)?([\d.]+)s", message)
    if m:
        return int(m.group(1) or 0) * 60 + float(m.group(2)) + 0.5
    return 10.0


def _clean(message: Message) -> Message:
    """Keep the parts of an assistant message the agent uses.

    Extra fields some models add (such as "reasoning") are kept out of the
    history, since the API rejects fields it does not expect on input.
    """
    out: Message = {"role": "assistant", "content": message.get("content") or ""}
    if message.get("tool_calls"):
        out["tool_calls"] = [
            {
                "id": call["id"],
                "type": "function",
                "function": {"name": call["function"]["name"], "arguments": call["function"].get("arguments") or "{}"},
            }
            for call in message["tool_calls"]
        ]
    if message.get("reasoning"):
        out["reasoning"] = message["reasoning"]
    return out


class RecordingChat:
    """Wraps a model and appends each reply to a JSONL file."""

    def __init__(self, inner: ChatModel, path: str) -> None:
        self.name = inner.name
        self.inner = inner
        self._inner = inner
        self._path = path
        open(path, "w", encoding="utf-8").close()

    def chat(self, messages: list[Message], tools: list[dict], tool_choice: Any = "auto") -> Message:
        reply = self._inner.chat(messages, tools, tool_choice)
        with open(self._path, "a", encoding="utf-8") as f:
            f.write(json.dumps(reply) + "\n")
        return reply


class ReplayChat:
    """Returns the replies recorded in a JSONL file, in order.

    The replies are replayed whatever the conversation holds, so the tools
    and the verifier still run for real; only the model is canned.
    """

    def __init__(self, path: str, name: str = "replay") -> None:
        self.name = f"{name} (replayed)"
        with open(path, encoding="utf-8") as f:
            self._replies = [json.loads(line) for line in f if line.strip()]
        self._next = 0

    def chat(self, messages: list[Message], tools: list[dict], tool_choice: Any = "auto") -> Message:
        if self._next >= len(self._replies):
            raise LLMError("the replay file has no more replies")
        reply = self._replies[self._next]
        self._next += 1
        return reply
