# Rewrite Ripen in Go

Status: accepted

Ripen began as a Python updater. For the public relaunch we are rewriting it in **Go 1.26+**, with `gofmt`/`golangci-lint`, `govulncheck` in CI, and GoReleaser for release artifacts. Registry work uses `google/go-containerregistry` (or a deliberately minimal client differential-tested against it); the Portainer and GitHub clients plus TLS fingerprint pinning sit on stdlib `net/http` and `crypto/tls`; the Agent surface uses the official `modelcontextprotocol/go-sdk`.

Full rationale and scoring: [Decide the implementation stack](https://github.com/frankieramirez/ripen/issues/9), drawing on [stack research](https://github.com/frankieramirez/ripen/issues/7) and [agent-operability research](https://github.com/frankieramirez/ripen/issues/8).

## Considered options

Go scored 34, Rust 27, Python 21.

- **Go.** The only candidate with a mature OCI registry client ecosystem, so we stop hand-rolling the registry protocol. Its official MCP SDK has 8 direct dependencies and holds the only kept API-stability promise among the three MCP SDKs, which preserves the tiny-auditable-supply-chain identity even after adding an Agent surface. Static cross-compiled multi-arch binaries and `FROM scratch` images need no extra toolchain. Diun and Watchtower are Go, and the original Watchtower was archived in December 2025, so a polished Go entrant reads as the successor.
- **Rust.** Rejected. Its sole registry client is pre-1.0, and its MCP SDK lands at a several-hundred-crate lockfile. Its real remaining advantage was portfolio signal from the language name. Recorded counterargument worth remembering: Rust could encode the fail-closed state machine in the type system at compile time. We declined that at the cost of ecosystem maturity and solo-maintainer velocity, because the stated goal is demonstrating AI-augmented engineering through product quality and long-term usefulness to self-hosters, which is served by shipping velocity and maintainability, not by the language label.
- **Python (stay put).** Rejected. Keeps us hand-rolling the registry protocol, and its MCP SDK pulls pydantic, starlette, uvicorn, and httpx, which contradicts the small-dependency posture.

## Consequences

The Go test suite is written **fresh** against documented behavior, using the existing Python suite as a reference for edge cases rather than porting it as the behavioral spec. This was the owner's call against the recommendation to port. The risk it accepts is losing failure-mode knowledge encoded in the Python tests. #9 left it to the migration-plan ticket to define how edge-case coverage gets verified; the answer landed in the [rework spec](../rework/SPEC.md) as the behavior inventory, where every row must be claimed by a Go test or struck with a reason before the Python code is removed.
