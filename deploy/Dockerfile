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
ARG VERSION=docker
ARG TARGETOS
ARG TARGETARCH
# panel binary (cross-compiled to the image's target platform)
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/singpanel ./cmd/panel
# agent binaries served to VPSes
RUN mkdir -p /out/agents && \
    GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/agents/singpanel-agent-linux-amd64 ./cmd/agent && \
    GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/agents/singpanel-agent-linux-arm64 ./cmd/agent

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /opt/singpanel
COPY --from=build /out/singpanel ./singpanel
COPY --from=build /out/agents ./dist/agents
COPY --from=web /web/dist ./web/dist
ENV SINGPANEL_LISTEN=":32334" \
    SINGPANEL_DB_DSN="/data/singpanel.db" \
    SINGPANEL_AGENTS_DIR="/opt/singpanel/dist/agents" \
    SINGPANEL_WEB_DIR="/opt/singpanel/web/dist"
VOLUME ["/data"]
EXPOSE 32334
ENTRYPOINT ["./singpanel"]
