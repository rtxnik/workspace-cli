VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/rtxnik/workspace-cli/cmd.version=$(VERSION)

.PHONY: build install clean test vet lint test-e2e test-golden-xray test-integration-proxy test-mutation pin-recipe

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

# The output layer's mutation harness. internal/output/mutation_test.go carries
# `//go:build mutation`, so a full corpus sweep per mutant stays out of
# `go test ./...`; this target is how it still runs as a hard gate, and
# .github/workflows/ci.yml's `mutation` job is where it runs on every push.
#
# THERE IS NO -run FILTER, deliberately. The build tag is what brings the
# harness into the run at all, and it should be the only thing deciding what
# runs here: a -run filter is a second list that has to be kept in step with
# the first. Measured — an earlier draft filtered on
# 'TestMutationHarness|TestContractMutationHarness|TestPairedAssertion', which
# runs exactly three tests and silently excludes TestMutantSwitchesAreRestored,
# the detector that proves a planted defect cannot leak into the rest of the
# run. The package's untagged tests run here a second time as a side effect,
# and that is the cheap half: the harness is about 100s of the ~117s total.
test-mutation:
	go test -tags mutation ./internal/output/ -v

pin-recipe:
	@test -n "$(RECIPE_DIR)" || { echo "usage: make pin-recipe RECIPE_DIR=<dir> [DOTFILES_REF=<sha>]"; exit 1; }
	sh scripts/pin-recipe.sh "$(RECIPE_DIR)" "$(DOTFILES_REF)"
