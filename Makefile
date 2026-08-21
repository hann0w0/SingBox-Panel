VERSION ?= v1.0.8
AGENT_VERSION ?= $(shell cat cmd/agent/VERSION 2>/dev/null || echo "v1.0.0")
PANEL_LDFLAGS := -s -w -X main.version=$(VERSION)
AGENT_LDFLAGS := -s -w -X main.version=$(AGENT_VERSION)
DIST := dist

.PHONY: all build panel agents web run clean tidy test

all: build

## build everything: frontend + panel + agents
build: web panel agents

## build the panel binary (host platform)
panel:
	go build -ldflags "$(PANEL_LDFLAGS)" -o $(DIST)/singbox-panel ./cmd/panel

## cross-compile the agent for the common Linux VPS architectures
agents:
	mkdir -p $(DIST)/agents
	cp cmd/agent/VERSION $(DIST)/agents/VERSION 2>/dev/null || true
	GOOS=linux GOARCH=amd64 go build -ldflags "$(AGENT_LDFLAGS)" -o $(DIST)/agents/singbox-panel-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -ldflags "$(AGENT_LDFLAGS)" -o $(DIST)/agents/singbox-panel-agent-linux-arm64 ./cmd/agent

## build the frontend into web/dist (served by the panel in production)
web:
	cd web && npm install && npm run build

## run the panel locally (SQLite, ./deploy/panel.example.yaml)
run: panel agents
	./$(DIST)/singbox-panel --config ./deploy/panel.example.yaml

tidy:
	go mod tidy

test:
	go test ./...

clean:
	rm -rf $(DIST) web/dist
