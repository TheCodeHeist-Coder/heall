"""Entry point: `python -m heall_agent heal --request <file>`.

Exit code 0 means the agent finished and its last stdout line is agent_done,
whether it fixed the failure or escalated. Any other exit code is a crash or
a setup problem, explained on stderr.

Environment:
  GROQ_API_KEY, GROQ_API_KEYS  one key, or several separated by commas
  HEALL_MODEL                  overrides the model named in the request
  HEALL_LLM_RECORD=<file>      save the model's replies for a later replay
  HEALL_LLM_REPLAY=<file>      replay saved replies instead of calling Groq
A .env file in the working directory or the project root is read too.
"""

from __future__ import annotations

import argparse
import os
import sys

from . import agent
from .contracts import ContractError, load_request
from .events import Emitter
from .llm import ChatModel, GroqChat, LLMError, RecordingChat, ReplayChat, groq_keys, load_env_file
from .tools import BackendError, HeallBackend


def choose_model(name: str) -> ChatModel:
    name = os.environ.get("HEALL_MODEL") or name
    if replay := os.environ.get("HEALL_LLM_REPLAY"):
        return ReplayChat(replay, name)
    model: ChatModel = GroqChat(groq_keys(), name)
    if record := os.environ.get("HEALL_LLM_RECORD"):
        model = RecordingChat(model, record)
    return model


def heal(request_path: str) -> int:
    load_env_file()
    try:
        req = load_request(request_path)
        model = choose_model(req.model)
    except (OSError, ContractError, LLMError) as e:
        print(f"heall-agent: {e}", file=sys.stderr)
        return 2

    out = Emitter()
    try:
        result = agent.run(req, model, HeallBackend(req, request_path), out)
    except BackendError as e:
        # The CLI the agent calls back into is broken; nothing can be proved.
        print(f"heall-agent: {e}", file=sys.stderr)
        return 1
    out.done(result)
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="heall-agent")
    sub = parser.add_subparsers(dest="command", required=True)
    heal_cmd = sub.add_parser("heal", help="propose a verified fix or escalate")
    heal_cmd.add_argument("--request", required=True, help="path to a HealRequest JSON file")
    args = parser.parse_args(argv)
    return heal(args.request)


if __name__ == "__main__":
    sys.exit(main())
