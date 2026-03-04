# Terraform Deployment Guide

This guide covers safe deployment with `deploy/terraform`.

## What This Module Creates

- SQS primary queue + DLQ
- DynamoDB tables: tasks, runs, steps, connections
- S3 artifact bucket
- Lambda functions: `task_api`, `worker`, `recovery`, `ws_connect`, `ws_disconnect`
- API Gateway HTTP + WebSocket APIs
- CloudWatch alarms/dashboard
- Optional WAF and API authorizers

## Build Real Lambda Packages

Terraform no longer supports placeholder Lambda packages.
Build real artifacts before `plan/apply`:

```bash
make build-lambda-zip
```

Default artifacts are written to:
- `deploy/terraform/dist/task_api.zip`
- `deploy/terraform/dist/worker.zip`
- `deploy/terraform/dist/recovery.zip`
- `deploy/terraform/dist/ws_connect.zip`
- `deploy/terraform/dist/ws_disconnect.zip`

Each zip must contain a Linux ARM64 `bootstrap` binary at archive root.

## Apply (Dev)

`dev` also requires real Lambda packages.

```bash
make build-lambda-zip
cd deploy/terraform
terraform init
terraform plan -var='environment=dev'
terraform apply -var='environment=dev'
```

## Apply (Staging/Prod)

`staging` and `prod` require valid package files.
Defaults under `deploy/terraform/dist/` can be used directly.

```bash
make build-lambda-zip
cd deploy/terraform
terraform init
terraform plan \
  -var='environment=staging' \
  -var='task_api_lambda_package_path=dist/task_api.zip' \
  -var='worker_lambda_package_path=dist/worker.zip' \
  -var='recovery_lambda_package_path=dist/recovery.zip' \
  -var='ws_connect_lambda_package_path=dist/ws_connect.zip' \
  -var='ws_disconnect_lambda_package_path=dist/ws_disconnect.zip'
```

## OpenAI API Key via Secrets Manager

If `taskapi`/`worker` should resolve OpenAI API key from Secrets Manager:

```bash
terraform plan \
  -var='openai_api_key_secret_arn=arn:aws:secretsmanager:us-east-1:123456789012:secret:agentforge/openai-api-key-xxxxxx' \
  -var='openai_api_key_secret_field=OPENAI_API_KEY'
```

This sets Lambda env vars:
- `OPENAI_API_KEY_SECRET_ARN`
- `OPENAI_API_KEY_SECRET_FIELD`

and grants `secretsmanager:GetSecretValue` / `DescribeSecret` to task_api + worker roles.

## Security Toggles

- `waf_enabled=true` enables AWS WAF on HTTP API stage.
- `http_jwt_authorizer_enabled=true` enables JWT authorizer on HTTP API.
- `websocket_authorizer_enabled=true` enables REQUEST authorizer on WebSocket `$connect`.

## CI Checks

Terraform checks run in `.github/workflows/terraform.yml`:
- `terraform fmt -check`
- `terraform validate`
- `tflint`
- `tfsec`
