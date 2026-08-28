#!/usr/bin/env sh
set -eu

artifact=${1:?usage: sign-blob.sh <artifact> <bundle>}
bundle=${2:?usage: sign-blob.sh <artifact> <bundle>}

cosign sign-blob --bundle "$bundle" --yes "$artifact"

if [ ! -s "$bundle" ]; then
	echo "cosign exited 0 but wrote no bundle to $bundle; refusing to release an unsigned artifact" >&2
	exit 1
fi

if ! grep -q 'application/vnd\.dev\.sigstore\.bundle' "$bundle"; then
	echo "$bundle is not a Sigstore bundle; refusing to release an artifact nobody can verify" >&2
	exit 1
fi

if ! grep -q '"messageSignature"' "$bundle"; then
	echo "$bundle carries no message signature; refusing to release an artifact nobody can verify" >&2
	exit 1
fi
