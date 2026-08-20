# Changelog

All notable changes to Ripen are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Ripen
follows [semantic versioning](https://semver.org/spec/v2.0.0.html): the
CLI verbs, the Response envelope, the Event envelope, and the MCP tools
are the public surface, and breaking any of them needs a major bump.

Each release's section here is what GitHub shows as the release notes.

## [Unreleased]

### Added

- The Go rewrite, replacing the Python updater: the same fail-closed
  Transaction, plus a compose-runtime backend, a versioned-JSON CLI and
  MCP agent surface, a webhook Notifier, and an optional read-only Web
  UI. See `docs/rework/SPEC.md` for the design and the behavior
  inventory it was verified against.
- Documentation for the whole surface: eight `docs/` pages, a rewritten README,
  `AGENTS.md`, `ROADMAP.md` with the permanent non-goals, and `CONTRIBUTING.md`.
- `CODE_OF_CONDUCT.md`: Contributor Covenant 2.1, linked from `CONTRIBUTING.md`.
  Enforcement reports go through GitHub's private reporting form for the
  repository rather than an email alias, and GitHub's own abuse reporting is
  named for anything that should not reach the maintainer.
- A `flake.nix`, so `nix run github:frankieramirez/ripen` builds Ripen from
  source. It exposes the `ripen` package and a `devShell` carrying the Go
  toolchain and golangci-lint, and CI builds it and checks what it reports on
  every push and pull request, so its `vendorHash` cannot go stale unnoticed.
  A release refuses a tag that disagrees with the version the flake carries.

### Changed

- `go.mod` asks for `go 1.26` rather than the exact patch `1.26.6`. Pinning the
  patch put the module's floor above the newest Go any distribution had
  packaged, which made every from-source install path unbuildable until they
  caught up. A floor is not a pin, so CI and the release now resolve the newest
  patch explicitly rather than building on whatever the runner had cached.

### Fixed

- Four faults in the release pipeline, each found by rehearsing a real release
  and none of them reachable by `goreleaser release --snapshot`, which
  authenticates to nothing, signs nothing and attests nothing. The release
  installed a cosign it could not verify; wrote its notes file into the work
  tree, which GoReleaser then refused as dirty; dropped the hand-written
  changelog section from the release body entirely; and left the archives
  without the build provenance the README tells people to check. Three are
  fixed. The fourth cannot be: GitHub does not persist provenance for
  user-owned private repositories, so a release is now refused before it
  publishes anything rather than after.

[Unreleased]: https://github.com/frankieramirez/ripen/compare/main...HEAD
