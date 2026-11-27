.PHONY: build test test-race vet fmt fmt-check lint cross clean

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/gax ./cmd/gax

test:
	CGO_ENABLED=0 go test ./...

test-race:
	CGO_ENABLED=1 go test -race -count=1 ./...

vet:
	CGO_ENABLED=0 go vet ./...

fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal && exit 1)

lint: fmt-check vet

cross:
	@mkdir -p bin/cross
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o bin/cross/gax-$$os-$$arch$$ext ./cmd/gax || exit 1; \
	done

clean:
	rm -rf bin dist
