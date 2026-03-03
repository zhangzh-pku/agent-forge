# Contributing to AgentForge

Thank you for your interest in contributing to AgentForge! This guide will help you get started.

## Prerequisites

- Go 1.24 or later
- Make
- Terraform 1.5+ (for infrastructure changes, optional)
- AWS CLI v2 (optional, only needed for production deployment)

## Getting Started

1. Fork the repository and clone your fork:

   ```bash
   git clone https://github.com/<your-username>/agent-forge.git
   cd agent-forge
   ```

2. Verify the build:

   ```bash
   make ci
   ```

## Building

Build all binaries:

```bash
make build
```

This compiles the Lambda handlers and CLI tools into the `bin/` directory.

## Running Tests

Run the full test suite:

```bash
make test
```

Run tests with race detection:

```bash
make test-race
```

Run only unit tests (no AWS credentials required):

```bash
go test -short ./...
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Before opening a PR, run:

  ```bash
  make fmt
  make ci
  ```

- Keep functions focused and well-documented. Exported functions must have doc comments.
- Error messages should be lowercase and should not end with punctuation.
- Prefer introducing configuration via environment variables under `pkg/config` instead of ad-hoc parsing in command entrypoints.

## Submitting a Pull Request

1. Create a feature branch from `main`:

   ```bash
   git checkout -b feature/my-change main
   ```

2. Make your changes in small, focused commits. Each commit should compile and pass tests.

3. Write or update tests to cover your changes.

4. Ensure all checks pass:

   ```bash
   make ci
   ```

5. Push your branch and open a pull request against `main`.

6. In your PR description, include:
   - **What** the change does
   - **Why** the change is needed
   - **How** you tested it

7. A maintainer will review your PR. Please respond to feedback promptly.

### PR Checklist (Required)

Before requesting review, confirm:

- [ ] `make ci` passed for Go/runtime changes
- [ ] Tests were added or updated for behavior changes
- [ ] Security impact was reviewed (authz/data exposure/dependencies)
- [ ] Docs/config were updated when interfaces or behavior changed

### Change Type -> Required Commands

| Change type | Required local checks |
|---|---|
| Go runtime/API/engine changes | `make ci` |
| Terraform (`deploy/terraform/**`) | `terraform -chdir=deploy/terraform fmt -check -recursive` and `terraform -chdir=deploy/terraform validate` |
| Docs-only changes | Verify referenced commands/paths exist in current repo |

## Terraform Changes

If your change modifies infrastructure in `deploy/terraform/`:

1. Run `terraform fmt` to format your configuration.
2. Run `terraform validate` to check for syntax errors.
3. Include `terraform plan` output in the PR description (with sensitive values redacted).

## Runtime Configuration

- Use `.env.example` as the baseline for local and aws runtime variables.
- `AGENTFORGE_RUNTIME=local` is default for development.
- `AGENTFORGE_RUNTIME=aws` requires DynamoDB/S3/SQS environment variables documented in `README.md`.

## Reporting Issues

- Use GitHub Issues to report bugs or request features.
- For security vulnerabilities, see [SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
