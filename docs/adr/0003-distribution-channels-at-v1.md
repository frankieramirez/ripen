# Distribution channels at `v1.0.0`

Status: accepted. Supersedes **in part** [ADR 0002](./0002-release-shape.md) — its channel list and its darwin-binary justification. Everything else in 0002 stands.

Strangers get Ripen from five channels at `v1.0.0`: GitHub Releases, GHCR, Docker Hub, `go install`, and the in-repo `flake.nix`. **There is no Homebrew tap.** The release mechanics ADR 0002 describes are unchanged: GoReleaser on an annotated `vX.Y.Z` tag on `main`, the same images under the same tags on both registries, hand-written `CHANGELOG.md` as the Release body, checksums, Syft SBOM, keyless cosign, `attest-build-provenance`, and semver as the compatibility promise after `v1.0.0`.

Full detail: [Drop the Homebrew channel and record the reversal](https://github.com/frankieramirez/ripen/issues/48), under the [release plumbing map](https://github.com/frankieramirez/ripen/issues/46).

A tap is a git repo whose name is load-bearing: `brew install frankieramirez/tap/ripen` resolves to `github.com/frankieramirez/homebrew-tap`. So the channel costs a second public repo to maintain and a long-lived cross-repo personal access token for GoReleaser to commit the cask with. That token would be the only credential in the pipeline able to push commits to a git repo, and it cannot be replaced by GitHub OIDC because the write lands in a different repository than the one running the workflow. With it gone, `DOCKERHUB_TOKEN` is the pipeline's sole long-lived secret, and it is scoped to a registry namespace. Everything else is keyless. macOS users get the archive from Releases or build with `go install`.

`darwin/amd64` and `darwin/arm64` binaries stay, on a different justification than ADR 0002 gave. ADR 0002 published them "so Homebrew installs without compiling", which no longer describes anything. The real reason is the operator who drives a remote NAS from a Mac: they run `ripen` on the laptop against a Portainer endpoint over the network, and asking them for a Go toolchain to do it is a worse ask than shipping two more archives from a matrix that already cross-compiles.

## Considered options

- **Keep the tap, accept the PAT.** Rejected. One credential that can push commits, held for the life of the project, guarding a convenience alias for `tar xzf`. The threat it adds is not proportional to the install step it saves.
- **Submit to homebrew-core instead of running a tap.** Rejected, for the same reason ADR 0002 rejected it at launch: homebrew-core has notability requirements Ripen does not meet yet, and lands the formula outside our control. It stays a post-launch question, not a `v1.0.0` channel.
- **Drop the darwin binaries along with the tap.** Rejected. The Mac operator is a real user of this tool even though Ripen only ever runs its Compose backend on Linux; see above.
- **Drop Docker Hub too, and cut `DOCKERHUB_TOKEN` as well.** Rejected. Self-hoster discovery happens on Docker Hub, and a token scoped to pushing one registry namespace is not comparable to one that can push commits to a git repo.

## Consequences

ADR 0002's "Considered options" are left exactly as written. The reasoning that made a tap look right — control over the formula while the release shape settles — was sound given the assumption that the tap was worth having at all; keeping the rejections readable is worth more than a tidy record. Only its channel list and its darwin justification are superseded.

`homebrew_casks` comes out of `.goreleaser.yaml` and `HOMEBREW_TAP_TOKEN` out of `.github/workflows/release.yaml`. The pre-public checklist no longer creates a tap repo in Phase 3 or verifies `brew install` in Phase 6, and the README quick start offers `go install` and a registry pull. Nothing about the fail-closed engine, the surfaces, or the state schema is touched by any of this.
