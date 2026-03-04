#!/usr/bin/env bash
set -euo pipefail

file="deploy/terraform/main.tf"

if [[ ! -f "$file" ]]; then
  echo "missing terraform file: $file" >&2
  exit 1
fi

extract_policy_block() {
  local policy_name="$1"
  awk -v name="$policy_name" '
    $0 ~ ("data \"aws_iam_policy_document\" \"" name "\"[[:space:]]*\\{") {
      in_block=1
      depth=0
    }
    in_block {
      print
      depth += gsub(/\{/, "{")
      depth -= gsub(/\}/, "}")
      if (depth == 0) {
        exit
      }
    }
  ' "$file"
}

assert_contains() {
  local content="$1"
  local needle="$2"
  local message="$3"
  if ! grep -Fq "\"$needle\"" <<<"$content"; then
    echo "IAM audit failed: $message" >&2
    exit 1
  fi
}

assert_not_contains() {
  local content="$1"
  local needle="$2"
  local message="$3"
  if grep -Fq "\"$needle\"" <<<"$content"; then
    echo "IAM audit failed: $message" >&2
    exit 1
  fi
}

task_api_policy="$(extract_policy_block task_api_policy)"
worker_policy="$(extract_policy_block worker_policy)"
recovery_policy="$(extract_policy_block recovery_policy)"

if [[ -z "$task_api_policy" || -z "$worker_policy" || -z "$recovery_policy" ]]; then
  echo "IAM audit failed: unable to locate required IAM policy blocks in $file" >&2
  exit 1
fi

assert_not_contains "$task_api_policy" "dynamodb:Scan" "task_api policy must not include dynamodb:Scan"
assert_not_contains "$task_api_policy" "s3:ListBucket" "task_api policy must not include s3:ListBucket"
assert_contains "$task_api_policy" "dynamodb:TransactWriteItems" "task_api policy must include dynamodb:TransactWriteItems"
assert_contains "$task_api_policy" "dynamodb:BatchWriteItem" "task_api policy must include dynamodb:BatchWriteItem"

assert_not_contains "$worker_policy" "dynamodb:Scan" "worker policy must not include dynamodb:Scan"
assert_not_contains "$worker_policy" "s3:ListBucket" "worker policy must not include s3:ListBucket"
assert_not_contains "$worker_policy" "s3:DeleteObject" "worker policy must not include s3:DeleteObject"
assert_contains "$worker_policy" "sqs:ChangeMessageVisibility" "worker policy must include sqs:ChangeMessageVisibility"

assert_not_contains "$recovery_policy" "dynamodb:Scan" "recovery policy must not include dynamodb:Scan"

echo "IAM least-privilege audit passed."
