.PHONY: build demo test test-cli test-agent test-web

build:
	cd cli && go build -o ../bin/heall .

demo:
	node demo/generate.mjs

test: test-cli test-agent test-web

test-cli:
	cd cli && go vet ./... && go test ./...

test-agent:
	cd agent && python3 -m unittest discover -s tests -v

test-web:
	cd web && pnpm run typecheck && pnpm test
