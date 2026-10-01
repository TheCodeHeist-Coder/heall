#!/usr/bin/env bash
# Checks that this machine is ready to demo heall. Run it before presenting:
# it starts nothing, changes nothing, and says what to do about each problem.
#
#   scripts/preflight.sh
cd "$(dirname "$0")/.." || exit 1
problems=0
ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FIX   %s\n        %s\n' "$1" "$2"; problems=$((problems + 1)); }
note() { printf '  note  %s\n' "$1"; }

echo "heall"
if [ -x bin/heall ]; then ok "bin/heall is built"; else bad "bin/heall is missing" "run: make build"; fi
if [ -f web/out/index.html ]; then ok "the dashboard is built"; else bad "web/out is missing" "run: make web"; fi

echo "sandbox"
if docker version >/dev/null 2>&1; then
  ok "Docker is running"
  if docker image inspect node:24-alpine >/dev/null 2>&1; then
    ok "the node:24-alpine image is on this machine"
  else
    bad "the node:24-alpine image is not pulled" "run: docker pull node:24-alpine (needs network)"
  fi
  left=$(docker ps -aq --filter label=heall.session | wc -l)
  if [ "$left" -eq 0 ]; then ok "no heall containers left over"; else bad "$left heall container(s) left over" "run: docker rm -f \$(docker ps -aq --filter label=heall.session)"; fi
else
  bad "Docker is not running" "start Docker, or run heall with --sandbox local"
fi

echo "demo repository"
if [ -f demo/out/scenarios.json ] && [ -d demo/out/repo/.git ]; then
  ok "demo/out/repo exists"
  if [ -z "$(git -C demo/out/repo status --porcelain)" ]; then ok "its checkout is clean"; else bad "demo/out/repo has uncommitted changes" "run: git -C demo/out/repo checkout -- . && git -C demo/out/repo clean -fd"; fi
  trees=$(git -C demo/out/repo worktree list | wc -l)
  if [ "$trees" -eq 1 ]; then ok "no worktrees left over"; else bad "$((trees - 1)) worktree(s) left over" "run: git -C demo/out/repo worktree prune"; fi
  if git -C demo/out/repo remote get-url origin >/dev/null 2>&1; then
    ok "it has a GitHub remote: $(git -C demo/out/repo remote get-url origin)"
  else
    note "it has no remote; a real pull request needs one (dry runs do not)"
  fi
else
  bad "the demo repository is missing" "run: make demo"
fi

echo "model"
if [ -n "${GROQ_API_KEY:-}${GROQ_API_KEYS:-}" ] || grep -qsE '^(export )?GROQ_API_KEYS?=.+' .env; then
  ok "a Groq key is configured"
else
  bad "no Groq key" "copy .env.example to .env and add a key; or demo with HEALL_LLM_REPLAY (see docs/demo.md)"
fi
for f in demo/recordings/off-by-one.jsonl demo/recordings/outdated-test.jsonl; do
  if [ -s "$f" ]; then ok "fallback recording $f"; else bad "$f is missing" "record it: see demo/recordings/README.md"; fi
done

echo "github"
if gh auth status >/dev/null 2>&1; then ok "gh is logged in"; else note "gh is not logged in; needed only for a real pull request"; fi

echo "ports"
for port in 7777; do
  if (exec 3<>/dev/tcp/127.0.0.1/$port) 2>/dev/null; then note "port $port is in use (is heall serve already running?)"; else ok "port $port is free"; fi
done 2>/dev/null

echo
if [ "$problems" -eq 0 ]; then echo "ready"; else echo "$problems thing(s) to fix"; exit 1; fi
