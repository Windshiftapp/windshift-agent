#!/bin/sh
# windshift-agent runner entrypoint.
#
# Prepares optional per-run config from env injected by RunService, then execs
# the agent, which reads {type:prompt}/{type:abort} on stdin and emits NDJSON
# events on stdout (JSONL runner drives it). Deliberately tiny compared to the retired Node runtime
# entrypoint: the agent reads LLM_* directly and the runner — not the agent —
# owns git push, so there is no provider-config or askpass plumbing here.
set -eu

# Windshift CLI config: render ws.toml only when the runner injected WS_* env.
if [ -n "${WS_TOKEN:-}" ] && [ -n "${WS_API_URL:-}" ] && [ -n "${WS_WORKSPACE_KEY:-}" ]; then
    # WS_API_URL follows the broker convention and includes the /api suffix
    # (LLM_BASE_URL is built on it), but the ws CLI prefixes its own
    # /rest/api/v1 routes onto the bare server root — handing it the /api
    # base 404s every CLI call. Strip the suffix for ws.toml only.
    WS_CLI_URL="${WS_API_URL%/}"
    WS_CLI_URL="${WS_CLI_URL%/api}"
    export WS_CLI_URL
    mkdir -p "$HOME/.config/ws"
    envsubst < /etc/windshift/ws.toml.template > "$HOME/.config/ws/config.toml"
    chmod 0600 "$HOME/.config/ws/config.toml"

    # The initial prompt points the agent at ~/WINDSHIFT.md for the exact CLI
    # surface plus this workspace's statuses, item types, and transitions —
    # without it the agent invents subcommands and status ids. `ws config
    # docs` writes to cwd when no project ws.toml is found, hence the cd.
    # Best-effort: a run without the doc still works via `ws --help`. Keep
    # stdout clean — it becomes the JSONL event stream the runner parses.
    if ! (cd "$HOME" && ws config docs >/dev/null 2>&1); then
        echo "windshift-agent-entrypoint: ws config docs failed; ~/WINDSHIFT.md unavailable" >&2
    fi
fi

# Local git identity so the agent's `git commit`s in /workspace succeed. There
# is NO push auth by design: the runner pushes the run branch through the
# git-proxy, and the agent never holds SCM credentials.
git config --global user.name "${GIT_AUTHOR_NAME:-windshift-agent}" >/dev/null 2>&1 || true
git config --global user.email "${GIT_AUTHOR_EMAIL:-agent@windshift.local}" >/dev/null 2>&1 || true
export GIT_TERMINAL_PROMPT=0

# Lifecycle marker before the agent takes over stdout with JSONL events.
printf '{"type":"lifecycle","phase":"entrypoint","run_id":"%s"}\n' "${AGENT_RUN_ID:-unset}"

if [ -d /workspace ]; then
    cd /workspace
fi

exec windshift-agent
