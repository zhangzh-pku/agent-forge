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

For staging/prod, pass real zip package paths for every Lambda.
Each zip must contain a Linux ARM64 `bootstrap` binary at archive root.

```bash
mkdir -p dist/lambda

GOOS=linux GOARCH=arm64 go build -o dist/lambda/taskapi/bootstrap ./cmd/taskapi
GOOS=linux GOARCH=arm64 go build -o dist/lambda/worker/bootstrap ./cmd/worker
GOOS=linux GOARCH=arm64 go build -o dist/lambda/recovery/bootstrap ./cmd/recovery
GOOS=linux GOARCH=arm64 go build -o dist/lambda/wsconnect/bootstrap ./cmd/wsconnect
GOOS=linux GOARCH=arm64 go build -o dist/lambda/wsdisconnect/bootstrap ./cmd/wsdisconnect

(cd dist/lambda/taskapi && zip -q -r ../taskapi.zip bootstrap)
(cd dist/lambda/worker && zip -q -r ../worker.zip bootstrap)
(cd dist/lambda/recovery && zip -q -r ../recovery.zip bootstrap)
(cd dist/lambda/wsconnect && zip -q -r ../wsconnect.zip bootstrap)
(cd dist/lambda/wsdisconnect && zip -q -r ../wsdisconnect.zip bootstrap)
```

## Apply (Dev)

`dev` allows placeholder Lambda packages for quick infrastructure bring-up.

```bash
cd deploy/terraform
terraform init
terraform plan -var='environment=dev'
terraform apply -var='environment=dev'
```

## Apply (Staging/Prod)

`staging` and `prod` require all `*_lambda_package_path` variables.
This prevents accidental placeholder deployments.

```bash
cd deploy/terraform
terraform init
terraform plan \
  -var='environment=staging' \
  -var='task_api_lambda_package_path=../../dist/lambda/taskapi.zip' \
  -var='worker_lambda_package_path=../../dist/lambda/worker.zip' \
  -var='recovery_lambda_package_path=../../dist/lambda/recovery.zip' \
  -var='ws_connect_lambda_package_path=../../dist/lambda/wsconnect.zip' \
  -var='ws_disconnect_lambda_package_path=../../dist/lambda/wsdisconnect.zip'
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
