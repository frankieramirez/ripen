#!/usr/bin/env bash
#
# Scans the binary the flake just built and compares what it carries against
# the recorded baseline.
#
# The Nix channel does not choose its own toolchain -- nixpkgs does -- so this
# binary can carry standard-library vulnerabilities the four GoReleaser
# channels do not, for as long as nixpkgs takes to land a Go patch (#69). That
# is accepted, and .github/nix-vuln-baseline.txt is the record of exactly what
# was accepted.
#
# What this refuses: anything the baseline does not already name. What it does
# not refuse: a vulnerability leaving the list. That direction is the fix
# arriving, and a red build is a poor reward for it -- so removals print
# loudly and pass, as the prompt to tighten the file.
set -euo pipefail

binary=${1:?usage: check-nix-vulnerabilities.sh <binary> <baseline>}
baseline=${2:?usage: check-nix-vulnerabilities.sh <binary> <baseline>}

report=$(mktemp)
trap 'rm -f "$report"' EXIT

# govulncheck exits 3 when it finds something, which is the expected case
# here, not an error. Any other non-zero is a real failure and must not be
# swallowed -- a scanner that cannot run has found nothing, which looks
# identical to a clean scan.
status=0
govulncheck -mode=binary -format=json "$binary" > "$report" || status=$?
if [ "$status" -ne 0 ] && [ "$status" -ne 3 ]; then
	echo "govulncheck exited $status; refusing to treat an unfinished scan as a clean one" >&2
	exit 1
fi

found=$(jq -r 'select(.finding) | .finding.osv' < "$report" | sort -u)
accepted=$(sed 's/#.*//' "$baseline" | tr -d '[:blank:]' | grep -v '^$' | sort -u || true)

echo "go toolchain:  $(go version "$binary" | awk '{print $2}')"

stamp=$(sed -n 's/^# last confirmed: *\([0-9-]*\).*/\1/p' "$baseline" | head -1)
if [ -n "$stamp" ]; then
	now=$(date -u +%s)
	then_=$(date -u -d "$stamp" +%s 2>/dev/null || date -u -j -f %Y-%m-%d "$stamp" +%s 2>/dev/null || echo "")
	if [ -n "$then_" ]; then
		age=$(( (now - then_) / 86400 ))
		[ "$age" = 1 ] && unit=day || unit=days
		echo "baseline:      $stamp ($age $unit old)"
	else
		echo "baseline:      $stamp"
	fi
fi
echo "accepted:      $(echo "$accepted" | grep -c . || true)"
echo "found:         $(echo "$found" | grep -c . || true)"

gone=$(comm -23 <(echo "$accepted") <(echo "$found"))
if [ -n "$gone" ]; then
	echo
	echo "these are no longer present and should be removed from $baseline:"
	echo "$gone" | sed 's/^/  /'
	echo "if the list is now empty, #69 is done -- keep the file so the gate stays armed."
fi

new=$(comm -13 <(echo "$accepted") <(echo "$found"))
if [ -n "$new" ]; then
	echo >&2
	echo "the nix channel carries vulnerabilities the baseline does not accept:" >&2
	echo "$new" | sed 's/^/  /' >&2
	echo >&2
	echo "each one needs a decision, not an addition to the file. see #69 for how" >&2
	echo "the accepted ones were judged: reachability first, then who has to be" >&2
	echo "malicious for it to fire." >&2
	exit 1
fi

echo
echo "no vulnerabilities outside the baseline"
