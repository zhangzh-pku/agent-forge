#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

tracked_artifacts="$(git ls-files -- 'bin/*' 'deploy/terraform/dist/*' || true)"

if [[ -n "${tracked_artifacts}" ]]; then
  echo "ERROR: tracked build artifacts detected:"
  echo "${tracked_artifacts}"
  echo
  echo "Remove tracked artifacts from git and keep generated files out of commits."
  exit 1
fi

echo "Build artifact audit passed."
