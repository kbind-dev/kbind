#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$REPO_ROOT/docs"

if [[ "${GITHUB_EVENT_NAME:-}" == "pull_request" ]]; then
  echo "Pull requests must build documentation without deploying." >&2
  exit 1
fi

remote=${REMOTE:-origin}
branch=${BRANCH:-gh-pages}
options=(--remote "$remote" --branch "$branch")

if [[ "${PUSH:-0}" == "1" ]]; then
  options+=(--push)
fi

git fetch "$remote" "$branch"
message=${DOCS_COMMIT_MESSAGE:-Publish v2 documentation preview}
mike deploy "${options[@]}" --message "$message" --title "v2 (preview)" v2
