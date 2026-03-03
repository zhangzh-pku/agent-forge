# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability in AgentForge, please report it responsibly. **Do not open a public GitHub issue.**

Instead, send an email to **security@agentforge.dev** with:

- A description of the vulnerability
- Steps to reproduce the issue
- The potential impact
- Any suggested fixes (optional)

We will acknowledge receipt within 48 hours and aim to provide a fix or mitigation plan within 7 business days.

## Encryption

### Data at Rest

All persistent data is encrypted at rest:

- **S3 artifacts** are encrypted using AWS KMS (SSE-KMS). Operators can supply a custom KMS key ARN or use the default `aws/s3` managed key.
- **DynamoDB tables** use AWS-managed encryption at rest (enabled by default on all tables).
- **SQS messages** are encrypted in transit via HTTPS endpoints. For at-rest encryption, enable SSE-SQS or SSE-KMS on the queue (configurable via Terraform variables).

### Data in Transit

- All API Gateway endpoints (HTTP and WebSocket) enforce TLS 1.2+.
- Internal AWS service calls use HTTPS endpoints exclusively.
- Lambda-to-service communication occurs within the AWS network over encrypted channels.

## Multi-Tenant Isolation

AgentForge supports multi-tenant deployments with the following isolation mechanisms:

- **Task-level isolation**: Each task runs in its own Lambda invocation with an isolated execution context. No shared in-memory state exists between tasks.
- **Data partitioning**: DynamoDB records use composite keys that include a tenant or task identifier as the partition key. IAM conditions or application-level checks prevent cross-tenant data access.
- **Artifact separation**: S3 object keys are prefixed with the task ID, ensuring each task's artifacts are stored under a distinct key prefix. Bucket policies can be extended with IAM condition keys for additional tenant-level restrictions.
- **Connection scoping**: WebSocket connections are associated with a specific task ID in the connections table. The GSI enables efficient lookup, and the worker only streams updates to connections that belong to the same task.

## Workspace and Path Safety

Agent workspaces are designed to prevent path traversal and unauthorized file access:

- **Sandboxed execution**: Agent tool operations are scoped to an isolated workspace directory. File paths provided by the agent are resolved relative to the workspace root and validated before use.
- **Path traversal prevention**: All file-path inputs are canonicalized and checked to ensure they do not escape the workspace boundary (e.g., `../` sequences are rejected).
- **No host filesystem access**: Lambda functions do not mount external volumes. The only writable storage is `/tmp` (ephemeral, per-invocation) and S3 (via the artifacts bucket).
- **Least-privilege IAM**: Each Lambda function is granted only the permissions it needs. The worker cannot modify infrastructure; the WebSocket handlers cannot access S3 or SQS.

## Dependency Management

- AgentForge currently depends on external modules (for example AWS SDK v2 packages).
- Go module checksums are verified via `go.sum`.
- Dependencies should be reviewed before adding. Run `go list -m -json all` to audit the dependency tree.
- CI pipelines should include `govulncheck` to detect known vulnerabilities in dependencies.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.x     | Yes       |

Security patches are applied to the latest release only.
