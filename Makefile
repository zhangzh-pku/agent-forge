.PHONY: build build-lambda build-lambda-zip fmt fmt-check vet test test-race lint vuln artifact-audit iam-audit ci clean

GO ?= go

# Build all binaries into bin/
build:
	@mkdir -p bin
	$(GO) build -o bin/taskapi ./cmd/taskapi
	$(GO) build -o bin/worker ./cmd/worker
	$(GO) build -o bin/recovery ./cmd/recovery
	$(GO) build -o bin/wsconnect ./cmd/wsconnect
	$(GO) build -o bin/wsdisconnect ./cmd/wsdisconnect

# Build for AWS Lambda (Linux ARM64)
build-lambda:
	@mkdir -p bin
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/taskapi-bootstrap ./cmd/taskapi
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/worker-bootstrap ./cmd/worker
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/recovery-bootstrap ./cmd/recovery
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/wsconnect-bootstrap ./cmd/wsconnect
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/wsdisconnect-bootstrap ./cmd/wsdisconnect

# Build Lambda zip artifacts expected by deploy/terraform (dist/*.zip).
build-lambda-zip: build-lambda
	@mkdir -p deploy/terraform/dist
	@set -e; \
		root="$$(pwd)"; \
		pack() { \
			src="$$1"; out="$$2"; \
			tmp="$$(mktemp -d)"; \
			cp "$$src" "$$tmp/bootstrap"; \
			if command -v zip >/dev/null 2>&1; then \
				(cd "$$tmp" && zip -q -r "$$root/$$out" bootstrap); \
			else \
				(cd "$$tmp" && python3 -m zipfile -c "$$root/$$out" bootstrap >/dev/null); \
			fi; \
			rm -rf "$$tmp"; \
		}; \
		pack bin/taskapi-bootstrap deploy/terraform/dist/task_api.zip; \
		pack bin/worker-bootstrap deploy/terraform/dist/worker.zip; \
		pack bin/recovery-bootstrap deploy/terraform/dist/recovery.zip; \
		pack bin/wsconnect-bootstrap deploy/terraform/dist/ws_connect.zip; \
		pack bin/wsdisconnect-bootstrap deploy/terraform/dist/ws_disconnect.zip

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
	golangci-lint run ./...

# Dependency vulnerability checks
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Ensure generated artifacts are not tracked in git.
artifact-audit:
	./scripts/check-build-artifacts.sh

# Terraform IAM least-privilege invariants
iam-audit:
	./scripts/check-iam-least-privilege.sh

# CI checks
ci:
	$(MAKE) artifact-audit
	$(MAKE) iam-audit
	$(MAKE) lint
	$(MAKE) test
	$(MAKE) test-race
	$(MAKE) vuln

# Remove build artifacts
clean:
	rm -rf bin/
