GO ?= go
BIN_DIR ?= bin
BINARY := $(BIN_DIR)/gax$(if $(filter windows,$(shell $(GO) env GOOS)),.exe)

# gax is cgo-free, so the binaries build static with CGO_ENABLED=0. The race detector needs cgo, so
# only the race target turns it back on.
CGO ?= 0
TEST_PACKAGES ?= ./...
TEST_FLAGS ?=

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/luank3/gax/internal/version.Value=$(VERSION)

# Ambient selection variables change one-shot output; app tests assert it verbatim.
TEST_ENV := env -u GAX_PROVIDER -u GAX_MODEL -u GAX_EFFORT -u GAX_PRESET
STATIC_ENV := env CGO_ENABLED=$(CGO)

.PHONY: all build install test test-race vet fmt fmt-check lint cover clean snapshot

all: build

build:
	$(STATIC_ENV) $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/gax

install:
	$(STATIC_ENV) $(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/gax

test:
	$(TEST_ENV) $(STATIC_ENV) $(GO) test $(TEST_FLAGS) $(TEST_PACKAGES)

test-race:
	$(TEST_ENV) env CGO_ENABLED=1 $(GO) test -race -count=1 ./...

vet:
	$(STATIC_ENV) $(GO) vet ./...

fmt:
	gofmt -l -w .

fmt-check:
	@changed="$$(gofmt -l .)"; \
	if [ -n "$$changed" ]; then \
		echo 'gofmt needed:'; \
		echo "$$changed"; \
		exit 1; \
	fi

lint: fmt-check vet

cover:
	$(TEST_ENV) $(STATIC_ENV) $(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

clean:
	rm -rf $(BIN_DIR) dist coverage.out

snapshot:
	goreleaser release --snapshot --clean
