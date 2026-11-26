VERSION ?= v1.0.19
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
AGENT_VERSION ?= $(shell cat cmd/agent/VERSION 2>/dev/null || echo "v1.0.19")
PANEL_LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
AGENT_LDFLAGS := -s -w -X main.version=$(AGENT_VERSION)
DIST := dist

.PHONY: all build embed panel agents web clean tidy test

all: build

## build everything: frontend + agents embedded into a single panel binary
build: embed panel

## stage compiled frontend and Linux agents into internal/assets for embedding
embed: web agents
	find internal/assets/web -mindepth 1 ! -name '.gitkeep' -delete
	find internal/assets/agents -mindepth 1 ! -name '.gitkeep' -delete
	cp -R web/dist/. internal/assets/web/
	cp -R $(DIST)/agents/. internal/assets/agents/

## build the panel binary (host platform)
panel:
	mkdir -p $(DIST)
	go build -ldflags "$(PANEL_LDFLAGS)" -o $(DIST)/singbox-panel ./cmd/panel

## cross-compile the agent for the common Linux VPS architectures
agents:
	mkdir -p $(DIST)/agents
	cp cmd/agent/VERSION $(DIST)/agents/VERSION 2>/dev/null || true
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "$(AGENT_LDFLAGS)" -o $(DIST)/agents/singbox-panel-agent-linux-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags "$(AGENT_LDFLAGS)" -o $(DIST)/agents/singbox-panel-agent-linux-arm64 ./cmd/agent

## build the frontend into web/dist (embedded into the panel binary on build)
web:
	cd web && npm ci && npm run build

tidy:
	go mod tidy

test:
	go test -race ./...

clean:
	rm -rf $(DIST) web/dist
	find internal/assets/web internal/assets/agents -mindepth 1 ! -name '.gitkeep' -delete 2>/dev/null || true
