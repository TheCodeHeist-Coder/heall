.PHONY: build web serve demo e2e test test-cli test-agent test-web

build:
	cd cli && go build -o ../bin/heall .

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

test: test-cli test-agent test-web

test-cli:
	cd cli && go vet ./... && go test ./...

test-agent:
	cd agent && python3 -m unittest discover -s tests -v

test-web:
	cd web && pnpm run typecheck && pnpm run lint && pnpm test
