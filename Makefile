.PHONY: build build-lambda fmt fmt-check vet test test-race lint ci clean

GO ?= go

# Build all binaries into bin/
build:
	@mkdir -p bin
	$(GO) build -o bin/taskapi ./cmd/taskapi
	$(GO) build -o bin/worker ./cmd/worker
	$(GO) build -o bin/wsconnect ./cmd/wsconnect
	$(GO) build -o bin/wsdisconnect ./cmd/wsdisconnect

# Build for AWS Lambda (Linux ARM64)
build-lambda:
	@mkdir -p bin
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/taskapi-bootstrap ./cmd/taskapi
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/worker-bootstrap ./cmd/worker
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/wsconnect-bootstrap ./cmd/wsconnect
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/wsdisconnect-bootstrap ./cmd/wsdisconnect

# Format code
fmt:
	gofmt -w .

# Check formatting only (no write)
fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Run 'make fmt' to format files"; gofmt -l .; exit 1)

# Static checks
vet:
	$(GO) vet ./...

# Run tests
test:
	$(GO) test ./...

# Run tests with race detector
test-race:
	$(GO) test -race ./...

# Lint checks (non-mutating)
lint:
	$(MAKE) fmt-check
	$(MAKE) vet

# CI checks
ci:
	$(MAKE) lint
	$(MAKE) test

# Remove build artifacts
clean:
	rm -rf bin/
