"""Entry point: `python -m heall_agent heal --request <file>`.

Exit code 0 means the agent finished and its last stdout line is agent_done,
whether it fixed the failure or escalated. Any other exit code is a crash.
"""

from __future__ import annotations

import argparse
import sys

from .contracts import ContractError, HealResult, load_request
from .events import Emitter


def heal(request_path: str) -> int:
    try:
        req = load_request(request_path)
    except (OSError, ContractError) as e:
        print(f"heall-agent: bad request: {e}", file=sys.stderr)
        return 2

    out = Emitter()
    out.emit("agent_started", model=req.model, max_attempts=req.max_attempts)
    # The tool loop lands in build step 6. Until then the agent escalates,
    # which is the safe answer for an agent that cannot prove a fix.
    out.done(
        HealResult(
            outcome="escalated",
            attempts=0,
            root_cause="",
            reason="the heal agent is not implemented yet",
        )
    )
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
