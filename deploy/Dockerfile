# ---- frontend build ----
# Run the JS toolchain on the builder's native arch instead of emulating the
# target: the frontend output is architecture-independent.
FROM --platform=$BUILDPLATFORM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build

# ---- go build ----
# Compile on the builder's native arch and cross-compile to the target with
# GOOS/GOARCH. Emulating the Go toolchain under QEMU (the alternative for arm64
# on an amd64 runner) is many times slower.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=v1.0.1
ARG AGENT_VERSION
ARG TARGETOS
ARG TARGETARCH
# panel binary (cross-compiled to the image's target platform)
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/singbox-panel ./cmd/panel
# agent binaries served to VPSes (reads cmd/agent/VERSION if AGENT_VERSION is unsupplied)
RUN mkdir -p /out/agents && \
    FINAL_AGENT_VER="${AGENT_VERSION:-$(cat ./cmd/agent/VERSION 2>/dev/null || echo "v1.0.0")}" && \
    GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=${FINAL_AGENT_VER}" -o /out/agents/singbox-panel-agent-linux-amd64 ./cmd/agent && \
    GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=${FINAL_AGENT_VER}" -o /out/agents/singbox-panel-agent-linux-arm64 ./cmd/agent

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /opt/singbox-panel
COPY --from=build /out/singbox-panel ./singbox-panel
COPY --from=build /out/agents ./dist/agents
COPY --from=web /web/dist ./web/dist
ENV SINGBOX_PANEL_LISTEN=":32334" \
    SINGBOX_PANEL_DB_DSN="/data/singbox-panel.db" \
    SINGBOX_PANEL_AGENTS_DIR="/opt/singbox-panel/dist/agents" \
    SINGBOX_PANEL_WEB_DIR="/opt/singbox-panel/web/dist"
VOLUME ["/data"]
EXPOSE 32334
ENTRYPOINT ["./singbox-panel"]
