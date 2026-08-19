# Ripen rework spec

The hand-off artifact of the [Public relaunch map](https://github.com/frankieramirez/ripen/issues/6). Every decision below was made on a map ticket; each section gists the decision and links the ticket that holds the full resolution and rationale. This document is the execution entry point: nothing is left to decide before the rework begins.

Vocabulary is `CONTEXT.md` — capitalized terms (Transaction, Baseline, Candidate, Circuit breaker, Proposal, Event, Notifier, Agent surface, Web UI, Actor, Response envelope, Event envelope) mean exactly what the glossary says.

## What is being built

**Ripen** — fail-closed image updates for Portainer and Compose. A digest ripens. You apply.

A Go rewrite of the existing Python updater, keeping its fail-closed Transaction semantics, adding a Compose-runtime backend, a versioned-JSON CLI + MCP Agent surface, a webhook Notifier, and an optional read-only Web UI, then shipping it publicly as `v1.0.0`.

- Audience: self-hosters primary, portfolio secondary.
- Maintenance posture: maintained for own use, contributions welcome, best-effort.
- One name everywhere: binary `ripen`, module `github.com/frankieramirez/ripen`, images `ghcr.io/frankieramirez/ripen` + `docker.io/frankieramirez/ripen`, display form **Ripen**. ([Choose the product name and positioning](https://github.com/frankieramirez/ripen/issues/11), [Decide how nas-stack-updater becomes Ripen](https://github.com/frankieramirez/ripen/issues/19))

## Stack and toolchain

[Decide the implementation stack](https://github.com/frankieramirez/ripen/issues/9), from [stack research](https://github.com/frankieramirez/ripen/issues/7) and [agent-operability research](https://github.com/frankieramirez/ripen/issues/8):

- **Go 1.26+**. `gofmt`/`golangci-lint`, `govulncheck` in CI, GoReleaser for release artifacts.
- Registry client: `google/go-containerregistry` (or a deliberately minimal client differential-tested against it).
- Portainer/GitHub clients and TLS fingerprint pinning on stdlib `net/http` + `crypto/tls`.
- MCP on the official `modelcontextprotocol/go-sdk`.
- Module path is the plain forge path `github.com/frankieramirez/ripen` — no vanity import, fixed before `v1.0.0` ([#19](https://github.com/frankieramirez/ripen/issues/19)).
- ADRs to author when execution starts: `docs/adr/0001-rewrite-in-go.md` (source material in [#9](https://github.com/frankieramirez/ripen/issues/9)) and one for the release shape ([#12](https://github.com/frankieramirez/ripen/issues/12)).

## Launch feature set

[Decide the launch feature set](https://github.com/frankieramirez/ripen/issues/10) (as amended):

- **The current Transaction, unchanged in scope**: Monitor mode default, extra-gated Apply mode; two observations plus the maturity window; one Service per run; HTTP health; rollback then Circuit breaker; exact multi-service policy with sibling health; TLS fingerprint or CA, no insecure mode.
- **Two backends**: Portainer API, and a Compose runtime (Docker Compose + Podman Compose). Privileged `docker.sock` is out of scope permanently.
- **Agent surface**: versioned-JSON CLI + first-party MCP server.
- **One outbound Notifier**: webhook.
- **Optional read-only Web UI**, embedded, off by default.
- Roadmap (published in `ROADMAP.md`, not designed here): extra Notifier adapters, metrics, other forges, non-HTTP health, full operator UI, Kubernetes, NAS vendor UIs, Quadlets, private registries, maintenance windows.

## Compose-runtime backend

[Design the compose-runtime backend](https://github.com/frankieramirez/ripen/issues/21) — full resolution on the ticket. Skeleton:

- **Port**: orchestrator port reshaped around the Transaction — `observe(stack_policy) → StackState` and `deploy(stack_state, compose, repull)`. Drift comparison stays in the shared updater core.
- **Adapters**: one compose adapter parameterized by engine command pair; `docker-compose` / `podman-compose` are two thin constructors. Podman via `podman compose` (built-in wrapper), never the Python `podman-compose` tool. Startup probes verify binary + JSON flags.
- **Identity**: `stack` = policy-declared name everywhere; `project:` defaults to it and Ripen always passes `--project-name` explicitly.
- **Connection**: compose CLI subprocess only; opt-in rootless socket via `DOCKER_HOST`/`CONTAINER_HOST` env override. A socket resolving to the privileged docker socket is a config-load refusal.
- **Apply**: in-place atomic digest-pin of the one `image:` line; variable-interpolated `image:` lines are `INELIGIBLE` for apply; no repull fallback; rollback restores previous file bytes first, always.
- **Verification**: conjunctive — every configured sibling running (and healthy where a healthcheck exists) and the functional health check passes.
- **Drift**: raw compose-file bytes + all env-file bytes + resolved service-name set; recorded by path; declared-but-missing env file ⇒ `INELIGIBLE`; symlinks resolved at config load; writability preflighted at observe time.
- **Git**: per-stack `git_path` ⇒ proposal-mode (never edits the local file); explicit, never inferred.
- **Policy**: per-stack `backend: portainer | docker-compose | podman-compose`, `file:` (single scalar), optional `project:`; top-level `compose.docker.{binary,socket}` / `compose.podman.{binary,socket}`. Mixed backends in one instance: one breaker, one state DB, one Event stream, one audit trail.
- **Wire**: new `ResultCode` `ENGINE_UNAVAILABLE`.

## Notifications

[Design notifications](https://github.com/frankieramirez/ripen/issues/18), amended by [#17](https://github.com/frankieramirez/ripen/issues/17) — full resolution on the ticket. Skeleton:

- **One Event stream, two sinks**: structured JSON to stderr (always on, every Event) and the webhook Notifier (off by default, filtered subset). Core port renamed `EventSink`; *Notifier* is the webhook sink.
- **17-Event catalogue**, dotted noun-first names; new Events close today's gaps: `breaker.opened`, `candidate.matured`, `transaction.succeeded`, `stack.recovered`. Default paging set marked on the ticket.
- **Event envelope**: `schema_version` (independent of the CLI's, both start at 1), `event`, `occurred_at`, `run_id`, `backend`, `stack`/`service` (nullable), `actor`, `data`. Additive changes don't bump.
- **Suppression**: page on state *changes*, keyed `(event, stack, service)` in SQLite; recovery re-arms; changing the webhook destination resets all suppression; cold table pages current state (correct).
- **Delivery**: at-most-once, fail-open, bounded retry (2, short backoff), async off the run path via bounded FIFO queue; `notifier.delivery_failed` goes to the stream only. The state database is the system of record, never the stream.
- **Invariant (test it)**: every paging Event corresponds to a durable state change already written — the Notifier can never say something `ripen status` can't confirm.
- **Config**: `notifier.webhook.{url_file,token_file,events,timeout_seconds}` + optional `heartbeat_interval_seconds`. Absent `notifier:` = silent-but-logging. Unknown event name in `events:` is a config-load error.
- **Health**: `last_success_at` + `consecutive_failures` persisted to a singleton row (cross-process); `dropped_since_start` stays in-memory.
- **Typed payloads** replace the key-marker redaction scrubber; a test walks every Event type asserting no secret-like field names.
- **`ripen notify test`** sends a real `notifier.test` through the real path.

## Agent surface

[Design the agent surface](https://github.com/frankieramirez/ripen/issues/17) — full resolution on the ticket. Skeleton:

- **The spine**: MCP is a strict subset of the CLI — every MCP tool maps to a CLI verb with the same name, guard, parameters, and payload. `clear_breaker` and Apply mode are absent from MCP *by construction*.
- **Verbs**: `status`, `candidates`, `audit`, `explain <stack>` (reads); `run --mode monitor`, `propose <stack>` (new), `clear-proposal` (writes); `run --mode apply`, `clear-breaker`, `daemon`, `notify test`, `mcp`, `schema` (CLI-only).
- **Response envelope**: JSON always (no `--json` flag), `schema_version`/`command`/`occurred_at`/`ok`/`data`, failures in the same envelope with a typed `error` (closed code set, each with `retryable`).
- **Exit codes**: 0 success, 1 operational, 2 config/usage, **3 human attention required** (breaker open or `rollback_failed`; read narrowly). Ungated.
- **Identity**: every read emits `backend`/`stack`/`service` (`service` nullable); `state_key` never appears in JSON. `backend` enum has three values from v1.
- **`status`** is policy-driven (configured-but-never-observed Services appear with `baseline: null`), and carries `versions` + `effective_policy` blocks.
- **`audit`** reads the `attempts` table (never the Event stream); cursor pagination; filters incl. `--run <run_id>`.
- **`run_id`**: UUIDv7 on every run, persisted on attempts, echoed in `status`, in the Event envelope.
- **MCP**: stdio-only, tools-only; `ripen mcp --enable-writes` (process flag) adds only `run_monitor_cycle`, `create_proposal`, `clear_proposal`; registration-time filtering; read-only mode builds no network clients and loads no credentials; results carry the identical Response envelope as `structuredContent`; failures are `isError: true`, never JSON-RPC protocol errors.
- **`propose` preconditions**: matured Candidate, `git_path` set, breaker closed (breaker halts every outbound action).
- **Actor**: closed enum `cli`/`daemon`/`mcp` recorded on every write and Event; determined by the surface, never accepted as a parameter.
- **Schema publication**: `ripen schema` emits JSON Schema for every response type; same files checked into `docs/schema/v1/`; CI asserts they match. Receivers ignore unknown fields and unknown result codes.
- **Hard constraints, each with a test**: nothing writes to stdout in the `ripen mcp` process; `ripen daemon` writes nothing to stdout (the Event stream on stderr is its output).
- **Container packaging**: documented read-only form (`:ro` state mount, no `--enable-writes`) and writes-enabled form; the Docker MCP Catalog entry ships the read-only form only.

## Web UI

[Design the optional read-only UI](https://github.com/frankieramirez/ripen/issues/22):

- Embedded via `go:embed`, served inside `ripen daemon`. Off by default; only `ui.enabled: true` activates it.
- Default `127.0.0.1:7476`; non-loopback bind refuses to start without a bearer token (`ui.token` / `RIPEN_UI_TOKEN`); no insecure escape hatch; `/healthz` alone is unauthenticated and information-free.
- Four views: overview; breaker banner with the clear command as copyable text (never a button); audit trail; effective policy + versions.
- Server-rendered Go `html/template`, vanilla JS, polling. No Node toolchain; `go build` stays the only build.
- The HTTP API reuses the Response envelope internally but is undocumented and unversioned — the Agent surface remains CLI + MCP only.

## State schema v1

New SQLite schema, from [Plan the rework migration](https://github.com/frankieramirez/ripen/issues/16) folding in [#17](https://github.com/frankieramirez/ripen/issues/17)/[#18](https://github.com/frankieramirez/ripen/issues/18):

- Tables carried conceptually from Python: `accepted_digests`, `candidates`, `pending_proposals`, `attempts`, `breaker`, `lease` (WAL, busy timeout, `BEGIN IMMEDIATE` lease singleton).
- `attempts.run_id` (UUIDv7) — new.
- `attempts.actor` (`cli`/`daemon`/`mcp`) — new.
- Notifier-health singleton row (`last_success_at`, `consecutive_failures`) — new.
- Notification-suppression table keyed `(event, stack, service)` — new.
- The misnamed `stack` columns (actually `state_key`) split into `backend`/`stack`/`service` in `attempts`, `candidates`, `accepted_digests`.
- **No migration from the Python schema.** This is schema v1; existing deployments start cold and re-baseline on first Monitor run. No config-migration tooling either — `policy.yaml` is hand-rewritten.

## Migration plan

[Plan the rework migration](https://github.com/frankieramirez/ripen/issues/16) — full runbook on the ticket.

**PR chain** — thirteen sequenced PRs straight into private `main`, each green on CI, no milestones:

1. Scaffold + toolchain (module, lint config, CI skeleton)
2. Domain model + policy config
3. State store (schema v1 above)
4. Registry client (digest observation)
5. Portainer backend
6. Compose backend
7. Transaction engine (verify / rollback / breaker) — includes fixing the carried-across defect: guard proposal creation on an existing pending proposal
8. CLI verbs + Response envelope
9. Daemon + Event stream + Notifier
10. MCP server
11. Web UI
12. GoReleaser + distribution wiring
13. Docs + issue forms + repo scaffolding

**Python code**: stays in-tree until the Go binary survives the live NAS cutover, then removed in one final PR before the visibility flip. That removal PR requires the behavior inventory (below) fully claimed.

**Test parity**: tests are written fresh in Go with the Python suite as reference. The behavior inventory in this spec is the verification gate — each Go module PR claims its rows.

**CI**: `ci.yaml` on push/PR — `go test -race`, `golangci-lint`, `govulncheck`, `goreleaser build --snapshot` cross-compiling the full matrix from ubuntu-latest, and a smoke job running the built binary's `status`/`schema`/`version` against an empty config. `release.yaml` on `v*` tags — GoReleaser with keyless cosign + SBOM + provenance. Unit tests use faked engine/registry ports; real-engine integration tests are roadmap.

**Live NAS cutover**: full rename to `ripen` (recreate the Portainer stack — old stack 150 kept stopped as rollback until the soak passes; copied secrets; hand-rewritten policy; one atomic `nas-infrastructure` PR covering `stacks/ripen/`, `fleet.json`, and three docs). A **48-hour Monitor soak** (Baseline seeded for every Service, at least one Candidate observed, zero error-level Events, healthcheck green) gates Apply mode and the visibility flip; a real Apply Transaction does not. Step-by-step runbook on [the ticket](https://github.com/frankieramirez/ripen/issues/16).

## Docs and community scaffolding

[Decide docs and community scaffolding](https://github.com/frankieramirez/ripen/issues/14) — the largest single writing task in the relaunch; budget it as a work item (PR 13 plus README rewrite):

- README + in-repo `docs/` (eight pages: `configuration`, `portainer`, `compose`, `agents`, `proposals`, `notifications`, `architecture`, `troubleshooting`), no site generator. README structure and hero line on the ticket; quick start is Compose-first.
- Hero line and GitHub repo description: **"Fail-closed image updates for Portainer and Compose. A digest ripens. You apply."**
- `AGENTS.md` at root + `CLAUDE.md` containing `@AGENTS.md` (repo-facing agents); `docs/agents.md` (product-facing agents) — kept separate deliberately.
- Issues only (Discussions off), three YAML issue forms + `blank_issues_enabled: false`, PR template with one safety checkbox, `CONTRIBUTING.md` (issue-first, no CLA/DCO), Contributor Covenant 2.1 with a dedicated contact alias, no FUNDING.yml.
- `ROADMAP.md` holds the canonical non-goals, seeded from the map's Out of scope; README carries a five-bullet version.
- Four badges: CI, latest release, license, OpenSSF Scorecard. ~13 topics listed on the ticket.

## Distribution and release

[Decide distribution and release mechanics](https://github.com/frankieramirez/ripen/issues/12):

- First public tag **`v1.0.0`**; semver is the compatibility promise afterward (CLI/MCP breaks need a major bump). Pre-public work stays untagged or `v0.x`.
- Images `linux/amd64` + `linux/arm64`, `FROM scratch`, published to GHCR and Docker Hub with tags `v1.0.0`, `1.0.0`, `v1.0`, `v1`, `latest`. Release binaries add `darwin/amd64` + `darwin/arm64`. No Windows, no 32-bit.
- Channels: GitHub Releases (GoReleaser on annotated tag), `go install .../cmd/ripen@vX.Y.Z`, Homebrew tap `frankieramirez/homebrew-tap` (GoReleaser-updated), in-repo `flake.nix`. No `curl | sh`, no AUR/scoop, nixpkgs and homebrew-core post-launch.
- Hand-written `CHANGELOG.md` (Keep a Changelog); GoReleaser publishes it as the Release body. No Changesets/Node.
- Checksums, Syft SBOM, cosign keyless (GitHub OIDC) on binaries and images, GitHub `attest-build-provenance`. No long-lived key.
- Docker MCP Catalog listing (read-only container form) at public launch — see the checklist.
- Registry/catalog descriptions always say "Ripen, a fail-closed image updater", never a bare `ripen` (PyPI collision and Docker Hub search noise, [#19](https://github.com/frankieramirez/ripen/issues/19)).

## Behavior inventory

Extracted from the Python test suite (2026-08-18). **This list gates the Python-removal PR**: every row must be claimed by a Go test (check the box and note the Go test) or explicitly struck with a reason (e.g. behavior deliberately changed by a design ticket — note which). Where a v1 design ticket supersedes the Python behavior (e.g. Portainer-only assumptions, the JSON-log redaction scrubber, CLI error output), the Go test asserts the *new* decided behavior and the row is claimed against it.

### Config parsing/validation

- [x] A minimal valid policy defaults to monitor mode with max_updates_per_run of 1, and stack auto_apply defaults to false (test_config.py::test_load_policy_defaults_to_single_update_monitor_mode) — Go: `config.TestLoadDefaultsToSingleUpdateMonitorMode`
- [x] A policy may declare a GitHub source (repository, base_branch, token_file) and a per-stack git_path pointing at the compose file in the repo (test_config.py::test_load_policy_supports_git_native_stack_source) — Go: `config.TestLoadSupportsGitNativeStackSource`
- [x] Setting git_path on a stack without a top-level github section is a config error ("requires github configuration") (test_config.py::test_git_path_requires_github_configuration) — Go: `config.TestGitPathRequiresGitHubConfiguration`
- [x] A stack may declare per-service rules with each service's own health check, including a custom accepted_status list (e.g. [200, 302]) (test_config.py::test_load_policy_supports_explicit_per_service_health_rules) — Go: `config.TestLoadSupportsExplicitPerServiceHealthRules`
- [x] A service in a multi-service stack may be marked enabled: false (health-only), and the per-service enabled flag is preserved in the loaded policy (test_config.py::test_multi_service_policy_supports_health_only_service) — Go: `config.TestMultiServicePolicySupportsHealthOnlyService`
- [x] A stack where every service is disabled is a config error ("at least one enabled service") (test_config.py::test_multi_service_policy_requires_one_managed_service) — Go: `config.TestMultiServicePolicyRequiresOneManagedService`
- [x] Per-service enabled/auto_apply flags must be real YAML booleans; quoted strings like "false"/"true" are a config error ("must be a boolean") — variants: enabled, auto_apply (test_config.py::test_per_service_flags_require_real_booleans) — Go: `config.TestPerServiceFlagsRequireRealBooleans`
- [x] A stack listing more than one expected service must define explicit per-service rules; a single stack-level health/auto_apply is a config error ("requires per-service rules") (test_config.py::test_multi_service_policy_requires_explicit_service_rules) — Go: `config.TestMultiServicePolicyRequiresExplicitServiceRules`
- [x] Duplicate names in expected_services are a config error ("duplicate") (test_config.py::test_expected_services_rejects_duplicate_names) — Go: `config.TestExpectedServicesRejectsDuplicateNames`
- [x] Health accepted_status must be a non-empty list of valid HTTP status codes — variants rejected: empty list, boolean, 99, 600 (test_config.py::test_health_statuses_are_nonempty_http_codes) — Go: `config.TestHealthStatusesAreNonemptyHTTPCodes`
- [x] A stack with per-service rules may not also set stack-level auto_apply or health (ambiguity is a config error) (test_config.py::test_per_service_policy_rejects_ambiguous_stack_level_apply_setting) — Go: `config.TestPerServicePolicyRejectsAmbiguousStackLevelApplySetting`
- [x] Unknown/unrecognized config fields are rejected ("unknown config fields") (test_config.py::test_unknown_field_is_rejected) — Go: `config.TestUnknownFieldIsRejected`
- [x] A stack cannot be both configured/enabled and listed in exclude ("also excluded") (test_config.py::test_enabled_stack_cannot_also_be_excluded) — Go: `config.TestEnabledStackCannotAlsoBeExcluded`
- [x] max_updates_per_run greater than 1 is rejected ("requires max_updates_per_run") (test_config.py::test_more_than_one_update_per_run_is_rejected) — Go: `config.TestMoreThanOneUpdatePerRunIsRejected`
- [x] tls_fingerprint_sha256 must be exactly 64 hexadecimal characters (test_config.py::test_invalid_tls_fingerprint_is_rejected) — Go: `config.TestInvalidTLSFingerprintIsRejected`
- [x] Portainer base_url must use https; http URLs are rejected (test_config.py::test_portainer_base_url_must_use_https) — Go: `config.TestPortainerBaseURLMustUseHTTPS`
- [x] Exactly one TLS trust mechanism (CA file or pinned fingerprint) is required; omitting both is a config error ("exactly one") (test_config.py::test_tls_trust_mechanism_is_required) — Go: `config.TestTLSTrustMechanismIsRequired`
- [x] Non-integer values for numeric settings (e.g. lease_ttl_seconds: nope) are a config error naming the field ("lease_ttl_seconds must be an integer") (test_config.py::test_malformed_numeric_setting_is_a_config_error) — Go: `config.TestMalformedNumericSettingIsAConfigError`

### State store

- [x] Accepted (baseline) digests and candidate observations persist across store reopens; observing the same candidate again increments count while preserving first_seen and updating last_seen (test_state.py::test_state_persists_baseline_and_candidate_observations) — Go: `state.TestStatePersistsBaselineAndCandidateObservations`
- [x] The store creates its parent directory if missing (test_state.py — store() helper uses tmp_path/"state"/updater.db) — Go: `state.TestStateCreatesParentDirectoryIfMissing`
- [x] Each service in a stack has an independent persisted accepted digest, and get_status reports all accepted digests (test_state.py::test_state_persists_independent_service_digests_for_one_stack) — Go: `state.TestStatePersistsIndependentServiceDigestsForOneStack`
- [x] A pending Git proposal (digest + PR URL) persists across store reopens and appears in status as pending_proposals; accepting a digest for that stack clears its pending proposal (test_state.py::test_state_persists_and_clears_pending_git_proposal) — Go: `state.TestStatePersistsAndClearsPendingGitProposal`
- [x] clear_pending_proposal returns whether a record existed: false when nothing pending, true when it removed one, false again afterwards (test_state.py::test_clear_pending_proposal_reports_whether_record_existed) — Go: `state.TestClearPendingProposalReportsWhetherRecordExisted`
- [x] The run lease excludes a concurrent acquire while unexpired; it can be acquired again after TTL expiry; status reports lease_active while any unreleased lease exists; releasing a stale (superseded) token does not deactivate the current lease; releasing the current token does (test_state.py::test_lease_excludes_concurrent_run_and_expires) — Go: `state.TestLeaseExcludesConcurrentRunAndExpires`
- [x] Clearing the circuit breaker requires a non-blank reason (blank is an error and the breaker stays open); with a reason, the breaker closes and status reflects it (test_state.py::test_breaker_requires_explicit_clear_reason) — Go: `state.TestBreakerRequiresExplicitClearReason`

### Digest observation / baselining (monitor)

- [x] Monitor mode records the currently running digest as the accepted Baseline without redeploying when the stack is up to date (test_updater.py::test_monitor_records_proven_baseline_without_redeploying) — Go: `updater.TestMonitorBaselinesTheProvenRunningDigestWithoutRedeploying`, `updater.TestMonitorBaselinesTheRegistryDigestWhenTheBackendReportsUpToDate`
- [x] In a multi-service stack, monitor baselines each service independently without redeploying (test_updater.py::test_monitor_baselines_each_service_in_a_multi_service_stack) — Go: `updater.TestMonitorBaselinesEachServiceOfAMultiServiceStackIndependently`
- [x] A health-only (disabled) service is skipped entirely for registry resolution and baselining; only enabled services produce results (test_updater.py::test_monitor_skips_registry_resolution_for_health_only_service) — Go: `updater.TestAHealthOnlyServiceIsNeverResolvedOrBaselined`
- [x] Monitor refuses to baseline when the registry already shows a newer digest than the running one (result BASELINE_BLOCKED, nothing accepted) (test_updater.py::test_monitor_refuses_to_baseline_when_update_already_pending) — Go: `updater.TestMonitorRefusesToBaselineWhenAnUpdateIsAlreadyPending`
- [x] Monitor reports a new registry digest as CANDIDATE without redeploying (test_updater.py::test_monitor_reports_candidate_without_redeploying) — Go: `updater.TestMonitorReportsANewRegistryDigestAsACandidateWithoutRedeploying`
- [x] Candidate observations require a minimum age: the first apply-mode run over a new digest reports CANDIDATE, and only after candidate_min_age_seconds has elapsed does apply proceed to UPDATED (test_updater.py::test_apply_updates_mature_candidate_after_health_passes) — Go: `updater.TestApplyWaitsForTheCandidateMaturityWindow`

### Transaction / apply

- [x] Apply on a mature candidate with passing health redeploys with the new digest pinned, records it as accepted, and issues exactly one update with repull=true for a single-service stack (test_updater.py::test_apply_updates_mature_candidate_after_health_passes) — Go: `updater.TestApplyRedeploysASingleServiceStackWithOneRepull`
- [x] In a multi-service stack, apply pins only the changed service's image to its digest, leaves sibling services' image lines untouched, uses repull=false, and a follow-up monitor run shows all services UP_TO_DATE (test_updater.py::test_apply_updates_only_one_service_in_a_multi_service_stack) — Go: `updater.TestApplyPinsOnlyTheChangedServiceOfAMultiServiceStack`
- [x] At most one update is applied per run even when multiple services have mature candidates: the first becomes UPDATED, the rest stay CANDIDATE (test_updater.py::test_apply_changes_at_most_one_service_when_two_candidates_are_mature) — Go: `updater.TestAtMostOneServiceIsUpdatedPerRun`
- [x] Rewriting the compose file preserves comments, YAML anchors/aliases, and header content; the pinned image is written as a quoted "tag@digest" reference (test_updater.py::test_multi_service_update_preserves_compose_comments_and_anchors) — Go: `composefile.TestPinningOneImagePreservesCommentsAnchorsAndSiblings`
- [x] Apply cancels (result DRIFTED, no update issued) when the compose file changes between planning and applying (test_updater.py::test_apply_cancels_when_compose_drifts_after_planning) — Go: `updater.TestApplyCancelsWhenTheComposeDriftsAfterPlanning`
- [x] A run fails with a permission error before any inventory work when the Portainer API identity does not match the expected username (test_updater.py::test_wrong_portainer_identity_fails_before_inventory) — Go: `updater.TestAWrongBackendIdentityFailsTheRunBeforeAnyInventoryWork`, `portainer.TestPreflightRefusesAMismatchedPortainerIdentity`
- [x] Environment variable hashing is order-independent (test_updater.py::test_environment_hash_is_independent_of_portainer_order) — Go: `portainer.TestTheFingerprintIsIndependentOfEnvironmentOrder`
- [x] A notifier failure never discards or alters the run result (test_updater.py::test_notification_failure_does_not_discard_run_result) — Go: `updater.TestAFailingEventSinkNeverChangesARunResult`

### Verification (health)

- [x] Health preflight covers every service in the stack: an unhealthy sibling service blocks the update entirely (result INELIGIBLE, no update issued) (test_updater.py::test_apply_refuses_to_mutate_when_another_stack_service_is_unhealthy) — Go: `updater.TestAnUnhealthySiblingBlocksTheUpdateEntirely`
- [x] A health-only (disabled) service's health check still gates sibling updates: if it is unhealthy the update is INELIGIBLE (test_updater.py::test_health_only_service_still_blocks_sibling_update_when_unhealthy) — Go: `updater.TestAHealthOnlyServiceStillGatesItsSiblings`
- [x] Verification checks every stack service's health both before and after the update (test_updater.py::test_apply_verifies_every_stack_service_before_and_after_update) — Go: `updater.TestVerificationChecksEveryServiceBeforeAndAfterTheUpdate`

### Rollback

- [x] Failed post-update health rolls back by redeploying with the previous digest pinned and opens the Circuit breaker (test_updater.py::test_failed_health_rolls_back_to_digest_and_opens_breaker) — Go: `updater.TestFailedPostUpdateHealthRollsBackAndOpensTheBreaker`
- [x] In a multi-service stack, rollback re-pins only the changed service back to the old digest, leaves siblings untouched, and the breaker reason names the failed stack/service (result ROLLED_BACK) (test_updater.py::test_failed_multi_service_health_check_rolls_back_only_changed_service) — Go: `updater.TestFailedPostUpdateHealthRollsBackAndOpensTheBreaker`
- [x] When health also fails after rollback, the result is ROLLBACK_FAILED and the breaker blocks any future apply (next run reports BREAKER_OPEN) (test_updater.py::test_failed_rollback_health_opens_breaker_and_stops_future_apply) — Go: `updater.TestAFailedRollbackIsReportedAndBlocksEveryFutureApply`
- [x] An exception thrown by the health adapter is treated as unhealthy (times out into rollback) rather than crashing the run (test_updater.py::test_health_adapter_exception_times_out_into_rollback) — Go: `updater.TestAHealthCheckThatErrorsCountsAsUnhealthy`

### Circuit breaker

- [x] Failed rollback verification opens the breaker, and an open breaker stops future apply runs with result BREAKER_OPEN (test_updater.py::test_failed_rollback_health_opens_breaker_and_stops_future_apply) — Go: `updater.TestAFailedRollbackIsReportedAndBlocksEveryFutureApply`
- [x] The breaker opens on failed post-update health even when the rollback itself succeeds (test_updater.py::test_failed_health_rolls_back_to_digest_and_opens_breaker) — Go: `updater.TestFailedPostUpdateHealthRollsBackAndOpensTheBreaker`
- [x] An unhealthy deployment observed via Git-flow reconciliation also opens the breaker (test_updater.py::test_unhealthy_git_deployment_opens_breaker_without_accepting_digest) — Go: `updater.TestAPinnedButUnhealthyGitDeploymentOpensTheBreakerWithoutAccepting`

### Timeout ambiguity

- [x] A backend update call that times out is treated as ambiguous and resolved by re-checking: when image status and health prove the deploy succeeded, the update is accepted (result UPDATED, digest accepted, breaker stays closed, no second deploy) (test_updater.py::test_timed_out_update_is_accepted_when_health_and_image_status_prove_success) — Go: `updater.TestATimedOutDeployIsAcceptedWhenImageStatusAndHealthProveSuccess`

### Git-backed stacks / Proposals

- [x] Apply refuses to mutate a Git-backed stack directly when no Git proposal configuration exists (result INELIGIBLE, no update, breaker stays closed) (test_updater.py::test_apply_refuses_to_detach_git_backed_multi_service_stack) — Go: `updater.TestApplyRefusesToDetachAGitBackedStackWithNoProposalConfiguration`
- [x] With GitHub config and a stack git_path, a mature candidate on a Git-backed stack produces a Proposal (PR) instead of a redeploy: result PROPOSED, updates_applied stays 0, the proposed content pins tag@digest at the configured repo path, and the pending proposal (digest + URL) is recorded (test_updater.py::test_git_backed_stack_creates_proposal_without_redeploying) — Go: `updater.TestAGitBackedStackProposesInsteadOfRedeploying`
- [x] A Git-flow deployment is accepted only after the live compose shows the digest pin AND the running digest matches AND health passes: result UPDATED with updates_applied 0, digest accepted, pending proposal cleared, no direct update issued (test_updater.py::test_git_deployment_is_accepted_only_after_digest_pin_and_health_match) — Go: `updater.TestAGitDeploymentIsAcceptedOnlyAfterThePinAndTheRunningDigestMatch`
- [x] If the Git-flow deployment's service is unhealthy, the digest is not accepted (Baseline stays old), the result is ERROR, and the breaker opens (test_updater.py::test_unhealthy_git_deployment_opens_breaker_without_accepting_digest) — Go: `updater.TestAPinnedButUnhealthyGitDeploymentOpensTheBreakerWithoutAccepting`
- [x] An operator can clear a reviewed stale pending proposal by stack name with a reason, and status then shows no pending proposals (test_updater.py::test_operator_can_clear_reviewed_stale_proposal) — Go: `updater.TestAnOperatorCanClearAReviewedStaleProposal`

### Image reference parsing

- [x] Docker Hub references normalize to registry-1.docker.io with a library/ repository prefix; other registries (e.g. ghcr.io) preserve registry and repository as written (test_updater.py::test_image_parser_normalizes_docker_hub_and_preserves_ghcr) — Go: `domain.TestParseImageReferenceNormalizesDockerHubAndPreservesGHCR`
- [x] A tag@digest reference exposes the tagged form (update channel) and the pinned digest separately, and can be re-pinned to a new digest (test_updater.py::test_tagged_digest_reference_preserves_update_channel_and_pin) — Go: `domain.TestTaggedDigestReferencePreservesUpdateChannelAndPin`
- [x] Invalid image references are rejected with a "valid OCI" error — variants: path traversal in repository, invalid tag characters, uppercase repository (test_updater.py::test_invalid_image_reference_is_rejected) — Go: `domain.TestInvalidImageReferencesAreRejected`

### Portainer adapter

- [x] A malformed stack entry from the Portainer API (e.g. non-integer Id) is reported as an adapter error, not a crash (test_adapters.py::test_malformed_portainer_stack_is_reported_as_adapter_error) — Go: `portainer.TestMalformedStackEntryIsAnAdapterError`
- [x] The API key file tolerates a trailing newline (stripped before use as the X-API-Key header) (test_adapters.py::test_api_key_file_tolerates_trailing_newline) — Go: `portainer.TestAPIKeyFileToleratesTrailingNewline`
- [x] An API key containing interior whitespace is rejected at adapter construction (test_adapters.py::test_api_key_file_rejects_interior_whitespace) — Go: `portainer.TestAPIKeyFileRejectsInteriorWhitespace`
- [x] Stack updates use the configured extended deployment timeout rather than the default request timeout (test_adapters.py::test_stack_update_uses_the_extended_deployment_timeout) — Go: `portainer.TestStackUpdateUsesTheExtendedDeploymentTimeout`
- [x] list_stacks marks a stack as git_backed when Portainer reports a GitConfig (test_adapters.py::test_stack_list_records_git_backing) — Go: `portainer.TestStackListRecordsGitBacking`
- [x] update_stack refuses to update a Git-backed stack (adapter error) without making any HTTP call (test_adapters.py::test_stack_update_refuses_git_backed_stack) — Go: `portainer.TestStackUpdateRefusesGitBackedStack`
- [x] Running-service digest discovery queries only running containers filtered by the stack's compose project label (never all containers), and maps service name to repo digest via each container's image RepoDigests (test_adapters.py::test_running_service_digests_are_scoped_to_the_authorized_compose_project) — Go: `portainer.TestRunningServiceDigestsAreScopedToTheAuthorizedComposeProject`
- [x] Digest discovery raises an adapter error ("no running containers") when the compose project has no running containers (test_adapters.py::test_running_service_digests_reject_an_empty_project_result) — Go: `portainer.TestRunningServiceDigestsRejectAnEmptyProjectResult`
- [x] When a container's image reference carries an explicit digest pin and the image has multiple RepoDigests, the container's exact pinned digest wins (test_adapters.py::test_running_service_digest_prefers_the_containers_exact_digest_pin) — Go: `portainer.TestRunningServiceDigestPrefersTheContainersExactDigestPin`

### Registry adapter

- [x] A bearer-auth challenge with a non-https realm is rejected ("realm must use https") (test_adapters.py::test_registry_rejects_insecure_bearer_realm) — Go: `registry.TestRejectsInsecureBearerRealm`
- [x] Multi-arch index resolution selects the manifest digest matching the requested os/architecture rather than the index digest (test_adapters.py::test_registry_resolves_the_linux_amd64_manifest_digest) — Go: `registry.TestResolvesTheLinuxAmd64ManifestDigest`
- [x] ARM manifests are disambiguated by variant (e.g. arm/v7 vs arm/v6) (test_adapters.py::test_registry_uses_variant_to_disambiguate_arm_manifests) — Go: `registry.TestUsesVariantToDisambiguateArmManifests`
- [x] For a single (non-index) manifest, the image config's platform is verified against the requested platform and a mismatch is an adapter error (test_adapters.py::test_registry_verifies_platform_for_single_manifest) — Go: `registry.TestVerifiesPlatformForSingleManifest`

### GitHub proposal adapter

- [x] Proposing a change creates a branch and digest-pin PR: fetches the base-branch file (decoding multi-line base64 content), PUTs the new content with the file's blob sha, and opens a PR (result created=true with the PR URL) (test_adapters.py::test_github_proposal_creates_digest_pin_pr) — Go: `github.TestProposingCreatesABranchAndADigestPinPullRequest`
- [x] Proposing when the branch already holds the desired content and a PR is open is idempotent: no POST/PUT calls, result is the existing PR URL with created=false (test_adapters.py::test_github_proposal_reuses_existing_branch_and_pull_request) — Go: `github.TestProposingIsIdempotentWhenTheBranchAndPullRequestExist`
- [x] A proposal is refused when the repository's base-branch file content differs from the live reviewed compose (source drift) (test_adapters.py::test_github_proposal_refuses_repository_source_drift) — Go: `github.TestProposingRefusesWhenTheRepositorySourceHasDrifted`
- [x] A GitHub token file readable by group or others (e.g. mode 0644) is rejected at adapter construction (test_adapters.py::test_github_token_file_rejects_broad_permissions) — Go: `github.TestATokenFileReadableByOthersIsRefused`

### Event sink / Notifier

- [ ] The stderr Event sink emits structured JSON and no Event payload carries secret material — in Go this becomes the typed-payload test from the notifications design, replacing the Python key-marker scrubber (test_adapters.py::test_json_notifier_redacts_secret_like_fields)

### CLI

- [ ] `run` exits 1 on an operational (adapter) error without a traceback — in Go the failure lands on stdout as an `ok: false` Response envelope plus a human-readable stderr line, per the agent-surface design (test_cli.py::test_run_reports_operational_error_without_traceback)
- [ ] `daemon --once` exits 1 on a transient run error, emits a structured error Event, and never sleeps (test_cli.py::test_daemon_once_reports_transient_error_without_sleep)

## Invariants to test explicitly

Collected from the design tickets; each gets a dedicated test in the Go suite:

1. Every paging Event corresponds to a durable state change already written ([#18](https://github.com/frankieramirez/ripen/issues/18)).
2. Nothing writes to stdout in the `ripen mcp` process ([#17](https://github.com/frankieramirez/ripen/issues/17)).
3. `ripen daemon` writes nothing to stdout ([#17](https://github.com/frankieramirez/ripen/issues/17)).
4. MCP read-only mode registers no write tools and constructs no network clients ([#17](https://github.com/frankieramirez/ripen/issues/17)).
5. `actor` is set by the surface and cannot be supplied by a caller ([#17](https://github.com/frankieramirez/ripen/issues/17)).
6. An open breaker blocks Apply *and* Proposal creation; Monitor observation and reads continue ([#17](https://github.com/frankieramirez/ripen/issues/17)).
7. Proposal creation is guarded on an existing pending proposal — no re-propose while a PR sits unmerged ([#16](https://github.com/frankieramirez/ripen/issues/16)).
8. `ripen schema` output matches `docs/schema/v1/` (CI assertion, [#17](https://github.com/frankieramirez/ripen/issues/17)).
9. No Event payload field name matches the secret-marker list ([#18](https://github.com/frankieramirez/ripen/issues/18)).
10. A configured compose socket resolving to the privileged docker socket refuses at config load ([#21](https://github.com/frankieramirez/ripen/issues/21)).
