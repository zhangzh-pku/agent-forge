#!/usr/bin/env bash
set -euo pipefail

awslocal sqs create-queue \
  --queue-name agentforge-tasks-local \
  --attributes VisibilityTimeout=300,ReceiveMessageWaitTimeSeconds=20 \
  >/dev/null

awslocal dynamodb create-table \
  --table-name agentforge-tasks-local \
  --attribute-definitions \
    AttributeName=pk,AttributeType=S \
    AttributeName=sk,AttributeType=S \
    AttributeName=gsi1pk,AttributeType=S \
    AttributeName=gsi1sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
  --global-secondary-indexes '[{"IndexName":"tenant-created-index","KeySchema":[{"AttributeName":"gsi1pk","KeyType":"HASH"},{"AttributeName":"gsi1sk","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}]' \
  --billing-mode PAY_PER_REQUEST \
  >/dev/null

awslocal dynamodb create-table \
  --table-name agentforge-runs-local \
  --attribute-definitions \
    AttributeName=pk,AttributeType=S \
    AttributeName=sk,AttributeType=S \
    AttributeName=gsi1pk,AttributeType=S \
    AttributeName=gsi1sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
  --global-secondary-indexes '[{"IndexName":"tenant-run-index","KeySchema":[{"AttributeName":"gsi1pk","KeyType":"HASH"},{"AttributeName":"gsi1sk","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}]' \
  --billing-mode PAY_PER_REQUEST \
  >/dev/null

awslocal dynamodb create-table \
  --table-name agentforge-steps-local \
  --attribute-definitions \
    AttributeName=pk,AttributeType=S \
    AttributeName=sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST \
  >/dev/null

awslocal dynamodb create-table \
  --table-name agentforge-connections-local \
  --attribute-definitions \
    AttributeName=pk,AttributeType=S \
    AttributeName=sk,AttributeType=S \
    AttributeName=gsi1pk,AttributeType=S \
    AttributeName=gsi1sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
  --global-secondary-indexes '[{"IndexName":"task-index","KeySchema":[{"AttributeName":"gsi1pk","KeyType":"HASH"},{"AttributeName":"gsi1sk","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}]' \
  --billing-mode PAY_PER_REQUEST \
  >/dev/null

awslocal dynamodb update-time-to-live \
  --table-name agentforge-steps-local \
  --time-to-live-specification Enabled=true,AttributeName=ttl \
  >/dev/null

awslocal dynamodb update-time-to-live \
  --table-name agentforge-connections-local \
  --time-to-live-specification Enabled=true,AttributeName=ttl \
  >/dev/null

awslocal s3api create-bucket --bucket agentforge-artifacts-local >/dev/null

echo "LocalStack init completed for AgentForge"
