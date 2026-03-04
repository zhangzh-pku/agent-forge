#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

cleanup() {
  if [[ "${KEEP_UP:-0}" != "1" ]]; then
    docker compose down --remove-orphans >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

printf 'Starting AgentForge taskapi container...\n'
docker compose up -d --build taskapi >/dev/null

printf 'Waiting for /health...\n'
for _ in $(seq 1 60); do
  if curl -fsS http://localhost:8080/health >/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS http://localhost:8080/health >/dev/null

printf 'Creating task via /v1/tasks...\n'
create_resp="$(curl -fsS -X POST http://localhost:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-Id: demo-tenant' \
  -H 'X-User-Id: demo-user' \
  -d '{"prompt":"List the prime numbers under 20"}')"

if command -v jq >/dev/null 2>&1; then
  printf '%s\n' "${create_resp}" | jq .
else
  printf '%s\n' "${create_resp}"
fi

task_id="$(printf '%s' "${create_resp}" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')"

if [[ -n "${task_id}" ]]; then
  printf 'Fetching task state...\n'
  get_resp="$(curl -fsS http://localhost:8080/v1/tasks/${task_id} \
    -H 'X-Tenant-Id: demo-tenant' \
    -H 'X-User-Id: demo-user')"
  if command -v jq >/dev/null 2>&1; then
    printf '%s\n' "${get_resp}" | jq .
  else
    printf '%s\n' "${get_resp}"
  fi
fi

if [[ "${KEEP_UP:-0}" == "1" ]]; then
  printf 'Containers are still running (KEEP_UP=1).\n'
else
  printf 'Quickstart finished; containers will be stopped.\n'
fi
