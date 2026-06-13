# syntax=docker/dockerfile:1
# =============================================================================
# windshift-agent runner image (WI-210)
#
# The thin, NODE-FREE per-run sandbox. The agent (built here from source) speaks
# the JSONL contract on stdin/stdout; the entrypoint renders optional ws config
# and sets a local git identity, then execs it. No Node, no npm, no retired runtime.
#
# Build context is THIS repo. The ws CLI lives in the core repo, so it is
# sourced from a prebuilt image rather than rebuilt here:
#
#   docker build --build-arg WS_IMAGE=<image-that-bakes-ws> \
#     -t windshift/agent:local .
#
# WS_IMAGE must expose the ws binary at /usr/local/bin/ws (the core
# deploy/coding-agent image does). For a specific arch add
# --build-arg TARGETARCH=arm64.
# =============================================================================

ARG WS_IMAGE=ghcr.io/windshiftapp/ws-carrier:latest

# --------------------------------------------------------------------
# Stage 1: build windshift-agent (stdlib-only module, static binary)
# --------------------------------------------------------------------
FROM golang:1.26.4-alpine AS agent-build
WORKDIR /src
# No third-party deps, but keep the download step so a future go.sum caches.
COPY go.mod ./
RUN go mod download
COPY . .
# Auto-populated per target platform by buildx. NEVER give these defaults:
# a default on a predefined platform ARG overrides the auto-provided value,
# which shipped an x86_64 agent binary inside the arm64 image — silently
# emulated via qemu binfmt on the arm64 runner host (core WI-394).
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/windshift-agent ./cmd/windshift-agent

# --------------------------------------------------------------------
# Stage 2: ws CLI, lifted from the core-built image
# --------------------------------------------------------------------
FROM ${WS_IMAGE} AS ws-src

# --------------------------------------------------------------------
# Stage 3: thin runtime — bash tools + our binary, NO Node
# --------------------------------------------------------------------
FROM alpine:3.21

# Agent-contract marker (WI-312): windshift-runner inspects the configured
# image for this label before its first claim and refuses to spawn an image
# without it. Guards against WSRUNNER_IMAGE pointing at a non-agent image
# (e.g. the ws-carrier), whose container would exit 0 without speaking the
# JSONL contract and report as a successful no-change run.
LABEL org.windshift.agent-contract="v1"

# ca-certificates (TLS to the brokers), git (agent commits in /workspace),
# gettext (envsubst for ws.toml). ripgrep backs the grep/list_files tools;
# fd, jq, and tree are for the model's own bash use — well-established CLIs
# beat reimplementing search/query logic in the agent binary. The GNU userland
# (coreutils/findutils/grep/sed/gawk/diffutils/patch/file) replaces the busybox
# applets so the model's bash use gets the GNU flags/behavior it expects
# (sed -i, grep -P, find -printf, …) instead of musl/busybox surprises.
# No node, no npm.
RUN apk add --no-cache ca-certificates git gettext ripgrep fd jq tree \
    coreutils findutils grep sed gawk diffutils patch file

# The JSONL container runner forces --user=1000:1000 and --read-only with a tmpfs at
# /home/agent, so own that path numerically (no passwd/group churn) and
# pre-create the ws config dir the entrypoint writes into.
RUN install -d -o 1000 -g 1000 -m 0750 /home/agent /home/agent/.config/ws

COPY --from=ws-src      /usr/local/bin/ws    /usr/local/bin/ws
COPY --from=agent-build /out/windshift-agent /usr/local/bin/windshift-agent
COPY deploy/ws.toml.template /etc/windshift/ws.toml.template
COPY deploy/windshift-agent-entrypoint.sh /usr/local/bin/windshift-agent-entrypoint.sh
RUN chmod +x /usr/local/bin/windshift-agent-entrypoint.sh \
             /usr/local/bin/windshift-agent \
             /usr/local/bin/ws \
    && chmod 0644 /etc/windshift/ws.toml.template

USER 1000:1000
ENV HOME=/home/agent
ENV PATH=/usr/local/bin:$PATH
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/windshift-agent-entrypoint.sh"]
