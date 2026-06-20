VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/rtxnik/workspace-cli/cmd.version=$(VERSION)

.PHONY: build install clean test vet lint test-e2e

build:
	go build -ldflags "$(LDFLAGS)" -o ws .

install:
	go install -ldflags "$(LDFLAGS)" .

clean:
	rm -f ws

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

test-e2e:
	go test -tags docker_e2e ./cmd/ -run TestProxyE2E -v
