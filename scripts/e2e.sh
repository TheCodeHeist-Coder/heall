#!/bin/sh
# Runs the whole pipeline on every scenario of the demo repository and checks
# each outcome. The model's replies are scripted (agent/tests/replays), so
# this needs no API key and no network; nothing is pushed or posted.
#
#   make build demo && scripts/e2e.sh
set -eu
cd "$(dirname "$0")/.."
repo=demo/out/repo
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
scenario() { python3 -c "import json,sys; s=[x for x in json.load(open('demo/out/scenarios.json'))['scenarios'] if x['name']==sys.argv[1]][0]; print(s[sys.argv[2]])" "$1" "$2"; }
field() { python3 -c "import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2]) or '')" "$1" "$2"; }

run() { # name, replay file, expected exit code, expected stage, extra flags...
  name=$1; replay=$2; want=$3; stage=$4; shift 4
  code=0
  HEALL_LLM_REPLAY="$PWD/agent/tests/replays/$replay" bin/heall run --repo "$repo" --good main \
    --bad "$(scenario "$name" branch)" --log "demo/out/$(scenario "$name" log)" \
    --dry-run --json --out "$out/$name" "$@" > "$out/$name.json" 2> "$out/$name.log" || code=$?
  if [ "$code" -ne "$want" ]; then
    echo "FAIL $name: exit code $code, want $want"; cat "$out/$name.log"; exit 1
  fi
  if [ "$(field "$out/$name.json" stage)" != "$stage" ]; then
    echo "FAIL $name: stopped at '$(field "$out/$name.json" stage)', want '$stage'"; cat "$out/$name.log"; exit 1
  fi
  echo "ok   $name: $(field "$out/$name.json" outcome)${stage:+ at $stage} (exit $code)"
}

run off-by-one off-by-one.jsonl 0 "" --inject-bad-patch
test "$(python3 -c "import json; print(json.load(open('$out/off-by-one.json'))['culprit']['sha'])")" = "$(scenario off-by-one culprit)"
grep -q "guardrail blocked it: protected_paths" "$out/off-by-one.log"
grep -q "it breaks 3 other test(s)" "$out/off-by-one.log"
grep -q "final check passed" "$out/off-by-one.log"
test -s "$out/off-by-one/fix.patch" && test -s "$out/off-by-one/report.md"

run outdated-test outdated-test.jsonl 3 heal
test "$(python3 -c "import json; print(json.load(open('$out/outdated-test.json'))['culprit']['sha'])")" = "$(scenario outdated-test culprit)"
test -s "$out/outdated-test/diagnosis.md" && test ! -e "$out/outdated-test/fix.patch"

# The flaky test never reaches the agent, so the replay file is not read.
run flaky off-by-one.jsonl 3 reproduce
grep -q "Math.random" "$out/flaky/diagnosis.md"

test -z "$(git -C "$repo" status --porcelain)"
test "$(git -C "$repo" worktree list | wc -l)" -eq 1
echo "all scenarios behaved as expected"
