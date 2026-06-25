VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/rtxnik/workspace-cli/cmd.version=$(VERSION)

.PHONY: build install clean test vet lint test-e2e test-golden-xray test-integration-proxy pin-recipe

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

test-golden-xray:
	go test -tags docker_e2e ./cmd/ -run TestXrayValidatesConfigs -v

# Local/operator target — runs all three integration tests (TestProfileLifecycleE2E
# is an operator-machine checkpoint that needs writable real ~/.config/xray state).
# CI runs only TestIntegration_Cycle (see .github/workflows/ci.yml H7 step).
test-integration-proxy:
	go test -tags integration ./internal/xray/ -run 'TestIntegration_Cycle|TestProfileLifecycleE2E|TestExistingStateDiscovery' -v

pin-recipe:
	@test -n "$(RECIPE_DIR)" || { echo "usage: make pin-recipe RECIPE_DIR=<dir> [DOTFILES_REF=<sha>]"; exit 1; }
	sh scripts/pin-recipe.sh "$(RECIPE_DIR)" "$(DOTFILES_REF)"
