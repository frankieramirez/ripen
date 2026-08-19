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

[Unreleased]: https://github.com/frankieramirez/ripen/compare/main...HEAD
