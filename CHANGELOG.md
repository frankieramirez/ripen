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

[Unreleased]: https://github.com/frankieramirez/ripen/compare/main...HEAD
