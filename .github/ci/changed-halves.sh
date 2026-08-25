#!/usr/bin/env bash
set -euo pipefail

# Decides which halves of ci.yaml run for a pull request, and writes `go` and
# `site` to $GITHUB_OUTPUT.
#
# The bias is one-way. Running a job that did not need to run costs a few
# minutes; skipping one that did means a green pull request that broke
# something. So every branch that is not certain resolves to running
# everything -- a push, a missing base sha, a diff that fails.

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
    # The docs pipeline globs docs/*.md and CONTEXT.md into the site, so a
    # docs edit can break the site build. It also runs the Go jobs: a test
    # asserts `ripen schema` and docs/schema/v1/ are identical.
    docs/* | CONTEXT.md)
      site=true
      go=true
      ;;
    # This file and the workflow that calls it decide what runs, so a change
    # to either is exercised by running both halves.
    .github/workflows/ci.yaml | .github/ci/changed-halves.sh)
      site=true
      go=true
      ;;
    *) go=true ;;
  esac
done <<< "$files"

emit "$go" "$site"
