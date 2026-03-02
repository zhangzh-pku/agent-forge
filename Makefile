.PHONY: build test test-race lint clean

# Build all binaries into bin/
build:
	@mkdir -p bin
	go build -o bin/taskapi ./cmd/taskapi
	go build -o bin/worker ./cmd/worker
	go build -o bin/wsconnect ./cmd/wsconnect
	go build -o bin/wsdisconnect ./cmd/wsdisconnect

# Build for AWS Lambda (Linux ARM64)
build-lambda:
	@mkdir -p bin
	GOOS=linux GOARCH=arm64 go build -o bin/taskapi-bootstrap ./cmd/taskapi
	GOOS=linux GOARCH=arm64 go build -o bin/worker-bootstrap ./cmd/worker
	GOOS=linux GOARCH=arm64 go build -o bin/wsconnect-bootstrap ./cmd/wsconnect
	GOOS=linux GOARCH=arm64 go build -o bin/wsdisconnect-bootstrap ./cmd/wsdisconnect

# Run tests
test:
	go test ./...

# Run tests with race detector
test-race:
	go test -race ./...

# Lint: format and vet
lint:
	gofmt -l -w .
	go vet ./...

# Remove build artifacts
clean:
	rm -rf bin/
