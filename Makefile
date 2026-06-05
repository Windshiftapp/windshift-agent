BINARY      := windshift-agent
PKG         := ./cmd/windshift-agent
IMAGE       ?= windshift/agent:local
WS_IMAGE    ?= windshift/coding-agent:latest
LDFLAGS     := -s -w

.PHONY: build test vet cross image verify-no-node clean

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

verify-no-node: ## assert the image contains no node/npm and the expected tools
	@docker run --rm --entrypoint sh $(IMAGE) -c '\
		set -e; \
		for b in windshift-agent ws git envsubst; do command -v $$b >/dev/null || { echo "MISSING $$b"; exit 1; }; done; \
		if command -v node >/dev/null || command -v npm >/dev/null; then echo "FAIL: node/npm present"; exit 1; fi; \
		echo "OK: windshift-agent + ws + git + envsubst present, no node/npm"'

clean:
	rm -rf dist $(BINARY)
