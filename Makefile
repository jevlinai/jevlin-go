GO ?= go
VERSION ?= dev

.PHONY: build test race vet fmt lint lint-windows vuln cross tidy verify quick

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/jevlin ./cmd/jevlin

test:
	$(GO) test -count=1 ./...

# The race detector needs cgo; deliberately separate from the CGO_ENABLED=0 build.
# cmd/jevlin's tests run one after another in one process and are nearly all of this
# stage's time, so tools/testshard splits the run into RACE_PARTS processes at once,
# as CI splits it into jobs. Together the parts run every test, each once.
RACE_PARTS ?= 4
race:
	CGO_ENABLED=1 $(GO) run ./tools/testshard -race -all -n $(RACE_PARTS) ./...

vet:
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 $(GO) vet ./...

fmt:
	$(GO) run golang.org/x/tools/cmd/goimports@v0.30.0 -local github.com/jevlinai/jevlin-go -w .

# Matches the CI golangci-lint job (config in .golangci.yml; version pinned in ci.yml).
lint:
	golangci-lint run

# The same lint for the Windows build. CI runs on Linux, so a finding in a file built only for
# Windows (//go:build windows, *_windows.go) is invisible to `lint`; this is what sees it.
lint-windows:
	GOOS=windows GOARCH=amd64 golangci-lint run ./...

# Dependency vulnerability scan (matches CI; pinned there).
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.5.0 ./...

tidy:
	$(GO) mod tidy

# Every release platform, compile only.
cross:
	@for os in darwin linux windows; do for arch in amd64 arm64; do \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -o /dev/null ./... || exit 1; \
	done; done

# The inner loop while a change is being made: vet, lint and the tests RUN names (a
# `go test -run` pattern) in PKG. A narrow RUN takes seconds. It is not the gate:
# make verify runs before every push.
PKG ?= ./...
RUN ?= .
quick:
	$(GO) vet $(PKG)
	golangci-lint run $(PKG)
	$(GO) test -count=1 -run '$(RUN)' $(PKG)

# The checks that take seconds come first, so a vet or lint finding stops the run before
# the tests start. There is no separate `test`: race runs every test, and CI's test matrix
# runs them without the race detector on all four systems, which is where
# pkg/redact's tight linear-time bound is held.
verify: build vet lint lint-windows tidy race vuln cross
