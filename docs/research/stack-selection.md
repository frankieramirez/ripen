# Research: best-in-class stack for a from-scratch rewrite

- **Ticket:** [#7](https://github.com/frankieramirez/nas-stack-updater/issues/7)
- **Date:** 2026-08-18
- **Method:** every claim below was verified against a primary source (official release pages, the actual repos' manifests, or language reference docs) on 2026-08-18. Citations sit next to the claims they support.

## Summary and recommendation

**Recommendation: Go (1.26.x).** It is the only stack that wins or ties on every criterion that this tool actually stresses: a mature OCI registry client ecosystem (so the hand-rolled registry protocol either disappears or gets a reference implementation to test against), a single static multi-arch binary as the natural output, a supply-chain story built into the toolchain (module checksum transparency log + `govulncheck`), an official stable MCP SDK co-maintained with Google whose entire direct dependency tree is eight modules, and near-total convention alignment with the tools this project will be compared to (Watchtower and Diun are Go; the whole container ecosystem is Go).

**Runner-up: Rust** — the stronger raw portfolio signal and the best refactoring safety, but a thinner OCI library ecosystem, a heavier async dependency tree for the same features, and more toolchain friction for a solo maintainer.

**Modernized Python** (3.14 + uv/ruff) is a genuinely good 2026 toolchain, but it loses on the three criteria that are this project's identity: distribution to NAS hardware, near-zero-dependency supply chain (the official MCP SDK alone destroys that property), and OCI client ecosystem (still hand-rolled).

**Dark horse: TypeScript** (the most battle-tested MCP SDK, and Renovate/WUD precedent) — examined and rejected below, primarily on supply-chain grounds.

Scores (1–5, higher is better):

| # | Criterion | Python 3.14 + uv | **Go 1.26** | Rust 1.97 | TS (dark horse) |
|---|---|---|---|---|---|
| 1 | OCI registry client ecosystem | 2 | **5** | 3 | 2 |
| 2 | Distribution ergonomics (static binary, multi-arch, cross-compile) | 2 | **5** | 4 | 3 |
| 3 | Supply-chain auditability / dependency footprint | 3 | **5** | 4 | 1 |
| 4 | MCP SDK maturity | 5 | **5** | 4 | 5 |
| 5 | Solo-maintainer ergonomics | 4 | **5** | 4 | 3 |
| 6 | Ecosystem convention (comparable tools) | 2 | **5** | 3 | 3 |
| 7 | "Impressively modern" portfolio signal (2026) | 3 | **4** | 5 | 3 |
| | **Total** | **21** | **34** | **27** | **20** |

Baseline versions verified 2026-08-18: Python 3.14.7 stable, released 2026-08-05, with 3.15 in pre-release for October 2026 ([python.org/downloads](https://www.python.org/downloads/)); Go 1.26.0 released 2026-02-10, latest point release go1.26.6 on 2026-08-13 ([go.dev/doc/devel/release](https://go.dev/doc/devel/release)); Rust 1.97.1 released 2026-07-16 ([rust-lang/rust releases](https://github.com/rust-lang/rust/releases/latest)); uv 0.12.5 released 2026-08-14 ([astral-sh/uv releases](https://github.com/astral-sh/uv/releases)); ruff 0.16.3 released 2026-08-13 ([astral-sh/ruff releases](https://github.com/astral-sh/ruff/releases)).

---

## 1. OCI / Docker Registry HTTP API client ecosystem

The current code hand-rolls the Distribution/OCI registry protocol over Python's stdlib HTTP. The question is which ecosystem lets a rewrite stand on a maintained, spec-tracking client instead.

**Go — this is where the registry protocol lives.** Three independent, actively maintained, production-grade client libraries:

- [google/go-containerregistry](https://github.com/google/go-containerregistry) — "Go library and CLIs for working with container registries", ~4,000 stars, latest release v0.21.9 on 2026-08-05. This is the library behind `crane`/`gcrane` and much of the container tooling ecosystem; it covers exactly this project's needs (manifest HEAD/GET, platform selection from manifest lists, digest resolution, token auth) without touching a Docker daemon.
- [regclient/regclient](https://github.com/regclient/regclient) — "Docker and OCI Registry Client in Go", active (pushed 2026-08-09), the library behind `regctl`.
- [oras-project/oras-go](https://github.com/oras-project/oras-go) — the ORAS Go library, active (pushed 2026-08-17), a CNCF project.

The OCI image-spec and distribution-spec Go types themselves (`github.com/opencontainers/image-spec`, `github.com/opencontainers/go-digest`) are the canonical structs the specs are defined against. If the rewrite chooses to keep a hand-rolled client for auditability, these libraries still serve as reference implementations to differential-test against. Score: 5.

**Rust — one real option, small but alive.** [oras-project/rust-oci-client](https://github.com/oras-project/rust-oci-client) ("A Rust crate to interact with OCI registries", v0.17.0 released 2026-05-19, 183 stars, pushed 2026-08-10) grew out of `krustlet`'s `oci-distribution` and is under the ORAS umbrella, but it is pre-1.0 and an order of magnitude less exercised than the Go options. Hand-rolling on top of `reqwest`/`ureq` is viable but is exactly the status quo being escaped. Score: 3.

**Python — effectively unchanged from today.** The only maintained OCI registry SDK is [oras-project/oras-py](https://github.com/oras-project/oras-py) (67 stars), oriented at ORAS artifact push/pull rather than platform-aware manifest-list digest resolution. `docker-py` talks to the daemon socket, which this project explicitly refuses to mount. A Python rewrite keeps hand-rolling the registry protocol. Score: 2.

**TypeScript** — Renovate hand-rolls its Docker registry datasource in-repo ([renovatebot/renovate `lib/modules/datasource/docker`](https://github.com/renovatebot/renovate/tree/main/lib/modules/datasource/docker)); there is no dominant standalone registry client library. Score: 2.

## 2. Distribution ergonomics

Target: amd64 + arm64 Linux (NAS hardware), ideally both a tiny container image and a single binary a self-hoster can drop onto any box.

**Go — the reference standard.** `CGO_ENABLED=0 go build` produces a fully static binary; cross-compilation is a matter of setting `GOOS`/`GOARCH` env vars with no extra toolchain ([go.dev source install docs, environment section](https://go.dev/doc/install/source#environment)). `FROM scratch` container images with one binary in them are idiomatic. The release tooling is best-in-class and maintained: [GoReleaser](https://github.com/goreleaser/goreleaser) v2.17.1 (2026-07-26) automates multi-arch archives, container images, checksums, SBOMs, and signing from one YAML file; [ko](https://github.com/ko-build/ko) (active, pushed 2026-08-18) builds multi-arch images without a Dockerfile. Score: 5.

**Rust — static binaries work, cross-compilation costs setup.** `x86_64-unknown-linux-musl` and `aarch64-unknown-linux-musl` are Tier 2 targets ([rustc platform support](https://doc.rust-lang.org/rustc/platform-support.html)), and fully static binaries via musl + rustls are standard practice. But cross-compiling from one host to both architectures needs either [cross-rs/cross](https://github.com/cross-rs/cross) (active, pushed 2026-08-14, container-based cross toolchains) or `cargo-zigbuild`; release automation via [cargo-dist](https://github.com/axodotdev/cargo-dist) (active, pushed 2026-08-18). All workable, one notch more machinery than Go. Score: 4.

**Python — container image or nothing.** There is no credible static-single-binary story: PyInstaller/Nuitka bundles are neither static nor cross-compilable, so the realistic distribution is exactly today's: a container image per architecture, carrying a full CPython runtime. uv makes *developer* installs excellent (`uv tool install`, PEP 723 scripts) but does not change what ships to a NAS. Score: 2.

**TypeScript** — Node needs a runtime; Bun/Deno can compile single binaries but they embed the whole runtime (~50–90 MB) and the practice is far less proven for long-running daemons. Score: 3.

## 3. Supply-chain auditability and dependency footprint

The current selling point — one runtime dependency (PyYAML), everything else stdlib — is worth preserving. Measured against what each stack needs *including the planned MCP server*:

**Go — smallest realistic tree plus toolchain-native verification.** The stdlib covers HTTP client/server, TLS (including fingerprint pinning via `crypto/tls` VerifyPeerCertificate), JSON, and crypto. A faithful rewrite needs roughly: a YAML parser, the OCI spec types, and the MCP SDK — whose *entire* direct dependency list is 8 modules, mostly `golang.org/x/*` and Google-maintained ([go-sdk go.mod](https://github.com/modelcontextprotocol/go-sdk/blob/main/go.mod)). Every module download is verified against the public checksum transparency log by default ([Go module checksum database](https://go.dev/ref/mod#checksum-database)), and [govulncheck](https://go.dev/doc/security/vuln/) does call-graph-aware vulnerability scanning as an official tool. `go.sum` + `vendor/` makes the whole tree reviewable in-repo. Score: 5.

**Rust — excellent audit tooling, bigger tree.** [cargo-audit / RustSec](https://github.com/rustsec/rustsec) and [cargo-vet](https://mozilla.github.io/cargo-vet/) are best-in-class third-party audit tools. But the official MCP crate `rmcp` requires tokio, serde, reqwest, oauth2, et al. ([rmcp Cargo.toml](https://github.com/modelcontextprotocol/rust-sdk/blob/main/crates/rmcp/Cargo.toml)), so the practical lockfile lands at a few hundred crates from many independent maintainers — auditable, but a much wider trust surface than Go's. Score: 4.

**Python — the zero-dep claim dies with MCP.** The current stdlib-only approach is genuinely strong, and uv's lockfile with hash pinning is solid. But the official MCP SDK depends on anyio, httpx, pydantic, starlette, uvicorn, jsonschema, pyjwt, opentelemetry-api and more ([python-sdk pyproject.toml](https://github.com/modelcontextprotocol/python-sdk/blob/main/pyproject.toml)) — adding MCP converts the near-zero-dependency tool into a typical several-dozen-package Python service. Score: 3.

**TypeScript — disqualifying.** WUD alone declares 41 runtime npm dependencies ([getwud/wud app/package.json](https://github.com/getwud/wud/blob/master/app/package.json)); npm transitive trees in the thousands are normal, and npm has been the ecosystem with the most prominent registry-compromise incidents. For a tool whose pitch is "safe thing that touches your deployments", this is the wrong story to tell. Score: 1.

## 4. MCP SDK maturity (as of 2026-08-18)

All four candidates now have *official* SDKs under the [modelcontextprotocol](https://github.com/modelcontextprotocol) org — this criterion no longer eliminates anyone:

| SDK | Latest release | Status |
|---|---|---|
| [python-sdk](https://github.com/modelcontextprotocol/python-sdk) | v2.0.0, 2026-07-28 | Official; 24k stars; very active (pushed 2026-08-18) |
| [typescript-sdk](https://github.com/modelcontextprotocol/typescript-sdk) | 2.0.0 packages, 2026-07-27 | Official; the reference implementation |
| [go-sdk](https://github.com/modelcontextprotocol/go-sdk) | v1.7.0, 2026-07-28 | Official, "maintained in collaboration with Google"; stable v1 line |
| [rust-sdk](https://github.com/modelcontextprotocol/rust-sdk) (`rmcp`) | rmcp v3.1.3, 2026-08-17 | Official; very active |

Python and TypeScript are the most battle-tested (5); Go is stable-v1, Google-co-maintained, and dependency-light (5); Rust's rmcp is official and releasing fast but has churned through major versions (v3 already), which costs some points for a long-lived daemon (4).

## 5. Solo-maintainer ergonomics

- **Go (5):** one toolchain does format/build/test/vet/cross-compile; builds are near-instant; the [Go 1 compatibility promise](https://go.dev/doc/go1compat) means upgrades are boring; explicit error handling suits fail-closed logic; `go test` + table tests map directly onto the existing regression-test culture. Weakest spot: the type system is less expressive than Rust's for encoding state-machine invariants.
- **Rust (4):** the best refactoring safety of the three — exhaustive matching and ownership make "fail closed" checkable at compile time, which is a real fit for this codebase's philosophy. Costs: slower compiles, steeper API churn in the async ecosystem, and more decisions (runtime, error crates) for one person to own.
- **Python (4):** uv 0.12 + ruff 0.16 is a genuinely excellent 2026 loop, and the existing ~4,400 lines are already written and tested. But strict typing is still bolt-on: Astral's `ty` type checker is at v0.0.72, explicitly pre-stable ([ty releases](https://github.com/astral-sh/ty/releases)), and runtime type errors remain possible in ways Go/Rust exclude.
- **TypeScript (3):** good tooling, but the runtime/bundler/tsconfig decision surface and npm churn are the classic solo-maintainer tax.

## 6. What comparable tools use (verified from their repos)

| Tool | Language | Evidence | Status |
|---|---|---|---|
| Watchtower ([containrrr/watchtower](https://github.com/containrrr/watchtower)) | Go | repo metadata | **Archived**; last commit 2025-12-17; 24.7k stars |
| Watchtower fork ([nicholas-fedor/watchtower](https://github.com/nicholas-fedor/watchtower)) | Go | repo metadata | Active (pushed 2026-08-18), 4.3k stars; keeps hand-rolled `pkg/registry` |
| Diun ([crazy-max/diun](https://github.com/crazy-max/diun)) | Go 1.26 | [go.mod](https://github.com/crazy-max/diun/blob/master/go.mod): uses `go.podman.io/image/v5` (containers/image) as its registry client | Active (pushed 2026-08-11), 4.9k stars |
| WUD ([getwud/wud](https://github.com/getwud/wud)) | TypeScript/Node | [app/package.json](https://github.com/getwud/wud/blob/master/app/package.json): 41 runtime deps | Active, 3.7k stars |
| Renovate ([renovatebot/renovate](https://github.com/renovatebot/renovate)) | TypeScript | hand-rolled [docker datasource](https://github.com/renovatebot/renovate/tree/main/lib/modules/datasource/docker) | Active, 22.3k stars |

Convention verdict: the container-native, daemon-shaped tools in this exact niche are Go; the TypeScript entries are either a web-app-shaped tool (WUD) or a platform product with a paid company behind it (Renovate). Convention matters here for two concrete reasons: (a) drive-by contributors from the self-host community already read Go, and (b) the libraries in criterion 1 exist *because* this domain is Go. Notably, the archiving of original Watchtower also leaves a visible vacuum in exactly this space — a well-engineered Go entrant reads as a successor, not an outlier.

## 7. "Impressively modern" portfolio signal in 2026

- **Rust (5):** still the strongest raw signal; a memory-safe, statically-verified infrastructure daemon in Rust reads as top-tier engineering.
- **Go (4):** reads as "professional infrastructure engineer who chose the boring-correct tool." Combined with GoReleaser artifacts, SBOM + provenance attestation, `FROM scratch` multi-arch images, govulncheck CI, and an MCP server, it is a thoroughly modern showing — the signal comes from the polish, not the language name.
- **Python (3):** uv/ruff/3.14 is modern, but "another Python updater script" is the default assumption a reviewer starts from; the stack fights the first impression.
- **TypeScript (3):** unremarkable in this niche.

---

## Recommendation

**Rewrite in Go 1.26+.** Concretely: `google/go-containerregistry` (or a deliberately minimal hand-rolled client differential-tested against it) for registry digest resolution; stdlib `net/http` + `crypto/tls` for the Portainer/GitHub clients and TLS fingerprint pinning; `modelcontextprotocol/go-sdk` for the MCP server; GoReleaser for multi-arch static binaries plus `FROM scratch` images; `go.sum` + govulncheck + SBOM/signing in CI as the visible supply-chain story. This preserves the project's identity (tiny, auditable, fail-closed) while fixing its two weakest properties today — hand-rolled registry protocol and Python-runtime-only distribution — and lands squarely in the ecosystem where its peers, contributors, and libraries already live.

**Strongest counterargument: Rust.** If the portfolio audience outweighs the self-hoster audience, Rust scores higher where it is hardest to fake — the type system can encode this tool's fail-closed state machine so that illegal transitions don't compile, and "safety-critical updater, written in the safety language" is a cleaner story than Go can tell. The official `rmcp` SDK and `rust-oci-client` make it feasible today. The honest costs: a pre-1.0 OCI client with ~180 stars where Go has three mature options, a several-hundred-crate lockfile where Go ships eight direct MCP deps, slower iteration for a solo maintainer, and no ecosystem-convention dividend. If maximum-signal is truly the top priority, choose Rust and accept those costs; otherwise Go is the better whole-project decision.

A secondary counterargument worth recording: the existing Python codebase is ~2,600 lines with ~1,800 lines of regression tests already encoding hard-won failure-mode knowledge (timeout ambiguity, digest pinning edge cases). A rewrite must port those tests, not just the features — that cost is real in any language and is the strongest argument for the "modernize Python in place" option, which otherwise loses on distribution, dependencies, and OCI ecosystem.
