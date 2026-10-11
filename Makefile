GO ?= go
BIN_DIR ?= bin
BINARY := $(BIN_DIR)/gax

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/luank3/gax/internal/version.Value=$(VERSION)

# Ambient selection variables change one-shot output; app tests assert it verbatim.
TEST_ENV := env -u GAX_PROVIDER -u GAX_MODEL -u GAX_EFFORT -u GAX_PRESET

.PHONY: all build install test test-race vet fmt fmt-check lint cover clean snapshot

all: build

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/gax

install:
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/gax

test:
	$(TEST_ENV) $(GO) test ./...

test-race:
	$(TEST_ENV) $(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

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
	$(TEST_ENV) $(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

clean:
	rm -rf $(BIN_DIR) dist coverage.out

snapshot:
	goreleaser release --snapshot --clean
