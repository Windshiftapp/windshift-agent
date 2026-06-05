# syntax=docker/dockerfile:1
# =============================================================================
# windshift-agent runner image (WI-210)
#
# The thin, NODE-FREE per-run sandbox. The agent (built here from source) speaks
# the JSONL contract on stdin/stdout; the entrypoint renders optional ws config
# and sets a local git identity, then execs it. No Node, no npm, no pi.
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

ARG WS_IMAGE=windshift/coding-agent:latest

# --------------------------------------------------------------------
# Stage 1: build windshift-agent (stdlib-only module, static binary)
# --------------------------------------------------------------------
FROM golang:1.26.4-alpine AS agent-build
WORKDIR /src
# No third-party deps, but keep the download step so a future go.sum caches.
COPY go.mod ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
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

# ca-certificates (TLS to the brokers), git (agent commits in /workspace),
# gettext (envsubst for ws.toml). No node, no npm.
RUN apk add --no-cache ca-certificates git gettext

# DockerPiRunner forces --user=1000:1000 and --read-only with a tmpfs at
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
