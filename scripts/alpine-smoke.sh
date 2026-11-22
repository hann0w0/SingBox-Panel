#!/bin/sh
set -eu

# Build static Linux/amd64 Agent + agent test binary and verify the Alpine +
# OpenRC install/check/uninstall flow inside a real alpine:3.21 container.
IMAGE="${ALPINE_IMAGE:-alpine:3.21}"
OUT_DIR="$(mktemp -d)"
trap 'rm -rf "$OUT_DIR"' EXIT

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.version=ci-alpine" \
  -o "$OUT_DIR/singbox-panel-agent" ./cmd/agent

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c \
  -o "$OUT_DIR/agent.test" ./internal/services/agent

docker run --rm \
  -v "$OUT_DIR:/smoke:ro" \
  -e SINGBOX_PANEL_ALPINE_LIVE=1 \
  -e GITHUB_TOKEN="${GITHUB_TOKEN:-}" \
  "$IMAGE" /bin/sh -eu -c '
    apk add --no-cache openrc ca-certificates curl
    mkdir -p /run/openrc
    touch /run/openrc/softlevel

    # Confirm the static Agent binary executes natively on Alpine musl.
    /smoke/singbox-panel-agent --version | grep -q "^singbox-panel-agent ci-alpine$"

    # Run the real Alpine sing-box install / OpenRC / check / uninstall test.
    /smoke/agent.test -test.v -test.run "^TestAlpineLiveEndToEnd$"
  '
