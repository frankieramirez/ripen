#!/usr/bin/env bash
set -euo pipefail

emit() {
  echo "go=$1" >> "$GITHUB_OUTPUT"
  echo "site=$2" >> "$GITHUB_OUTPUT"
  echo "go=$1 site=$2"
  exit 0
}

if [ "${GITHUB_EVENT_NAME:-}" != "pull_request" ]; then
  echo "$GITHUB_EVENT_NAME is not a pull request; running everything"
  emit true true
fi

if [ -z "${BASE:-}" ]; then
  echo "no base sha to diff against; running everything" >&2
  emit true true
fi

if ! files="$(git diff --name-only "$BASE" HEAD)"; then
  echo "could not diff against $BASE; running everything" >&2
  emit true true
fi

if [ -z "$files" ]; then
  echo "no files changed; running everything" >&2
  emit true true
fi

echo "changed files:"
sed 's/^/  /' <<< "$files"

go=false
site=false
while IFS= read -r file; do
  case "$file" in
    site/*) site=true ;;
    docs/* | CONTEXT.md)
      site=true
      go=true
      ;;
    .github/workflows/ci.yaml | .github/ci/changed-halves.sh)
      site=true
      go=true
      ;;
    *) go=true ;;
  esac
done <<< "$files"

emit "$go" "$site"
