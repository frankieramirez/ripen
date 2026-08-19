#!/usr/bin/env sh
#
# The release's blob signing step. GoReleaser calls this, not cosign.
#
# GoReleaser's sign pipe never checks that the signing command wrote
# anything: it registers the signature artifact from the configured name
# and moves on. cosign v3.0.6 — the version sigstore/cosign-installer
# still defaults to — accepts the pre-v3 --output-signature and
# --output-certificate flags, warns, ignores them, and exits 0. The two
# together published a release advertising a signature and a certificate
# that were never written (#59).
#
# The flags are gone from the config now, but the hole they fell through
# is not: any cosign that exits 0 without writing would do it again. So
# there is no arrangement in which signing runs and the assertion does
# not — the assertion is the signing step. Nothing to skip, nothing to
# misconfigure, no separate CI step to reorder or drop.
#
# What is deliberately not asserted here: that the certificate identity
# is the release workflow. Verifying that needs a keyless certificate,
# which no local run has, and requiring it would make
# `goreleaser release --snapshot` — the fast gate that caught this —
# impossible to run outside CI. That check is the release rehearsal's.
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
