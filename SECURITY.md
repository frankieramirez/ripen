# Security policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
vulnerability reporting for this repository.

Include the affected version, reproduction steps, and impact. Do not include real
Portainer API keys, GitHub tokens, webhook URLs, Compose environment secrets, or
anything else from a private network.

## What Ripen can do, and what that means

Ripen can redeploy the stacks its policy names. Treat its policy file, its
credential files, its state database, and the networks it sits on as
infrastructure with the same sensitivity as the services it updates.

- **Start in monitor mode.** Read what it records before any stack carries
  `auto_apply: true`.
- **Give the Portainer automation user access to only the stacks Ripen may
  update.** It is checked against `expected_username` on every run, before any
  inventory work, and a mismatch stops the run.
- **Do not mount the privileged Docker socket.** Ripen never asks for one, and a
  configured Compose socket that resolves to `/var/run/docker.sock` or
  `/run/docker.sock` — through any symlink chain — refuses at config load.
- **Mount every secret from a read-only file.** API keys, GitHub tokens, webhook
  URLs and tokens, and the UI token are all file paths. None of them belong in
  the policy document, in Compose, or in Git.
- **Use HTTPS with a CA file or an exact certificate fingerprint.** There is no
  insecure mode.

## Credential handling

| Secret | Requirements |
| --- | --- |
| Portainer API key | Mode `0600`. Rejected at startup if it contains interior whitespace. |
| GitHub token | Mode `0600` — a file readable by group or others is refused. Fine-grained, one repository, Metadata read plus Contents and Pull requests read/write. Nothing more. |
| Webhook URL and token | Files. The URL must be https unless it points at this host. Only a hash of the destination is stored, and it never appears in a failure report. |
| Web UI token | A file or `RIPEN_UI_TOKEN`. Required for any bind that is not loopback. |

Nothing Ripen emits carries a secret. Event payloads are a closed set of fields
with nowhere to put one, and a test walks that type to keep it so.

## Surface boundaries

- **`ripen mcp`** is read-only by default: the write tools are never registered
  and no network client is built, so the process holds no credentials at all.
  With `--enable-writes` it gains exactly three tools — a Monitor cycle, opening
  a Proposal, and clearing a Proposal. Applying an update and clearing the
  Circuit breaker have no MCP tool in either mode.
- **The Web UI** is off unless enabled, reads only, and offers nothing
  clickable that changes state. A non-loopback bind requires a bearer token.
  `/healthz` is the only unauthenticated route and returns nothing but `ok`.
- **The daemon** writes nothing to stdout; its Event stream goes to stderr.

## Supply chain

Releases are built by GitHub Actions from a tag and published with checksums, an
SBOM per archive, a keyless cosign signature over the checksum file and one on
every image, and GitHub build provenance. The blob signature ships as a Sigstore
bundle, `ripen_<version>_checksums.txt.sigstore.json`, holding the signature, the
Fulcio certificate and the transparency-log proof in one file; verifying it needs
cosign v3. The release notes carry the exact `cosign verify-blob` invocation.

Only the checksum file is signed, and that is the whole chain: it lists every
archive and every SBOM in the release, so one verified signature plus a checksum
check covers all of them. Signing each archive separately would add certificates
to verify, not assurance.

A release cannot be published unsigned. GoReleaser does not check that a signing
command wrote anything, so signing runs through a wrapper that refuses when
cosign produces no bundle; there is no arrangement in which the step is skipped
quietly.

Images are `FROM scratch` with a CA bundle and the binary — no shell, no package
manager, a non-root uid.

The provenance covers what the Release carries rather than what it was built
from: the four archives and the checksums file. So an archive verifies as it was
downloaded, with nothing to extract or hash first.

```bash
gh attestation verify ripen_<version>_linux_amd64.tar.gz --repo frankieramirez/ripen
```

The checksums file names every archive and every SBOM by digest, so attesting it
reaches those too. The images carry no provenance attestation — the cosign
signature on each manifest is the claim there.

### The Nix channel trails on standard-library patches

The four archive channels are built with the newest Go patch release. The flake
is built with whatever Go nixpkgs ships, and nixpkgs moves a Go bump through a
rebuild queue that has historically taken two to three weeks to reach
`nixos-unstable`. So a binary from `nix run` can carry standard-library
vulnerabilities that the same version downloaded as an archive does not.

This is a standing lag rather than a one-off, and it is accepted rather than
worked around: pinning a newer toolchain by hand goes stale inside the same
fortnight and then holds the channel *behind* nixpkgs instead of ahead of it.
What is not accepted is not knowing. CI scans the binary the flake builds on
every push, and refuses anything not already named in
[`.github/nix-vuln-baseline.txt`](.github/nix-vuln-baseline.txt), which records
what the channel currently carries and why each entry was judged tolerable.

To check for yourself:

```bash
nix build github:frankieramirez/ripen
govulncheck -mode=binary ./result/bin/ripen
```

If that matters for your deployment, take an archive or an image instead — those
are built with the patched toolchain, and both are verifiable as described above.

## Supported versions

The latest release. Ripen is maintained for its author's own use; fixes land on
`main` and go out in the next tag.
