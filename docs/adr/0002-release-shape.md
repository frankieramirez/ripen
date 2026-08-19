# Release shape: self-updated distribution on six channels

Status: accepted

Strangers get Ripen from GitHub Releases, GHCR, Docker Hub, Homebrew, Nix, or `go install`, and they update it themselves. **Ripen does not update Ripen.** Releases are cut by GoReleaser from an annotated `vX.Y.Z` tag on `main`, publishing `linux/amd64` and `linux/arm64` `FROM scratch` images to both `ghcr.io/frankieramirez/ripen` and `docker.io/frankieramirez/ripen` under the same tags (`vX.Y.Z`, `X.Y.Z`, `vX.Y`, `vX`, `latest`), plus those two platforms and `darwin/amd64`/`darwin/arm64` as static binaries so Homebrew installs without compiling.

Full detail: [Decide distribution and release mechanics](https://github.com/frankieramirez/ripen/issues/12).

Supporting choices: Homebrew ships through our own `frankieramirez/homebrew-tap`, updated by GoReleaser. Nix ships as an in-repo `flake.nix` building from source. The first public tag is `v1.0.0`, after which semver is the compatibility promise and CLI or MCP breaks require a major bump. `CHANGELOG.md` is hand-written in Keep a Changelog format and published as the GitHub Release body. Supply-chain artifacts are checksums, a Syft SBOM, keyless cosign signatures via GitHub OIDC, and `attest-build-provenance`.

## Considered options

The rejections are the load-bearing part here, because each one is a thing a reasonable contributor will propose again:

- **No `curl | sh` installer.** Six channels already cover the audience, and a shell installer undercuts the verifiable-supply-chain posture.
- **No Changesets.** It is a Node/JS monorepo tool and would put Node into a Go release pipeline. If per-PR changelog fragments become painful once contributors show up, switch to changie, not Changesets.
- **No homebrew-core or nixpkgs submission at launch.** Both are post-`v1.0.0` follow-ups; launching through our own tap and flake keeps the release under our control while the shape settles.
- **No Windows, no 32-bit ARM, no extra Linux arches, no PyPI or crates.io.** Outside the self-hoster audience or outside the stack.
- **No long-lived signing key and no separate SLSA-generator workflow.** Keyless cosign plus GitHub attestation covers it without key custody.
- **No nightly image and no Watchtower-on-Ripen story.** Compose examples and the README pin `vX.Y.Z` or a digest, which is the same fail-closed discipline Ripen asks of its users.

## Consequences

Publishing to two registries means GoReleaser OSS drives both through multiple `image_templates`. The Docker Hub description updater is Pro-only, so Hub's description is maintained by hand. The Docker MCP Catalog listing is a launch-time item and must not be filed as a stub: it waits on the Agent surface existing. Creating the public `homebrew-tap` repo is what first exposes the project name publicly, so it happens when GoReleaser is wired, not before.
