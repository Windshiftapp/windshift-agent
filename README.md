# windshift-agent

The thin native coding agent that runs inside the Windshift runner's throwaway
container. A single static Go binary with **no Node/npm** — it ships alongside
`git`, the `ws` CLI, and CA certs and nothing else.

Tracked under Windshift initiative **WI-204**; design lives on page 35 (Unified
Runner Repo Preparation & Git Isolation). Pairs with **windshift-runner**
(in the `core` repo) which owns the container lifecycle, secret injection, and
git forking.

## Trust model

The agent is the **untrusted** payload:

- It holds **no SCM credentials** and never decides what to clone or push.
- It reaches the model **only** through the Windshift `llm-proxy`
  (OpenAI-compatible `/v1/chat/completions`), never a raw provider key.
- Its tools operate inside the runner-prepared `/workspace`; the Docker
  substrate (cap-drop, read-only rootfs, user 1000, egress policy) is the
  real boundary.

## Configuration (environment only)

| Var | Meaning |
|-----|---------|
| `LLM_BASE_URL` | llm-proxy **host root**, e.g. `http://llm-proxy` (required). The client appends `/v1/chat/completions` — do not include `/v1`. |
| `LLM_MODEL`    | model id the proxy routes (required) |
| `LLM_API_KEY`  | per-run broker token; never a raw provider key (optional) |
| `LLM_CONTEXT_SIZE` | context-window budget hint; default 128000, overridden live by the proxy's `X-Context-Window` header (optional) |
| `LLM_STREAM` | `1`/`true` to use the SSE streaming path; default off — the non-streaming (`stream:false`) MVP (optional) |

## Layout

```
cmd/windshift-agent/   main() — JSONL subprocess entrypoint (loop: WI-207)
internal/llm/          OpenAI-compatible client + tool-call handling
internal/ctx/          message packing / context budgeting / truncation
internal/tools/        bash, read_file, write_file, edit_file
internal/cloud/        budget/auth header helpers (trim under WI-208)
```

## Container image

The runner image is the thin, **node-free** sandbox: `windshift-agent` + `git`
+ `ws` + CA certs + `envsubst`/entrypoint on `alpine`, nothing else. The agent
is built from this repo; the `ws` CLI lives in core, so it is lifted from a
prebuilt image via the `WS_IMAGE` build arg.

```bash
make cross                                  # static linux amd64 + arm64 binaries
make image WS_IMAGE=<image-with-ws>         # build windshift/agent:local
make verify-no-node                         # assert tools present AND no node/npm
make image-go-validation WS_IMAGE=<image>   # build Go validation variant
make verify-go-validation                   # assert Go/CGO validation tools exist
```

`make verify-no-node` is the WI-210 acceptance check: it confirms
`windshift-agent`, `ws`, `git`, and `envsubst` resolve and that neither `node`
nor `npm` exists in the image. The container runs as uid 1000 with `/workspace`
as the working dir, matching the Docker JSONL runner's `--user=1000:1000 --read-only`
contract.

The Go validation variant (`Dockerfile.go-validation`) is a custom runner image
for bindings that need to validate Go repositories inside the agent sandbox. It
keeps the same agent contract but adds Go, `make`, `bash`, a native C/C++
toolchain, `pkg-config`, SQLite headers, and SSH-capable git tooling. CI
publishes it under the same image repository with a `-go-validation` tag suffix,
e.g. `ghcr.io/windshiftapp/windshift-agent:main-go-validation`.

## Releases / CI

GitHub Actions (`.github/workflows/docker.yml`) builds the default image and
the Go validation variant on every push to `main` and on `v*` tags, publishing
multi-arch (amd64+arm64) to `ghcr.io/windshiftapp/windshift-agent`. The default
image gets `:latest` on stable tags, plus semver and `sha-` tags. The Go
validation image gets the same tag set with a `-go-validation` suffix, e.g.
`:main-go-validation`, `:<version>-go-validation`, and
`:latest-go-validation` on stable tags. Pull requests build-only. The `ws` CLI
is lifted from `WS_IMAGE` (default `ghcr.io/windshiftapp/ws-carrier:latest`;
override with the `WS_IMAGE` repo/org variable).

Core's `release.sh` also builds and pushes this image as part of a coordinated
release (using the ws-carrier image it just built as `WS_IMAGE`), so a
release publishes the agent at the same version as the server.

## Provenance

Forked and stripped from [codehamr](https://github.com/codehamr/codehamr)
(MIT). The upstream TUI (Bubble Tea), self-updater, cloud/HamrPass defaults,
and config-file bootstrap were removed; the LLM client, context packer, and
coding tools were kept and rehomed under the `windshift-agent` module. The
original MIT license is retained in `LICENSE.codehamr`.
