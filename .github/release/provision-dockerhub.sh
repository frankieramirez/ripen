#!/usr/bin/env bash

set -euo pipefail

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1 && [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
  BOLD=$(tput bold); DIM=$(tput dim); RESET=$(tput sgr0)
  BLUE=$(tput setaf 4); GREEN=$(tput setaf 2); YELLOW=$(tput setaf 3); RED=$(tput setaf 1)
else
  BOLD=""; DIM=""; RESET=""; BLUE=""; GREEN=""; YELLOW=""; RED=""
fi

TOTAL_STAGES=0

_STAGE_INDEX=0
ENV_FILE="${ENV_FILE:-.env}"
WRITTEN_ENV=()
WRITTEN_SECRET=()
SKIPPED=()

_clear() {
  [[ -t 1 ]] || return 0
  if command -v tput >/dev/null 2>&1; then tput clear; else printf '\033[2J\033[3J\033[H'; fi
}

banner() {
  _clear
  printf '\n%s%s  %s%s\n' "$BOLD" "$BLUE" "$1" "$RESET"
  printf '%s  %s stages%s\n\n' "$DIM" "$TOTAL_STAGES" "$RESET"
  printf '%s  You drive the browser; this wizard tells you exactly what to do and\n' "$DIM"
  printf '  captures the values you copy back. Stop any time with Ctrl-C and re-run\n'
  printf '  later — it remembers values already saved.%s\n' "$RESET"
  pause "Ready to start?"
}

stage() {
  _clear
  _STAGE_INDEX=$((_STAGE_INDEX + 1))
  printf '\n%s%s▸ Stage %s/%s · %s%s\n' \
    "$BOLD" "$BLUE" "$_STAGE_INDEX" "$TOTAL_STAGES" "$1" "$RESET"
}

say()  { printf '  %s\n' "$1"; }
step() { printf '  %s•%s %s\n' "$BLUE" "$RESET" "$1"; }
note() { printf '  %s%s%s\n' "$DIM" "$1" "$RESET"; }
warn() { printf '  %s⚠ %s%s\n' "$YELLOW" "$1" "$RESET"; }

open_url() {
  local url="$1"
  printf '  %s↗ opening%s %s\n' "$GREEN" "$RESET" "$url"
  { if   command -v wslview     >/dev/null 2>&1; then wslview "$url"
    elif command -v explorer.exe >/dev/null 2>&1; then explorer.exe "$url"
    elif command -v xdg-open    >/dev/null 2>&1; then xdg-open "$url"
    elif command -v open        >/dev/null 2>&1; then open "$url"
    else warn "couldn't open a browser — visit it manually: $url"; fi
  } >/dev/null 2>&1 || warn "couldn't open a browser — visit it manually: $url"
}

pause() {
  printf '  %s%s%s ' "$DIM" "${1:-Press Enter to continue}" "$RESET"
  read -r _ || true
}

confirm() {
  local reply=""
  printf '  %s? %s [y/N] ' "$YELLOW" "$1"
  read -r reply || true
  [[ "$reply" =~ ^[Yy] ]]
}

_existing() {
  [[ -f "$ENV_FILE" ]] || return 1
  local line; line=$(grep -E "^${1}=" "$ENV_FILE" | tail -n1) || return 1
  printf '%s' "${line#*=}"
}

ask() {
  local key="$1" prompt="$2" current input
  current=$(_existing "$key" || true)
  if [[ -n "$current" ]]; then
    printf '  %s%s%s %s[Enter keeps current]%s ' "$BOLD" "$prompt" "$RESET" "$DIM" "$RESET"
  else
    printf '  %s%s%s ' "$BOLD" "$prompt" "$RESET"
  fi
  read -r input || true
  [[ -z "$input" && -n "$current" ]] && input="$current"
  printf -v "$key" '%s' "$input"
}

ask_secret() {
  local key="$1" prompt="$2" current input
  current=$(_existing "$key" || true)
  if [[ -n "$current" ]]; then
    printf '  %s%s%s %s[Enter keeps current]%s ' "$BOLD" "$prompt" "$RESET" "$DIM" "$RESET"
  else
    printf '  %s%s%s ' "$BOLD" "$prompt" "$RESET"
  fi
  read -rs input || true
  printf '\n'
  [[ -z "$input" && -n "$current" ]] && input="$current"
  printf -v "$key" '%s' "$input"
}

write_env() {
  local key="$1" value="$2" tmp
  touch "$ENV_FILE"
  tmp=$(mktemp)
  grep -vE "^${key}=" "$ENV_FILE" > "$tmp" || true
  printf '%s=%s\n' "$key" "$value" >> "$tmp"
  mv "$tmp" "$ENV_FILE"
  WRITTEN_ENV+=("$key")
  printf '  %s✓ wrote%s %s → %s\n' "$GREEN" "$RESET" "$key" "$ENV_FILE"
}

set_secret() {
  local name="$1" value="$2"
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    if printf '%s' "$value" | gh secret set "$name" >/dev/null 2>&1; then
      WRITTEN_SECRET+=("$name")
      printf '  %s✓ set%s GitHub secret %s\n' "$GREEN" "$RESET" "$name"
      return
    fi
  fi
  SKIPPED+=("GitHub secret $name (set it manually: gh secret set $name)")
  warn "skipped GitHub secret $name — gh not ready; set it later"
}

set_var() {
  local name="$1" value="$2"
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    if gh variable set "$name" --body "$value" >/dev/null 2>&1; then
      printf '  %s✓ set%s GitHub variable %s\n' "$GREEN" "$RESET" "$name"
      return
    fi
  fi
  SKIPPED+=("GitHub variable $name")
  warn "skipped GitHub variable $name — gh not ready; set it later"
}

finish() {
  _clear
  printf '\n%s%s  ✓ Setup complete%s\n' "$BOLD" "$GREEN" "$RESET"
  (( ${#WRITTEN_ENV[@]} ))    && note "wrote ${#WRITTEN_ENV[@]} value(s) to $ENV_FILE: ${WRITTEN_ENV[*]}"
  (( ${#WRITTEN_SECRET[@]} )) && note "set ${#WRITTEN_SECRET[@]} GitHub secret(s): ${WRITTEN_SECRET[*]}"
  if (( ${#SKIPPED[@]} )); then
    printf '\n'; warn "still to do by hand:"
    for s in "${SKIPPED[@]}"; do note "  - $s"; done
  fi
  printf '\n'
}

ENV_FILE=/dev/null

DOCKERHUB_REPO=ripen
DOCKERHUB_DESCRIPTION="Ripen, a fail-closed image updater"
GITHUB_REPO=frankieramirez/ripen
CHECK_TAG=provision-check

HUB_JWT=""
REPO_JSON=""
REPO_IS_PRIVATE=""

fail() {
	printf '  %s✗ %s%s\n\n' "$RED" "$1" "$RESET" >&2
	exit 1
}

hub_login_body() {
	jq -nc --arg u "$DOCKERHUB_USERNAME" --arg s "$DOCKERHUB_TOKEN" \
		"{\"$1\": \$u, \"$2\": \$s}"
}

hub_jwt() {
	local response
	response=$(hub_login_body identifier secret | curl -fsS -X POST \
		-H 'Content-Type: application/json' --data @- \
		https://hub.docker.com/v2/auth/token) || response=""
	HUB_JWT=$(printf '%s' "$response" | jq -r '.access_token // empty' 2>/dev/null || true)
	if [[ -z "$HUB_JWT" ]]; then
		response=$(hub_login_body username password | curl -fsS -X POST \
			-H 'Content-Type: application/json' --data @- \
			https://hub.docker.com/v2/users/login) || response=""
		HUB_JWT=$(printf '%s' "$response" | jq -r '.token // empty' 2>/dev/null || true)
	fi
	[[ -n "$HUB_JWT" ]]
}

hub_get() {
	curl -fsS -H "Authorization: Bearer $HUB_JWT" "https://hub.docker.com$1"
}

read_repo() {
	REPO_JSON=$(hub_get "/v2/repositories/$DOCKERHUB_USERNAME/$DOCKERHUB_REPO/") || return 1
	REPO_IS_PRIVATE=$(printf '%s' "$REPO_JSON" | jq -r '.is_private')
}

TOTAL_STAGES=6

banner "Docker Hub — private repository and release token"

warn "Nothing is saved to disk between runs. If you stop partway, start again"
note "from the top; the stages that are already done detect themselves."
pause "Press Enter to continue"

stage "Preflight"
say "Checking the tools this needs, and where it is pointed."

for tool in docker gh curl jq; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed; this wizard needs docker, gh, curl and jq"
done
note "docker, gh, curl, jq — all present"

gh auth status >/dev/null 2>&1 || fail "gh is not authenticated; run: gh auth login"

repo=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)
[[ "$repo" == "$GITHUB_REPO" ]] || fail "run this from a $GITHUB_REPO checkout; gh sees '${repo:-nothing}'"
note "GitHub repo: $repo"

root=$(git rev-parse --show-toplevel)
workflow="$root/.github/workflows/release.yaml"
for name in DOCKERHUB_USERNAME DOCKERHUB_TOKEN; do
	grep -q "secrets\.$name" "$workflow" \
		|| fail "release.yaml no longer reads secrets.$name; update this wizard before running it"
done
note "release.yaml reads DOCKERHUB_USERNAME and DOCKERHUB_TOKEN"

existing=$(gh secret list 2>/dev/null | grep '^DOCKERHUB_' || true)
if [[ -n "$existing" ]]; then
	warn "DOCKERHUB_* secrets already exist — treating this run as a rotation"
	note "The old token keeps working until you delete it in stage 6, which is the"
	note "order you want. If Docker Hub refuses to issue a second token while the"
	note "first exists, delete the old one first and accept the gap: the only thing"
	note "it breaks is a v* tag pushed before this run finishes."
fi

ask DOCKERHUB_USERNAME "Docker Hub username [frankieramirez]:"
DOCKERHUB_USERNAME=${DOCKERHUB_USERNAME:-frankieramirez}
pause "Press Enter to continue"

stage "Confirm the private repository allowance"
say "A Docker Personal account includes exactly one private repository."
say "If it is already spent, $DOCKERHUB_USERNAME/$DOCKERHUB_REPO would be created public."
open_url "https://hub.docker.com/repositories/$DOCKERHUB_USERNAME"
step "Look down the list for the 'Private' badge."
note "Personal = 1 private repo. Pro and above are unlimited."

if confirm "Does $DOCKERHUB_USERNAME/$DOCKERHUB_REPO already exist?"; then
	REPO_EXISTS=yes
	note "skipping creation; stage 5 still proves it is private"
else
	REPO_EXISTS=no
	confirm "Is the private slot free?" || fail "the private slot is spent; free it or upgrade the plan before provisioning ripen"
fi

stage "Create the repository — private"
if [[ "$REPO_EXISTS" == yes ]]; then
	note "$DOCKERHUB_USERNAME/$DOCKERHUB_REPO already exists — nothing to create"
	pause "Press Enter to continue"
else
	open_url "https://hub.docker.com/repository/create"
	step "Namespace: $DOCKERHUB_USERNAME"
	step "Repository Name: $DOCKERHUB_REPO"
	step "Short description: $DOCKERHUB_DESCRIPTION"
	step "Visibility: Private"
	warn "Public is the default selection. Change it before you click Create."
	note "The description is the naming discipline from #19: never a bare 'ripen',"
	note "which collides with an unrelated PyPI package. Max 100 characters."
	note "A repository cannot be renamed after it is created."
	pause "Created it, set to Private? Press Enter"
fi

stage "Generate the access token"
say "One token, used by one consumer: the release workflow."
open_url "https://app.docker.com/settings/personal-access-tokens/create"
step "Description: ripen release workflow (github actions)"
step "Expiration: 365 days"
step "Access permissions: Read & Write. Not Delete — pushing is all this needs."
warn "Whatever you pick, this token reaches every repository the account owns."
note "Scoping a token to one repository is a Pro/Team feature, so account-wide"
note "is the floor here. Only the permission level is yours to choose, and if"
note "the page offers no choice at all, what you get is read/write/delete."
note "docs/release-credentials.md records the scope this actually got."
warn "Docker Hub shows the token once. Copy it before leaving the page."
ask_secret DOCKERHUB_TOKEN "Paste the token:"
[[ -n "$DOCKERHUB_TOKEN" ]] || fail "no token entered; re-run once you have one"

stage "Prove it: the token pushes, and the repository is private"
say "Nothing is assumed here. Each claim is checked before the next runs."

printf '%s' "$DOCKERHUB_TOKEN" | docker login --username "$DOCKERHUB_USERNAME" --password-stdin >/dev/null 2>&1 \
	|| fail "docker login failed; the username or the token is wrong"
note "docker login: ok"

hub_jwt || fail "the Docker Hub API rejected the token; it cannot be used to check visibility"
read_repo || fail "$DOCKERHUB_USERNAME/$DOCKERHUB_REPO does not exist — go back to stage 3 and create it"

if [[ "$REPO_IS_PRIVATE" != "true" ]]; then
	warn "$DOCKERHUB_USERNAME/$DOCKERHUB_REPO is PUBLIC."
	open_url "https://hub.docker.com/repository/docker/$DOCKERHUB_USERNAME/$DOCKERHUB_REPO/settings"
	step "Settings → Visibility → Make private."
	pause "Made it private? Press Enter to re-check"
	read_repo || fail "could not re-read the repository"
	[[ "$REPO_IS_PRIVATE" == "true" ]] || fail "still public; refusing to push into a repository the whole internet can pull"
fi
note "visibility: private"

description=$(printf '%s' "$REPO_JSON" | jq -r '.description // ""')
if [[ "$description" != "$DOCKERHUB_DESCRIPTION" ]]; then
	warn "description is '${description:-empty}', not '$DOCKERHUB_DESCRIPTION'"
	open_url "https://hub.docker.com/repository/docker/$DOCKERHUB_USERNAME/$DOCKERHUB_REPO/general"
	step "Set the short description to: $DOCKERHUB_DESCRIPTION"
	pause "Press Enter to continue"
else
	note "description: $DOCKERHUB_DESCRIPTION"
fi

say "Pushing a throwaway image to prove the token can write. It goes into the"
say "real repository — there is nowhere else to put it, since the account's one"
say "private slot is this repository. It is private, and the tag comes off next."
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
printf 'provisioned by .github/release/provision-dockerhub.sh\n' > "$build_dir/provision-check.txt"
printf 'FROM scratch\nCOPY provision-check.txt /provision-check.txt\n' > "$build_dir/Dockerfile"
docker build -q -t "$DOCKERHUB_USERNAME/$DOCKERHUB_REPO:$CHECK_TAG" "$build_dir" >/dev/null 2>&1 \
	|| fail "could not build the throwaway image"
docker push "$DOCKERHUB_USERNAME/$DOCKERHUB_REPO:$CHECK_TAG" >/dev/null 2>&1 \
	|| fail "push refused; the token cannot write to $DOCKERHUB_USERNAME/$DOCKERHUB_REPO"
note "push: ok — the release workflow will be able to publish images"

if curl -fs -X DELETE -H "Authorization: Bearer $HUB_JWT" \
	"https://hub.docker.com/v2/repositories/$DOCKERHUB_USERNAME/$DOCKERHUB_REPO/tags/$CHECK_TAG/" >/dev/null 2>&1; then
	note "removed the $CHECK_TAG tag"
else
	say "The token cannot delete — as intended. Remove the tag yourself:"
	open_url "https://hub.docker.com/repository/docker/$DOCKERHUB_USERNAME/$DOCKERHUB_REPO/tags"
	step "Delete the '$CHECK_TAG' tag."
	SKIPPED+=("delete the $CHECK_TAG tag on Docker Hub")
	pause "Deleted it? Press Enter to continue"
fi

docker image rm "$DOCKERHUB_USERNAME/$DOCKERHUB_REPO:$CHECK_TAG" >/dev/null 2>&1 || true

docker logout >/dev/null 2>&1 || true
note "logged out — the token is not left in the local credential store"

stage "Hand the token to the release workflow"
say "These two names are read by .github/workflows/release.yaml. They must"
say "match exactly, or the Docker Hub login step fails at tag time."
set_secret DOCKERHUB_USERNAME "$DOCKERHUB_USERNAME"
set_secret DOCKERHUB_TOKEN "$DOCKERHUB_TOKEN"

missing=""
for name in DOCKERHUB_USERNAME DOCKERHUB_TOKEN; do
	gh secret list 2>/dev/null | grep -q "^$name" || missing="$missing $name"
done
[[ -z "$missing" ]] || fail "gh secret list does not show:$missing — set them by hand before tagging"
note "gh secret list shows both secrets"
printf '\n' 
step "If this was a rotation, delete the old token now:"
note "https://app.docker.com/settings/personal-access-tokens"
note "If Docker Hub refused to issue a second token while the old one existed,"
note "you deleted it before stage 4 and there is nothing left to do here."
pause "Press Enter to finish"

finish
note "The expiry lives on Docker Hub's token page — there is no second copy"
note "to drift. Rotation: docs/release-credentials.md"
printf '\n'
