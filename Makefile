BINARY      := windshift-agent
PKG         := ./cmd/windshift-agent
IMAGE       ?= windshift/agent:local
PW_IMAGE    ?= windshift/agent-playwright:local
WS_IMAGE    ?= ghcr.io/windshiftapp/ws-carrier:latest
LDFLAGS     := -s -w

.PHONY: build test vet cross image image-playwright verify-no-node verify-agent-contract clean

build: ## build the host binary
	go build -ldflags='$(LDFLAGS)' -o $(BINARY) $(PKG)

vet:
	go vet ./...

test:
	go test ./...

cross: ## static linux amd64 + arm64 binaries (CGO off)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='$(LDFLAGS)' -o dist/$(BINARY)-linux-arm64 $(PKG)

image: ## build the thin runtime image (WS_IMAGE supplies the ws CLI)
	docker build --build-arg WS_IMAGE=$(WS_IMAGE) -t $(IMAGE) .

image-playwright: ## build the Playwright runner variant (Node + browsers; WI-450)
	docker build -f Dockerfile.playwright --build-arg WS_IMAGE=$(WS_IMAGE) -t $(PW_IMAGE) .

verify-agent-contract: ## assert an image carries the agent-contract label + the agent/ws binaries (works for any variant)
	@test "$$(docker inspect -f '{{ index .Config.Labels "org.windshift.agent-contract" }}' $(PW_IMAGE))" = "v1" \
		|| { echo "FAIL: $(PW_IMAGE) missing org.windshift.agent-contract=v1"; exit 1; }
	@docker run --rm --entrypoint sh $(PW_IMAGE) -c '\
		set -e; \
		for b in windshift-agent ws git envsubst rg fd jq tree node npx; do command -v $$b >/dev/null || { echo "MISSING $$b"; exit 1; }; done; \
		echo "OK: agent contract + ws + tools + node present"'

verify-no-node: ## assert the image contains no node/npm and the expected tools
	@docker run --rm --entrypoint sh $(IMAGE) -c '\
		set -e; \
		for b in windshift-agent ws git envsubst rg fd jq tree; do command -v $$b >/dev/null || { echo "MISSING $$b"; exit 1; }; done; \
		if command -v node >/dev/null || command -v npm >/dev/null; then echo "FAIL: node/npm present"; exit 1; fi; \
		echo "OK: windshift-agent + ws + git + envsubst + rg/fd/jq/tree present, no node/npm"'

clean:
	rm -rf dist $(BINARY)
