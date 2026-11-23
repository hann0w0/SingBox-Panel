# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web /src/web/dist/ ./internal/assets/web/
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT
# Every panel embeds both Linux Agent architectures and the compiled frontend so
# the runtime image only ships a single self-contained binary.
RUN --mount=type=cache,target=/root/.cache/go-build \
    cp cmd/agent/VERSION internal/assets/agents/VERSION && \
    for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X main.version=$(cat cmd/agent/VERSION)" \
        -o internal/assets/agents/singbox-panel-agent-linux-$arch ./cmd/agent || exit 1; \
    done && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/singbox-panel ./cmd/panel

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates && \
    addgroup -g 10001 panel && adduser -D -H -u 10001 -G panel panel && \
    mkdir -p /app/data && chown 10001:10001 /app/data
WORKDIR /app
COPY --from=backend /out/singbox-panel /usr/local/bin/singbox-panel
ENV SINGBOX_PANEL_DEPLOYMENT=docker \
    SINGBOX_PANEL_LISTEN=:8080 \
    SINGBOX_PANEL_DB_DSN=/app/data/singbox-panel.db
USER 10001:10001
VOLUME ["/app/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -T 3 -O /dev/null http://127.0.0.1:8080/api/ready || exit 1
ENTRYPOINT ["/usr/local/bin/singbox-panel"]
