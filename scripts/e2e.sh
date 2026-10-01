#!/bin/sh
# Runs the heal stage end to end on the demo repository with scripted model
# replies, so it needs no API key. It checks the outcome of each scenario.
#
#   make build demo && scripts/e2e.sh
set -eu
cd "$(dirname "$0")/.."
repo=demo/out/repo
scenario() { python3 -c "import json,sys; s=[x for x in json.load(open('demo/out/scenarios.json'))['scenarios'] if x['name']==sys.argv[1]][0]; print(s[sys.argv[2]])" "$1" "$2"; }

run() { # name, replay file, expected exit code, extra flags...
  name=$1; replay=$2; want=$3; shift 3
  code=0
  HEALL_LLM_REPLAY="$PWD/agent/tests/replays/$replay" bin/heall heal --repo "$repo" --good main \
    --bad "$(scenario "$name" branch)" --test "$(scenario "$name" target_test)" --culprit "$(scenario "$name" culprit)" \
    "$@" > "/tmp/heall-e2e-$name.log" 2>&1 || code=$?
  if [ "$code" -ne "$want" ]; then
    echo "FAIL $name: exit code $code, want $want"; cat "/tmp/heall-e2e-$name.log"; exit 1
  fi
  echo "ok   $name (exit $code)"
}

run off-by-one off-by-one.jsonl 0 --inject-bad-patch
grep -q "guardrail blocked it: protected_paths" /tmp/heall-e2e-off-by-one.log
grep -q "it breaks 3 other test(s)" /tmp/heall-e2e-off-by-one.log
grep -q "final check passed" /tmp/heall-e2e-off-by-one.log
run outdated-test outdated-test.jsonl 3
grep -q "escalated at heal" /tmp/heall-e2e-outdated-test.log
test -z "$(git -C "$repo" status --porcelain)"
test "$(git -C "$repo" worktree list | wc -l)" -eq 1
echo "all scenarios behaved as expected"
