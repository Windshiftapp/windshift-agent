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

## Provenance

Forked and stripped from [codehamr](https://github.com/codehamr/codehamr)
(MIT). The upstream TUI (Bubble Tea), self-updater, cloud/HamrPass defaults,
and config-file bootstrap were removed; the LLM client, context packer, and
coding tools were kept and rehomed under the `windshift-agent` module. The
original MIT license is retained in `LICENSE.codehamr`.
