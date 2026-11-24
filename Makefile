VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build linux-amd64 linux-arm64 darwin-arm64 darwin-amd64 windows-amd64 windows-embedded embedded-agent darwin-embedded test race vet clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh .

linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-linux-amd64 .

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-linux-arm64 .

darwin-amd64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-darwin-amd64 .

darwin-arm64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-darwin-arm64 .

windows-amd64:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-windows-amd64.exe .

# Build a gzip-compressed linux/amd64 agent for embedding in macOS/Windows binaries.
embedded-agent:
	mkdir -p internal/embedded/agents
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o internal/embedded/agents/linux-amd64 .
	gzip -9 -f internal/embedded/agents/linux-amd64

# macOS binaries that carry a linux/amd64 agent inside them.
darwin-embedded: embedded-agent
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -tags embedded_agents -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -tags embedded_agents -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-darwin-arm64 .

# Windows clients with an embedded linux/amd64 agent.
windows-embedded: embedded-agent
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags embedded_agents -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-windows-amd64.exe .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -tags embedded_agents -trimpath -ldflags '$(LDFLAGS)' -o bin/kubessh-windows-arm64.exe .

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -rf bin
