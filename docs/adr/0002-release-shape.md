# Release shape: self-updated distribution on six channels

Status: accepted

Strangers get Ripen from GitHub Releases, GHCR, Docker Hub, Homebrew, Nix, or `go install`, and they update it themselves. **Ripen does not update Ripen.** Releases are cut by GoReleaser from an annotated `vX.Y.Z` tag on `main`, publishing `linux/amd64` and `linux/arm64` `FROM scratch` images to both `ghcr.io/frankieramirez/ripen` and `docker.io/frankieramirez/ripen` under the same tags (`vX.Y.Z`, `X.Y.Z`, `vX.Y`, `vX`, `latest`), plus those two platforms and `darwin/amd64`/`darwin/arm64` as static binaries so Homebrew installs without compiling.

Full detail: [Decide distribution and release mechanics](https://github.com/frankieramirez/ripen/issues/12).

Supporting choices: Homebrew ships through our own `frankieramirez/homebrew-tap`, updated by GoReleaser. Nix ships as an in-repo `flake.nix` building from source. `go install github.com/frankieramirez/ripen/cmd/ripen@vX.Y.Z` is a documented third path. The first public tag is `v1.0.0`, with pre-public work staying untagged or on `v0.x`; after `v1.0.0` semver is the compatibility promise and CLI or MCP breaks require a major bump. `CHANGELOG.md` is hand-written in Keep a Changelog format and published as the GitHub Release body. Supply-chain artifacts are checksums, a Syft SBOM, keyless cosign signatures via GitHub OIDC, and `attest-build-provenance`.

## Considered options

The rejections are the load-bearing part here, because each one is a thing a reasonable contributor will propose again:

- **No `curl | sh` installer.** Recorded as a flat no in #12, without a stated reason; do not read one in.
- **No Changesets.** It is a Node/JS monorepo tool and would put Node into a Go release pipeline. If per-PR changelog fragments become painful once contributors show up, switch to changie, not Changesets.
- **No homebrew-core submission at launch, and no AUR or scoop at launch.** Launching through our own tap keeps the release under our control while the shape settles.
- **No nixpkgs submission until after `v1.0.0` is public.** The in-repo flake covers Nix users at launch.
- **No Windows, no 32-bit ARM, no extra Linux arches, no PyPI or crates.io.** Outside the self-hoster audience or outside the stack.
- **No long-lived signing key, and no separate SLSA-generator workflow at launch.** Keyless cosign plus GitHub attestation covers it without key custody.
- **No nightly image and no Watchtower-on-Ripen story.** Compose examples and the README pin `vX.Y.Z` or a digest, which is the same fail-closed discipline Ripen asks of its users.

## Consequences

Publishing to two registries means GoReleaser OSS drives both through multiple `image_templates`. GoReleaser's Docker Hub description updater is Pro-only and is not required, so nothing in the pipeline depends on it. The Docker MCP Catalog listing goes in at public launch as the read-only container form only, and must not be filed as a stub: the listing PR into [docker/mcp-registry](https://github.com/docker/mcp-registry) waits on the Agent surface existing. The public `frankieramirez/homebrew-tap` repo gets created when GoReleaser is wired, per the [pre-public checklist](../rework/CHECKLIST.md).
