.PHONY: build embed release web serve demo e2e preflight benchmark evaluate test test-cli test-agent test-web

# The agent, and the dashboard if it has been built, are copied into the Go
# tree first so that the binary carries them and runs from anywhere.
build: embed
	cd cli && go build -o ../bin/heall .

embed:
	rm -rf cli/embedded/agent/heall_agent
	mkdir -p cli/embedded/agent/heall_agent
	cp agent/heall_agent/*.py cli/embedded/agent/heall_agent/
	find cli/embedded/web -mindepth 1 ! -name PLACEHOLDER -delete
	if [ -f web/out/index.html ]; then cp -R web/out/. cli/embedded/web/; fi

# Release archives for Linux and macOS in dist/, with checksums.
release: web
	scripts/release.sh

# The dashboard as static files in web/out, which `heall serve` hands out.
web:
	cd web && pnpm install --frozen-lockfile && pnpm run build

# Dashboard at http://localhost:7777, showing the runs recorded in .heall/.
serve: build
	bin/heall serve

demo:
	node demo/generate.mjs

# The heal stage on the demo repository, with scripted model replies.
e2e: build
	scripts/e2e.sh

# Is this machine ready to demo?
preflight:
	scripts/preflight.sh

# Times the bisect against git bisect run; writes docs/benchmark.md.
benchmark: build
	scripts/benchmark.py

# Runs every demo scenario live and scores it; writes docs/results.md.
# Needs a Groq key.
evaluate: build
	scripts/evaluate.py

test: test-cli test-agent test-web

test-cli:
	cd cli && go vet ./... && go test ./...

test-agent:
	cd agent && python3 -m unittest discover -s tests -v

test-web:
	cd web && pnpm run typecheck && pnpm run lint && pnpm test
