BINARY      := windshift-agent
PKG         := ./cmd/windshift-agent
IMAGE       ?= windshift/agent:local
PW_IMAGE    ?= windshift/agent-playwright:local
GO_IMAGE    ?= windshift/agent-go-validation:local
WS_IMAGE    ?= ghcr.io/windshiftapp/ws-carrier:latest
LDFLAGS     := -s -w

.PHONY: build test vet cross image image-playwright image-go-validation verify-no-node verify-agent-contract verify-go-validation clean

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

image-go-validation: ## build the Go validation runner variant (Go toolchain + CGO build deps)
	docker build -f Dockerfile.go-validation --build-arg WS_IMAGE=$(WS_IMAGE) -t $(GO_IMAGE) .

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

verify-go-validation: ## assert the Go validation image has the agent contract + Go/CGO validation tools
	@test "$$(docker inspect -f '{{ index .Config.Labels "org.windshift.agent-contract" }}' $(GO_IMAGE))" = "v1" \
		|| { echo "FAIL: $(GO_IMAGE) missing org.windshift.agent-contract=v1"; exit 1; }
	@docker run --rm --entrypoint sh $(GO_IMAGE) -c '\
		set -e; \
		for b in windshift-agent ws git envsubst rg fd jq tree go gcc g++ make pkg-config sqlite3; do command -v $$b >/dev/null || { echo "MISSING $$b"; exit 1; }; done; \
		go version; \
		echo "OK: Go validation image has agent contract + ws + Go/CGO tooling"'

clean:
	rm -rf dist $(BINARY)
