# Research: agent operability patterns for infra tools

Ticket: [#8](https://github.com/frankieramirez/nas-stack-updater/issues/8)
Date: 2026-08-18. All claims cite the primary source that owns them; facts were
verified against live repos/docs on this date.

## Summary

The niche this project is aiming for is genuinely unoccupied: as of today, no
self-host container updater ships a first-party agent surface. Watchtower is
archived (read-only since 2025-12-17,
<https://github.com/containrrr/watchtower>), Diun is notification-only
(<https://github.com/crazy-max/diun>), and WUD has a REST API but no official
MCP server — only a zero-adoption third-party wrapper
(<https://github.com/reimlima/wud-mcp>). Agent-native update surfaces exist
today only one layer up (GitOps: mcp-for-argocd, Flux Operator MCP), one layer
down (Homebrew's official `brew mcp-server`), and at the forge (GitHub assigning
Dependabot alerts to Copilot/Claude/Codex agents). Meanwhile every serious
first-party infra MCP server has converged on the same gating grammar:
registration-time tool filtering, a read-only default or kill switch, a
"write vs destructive" distinction, and scoped revocable credentials — never
trusting spec annotations alone.

### Recommended surface shape (one paragraph)

Two layers over one core. **Layer 1: the CLI is the machine contract** — it is
already JSON-on-stdout with 0/1/2 exit-code discipline (`src/nas_stack_updater/cli.py`),
so formalize it: a versioned JSON envelope, a documented exit-code table
including a Terraform-style "action pending" code, a `status` view that exposes
candidates and the attempts audit trail, and an `AGENTS.md`. **Layer 2: a small
MCP server (stdio first) wrapping the same core**, read-only by default, with
exactly two gated writes — creating a Git digest-pin proposal (which lands
behind the existing PR-as-gate) and clearing a stale proposal record with a
recorded reason and `actor` attribution. **Never exposed to an agent, under any
flag:** clearing the circuit breaker, triggering apply mode, and editing
policy/secrets/TLS. The breaker exists precisely because automated verification
already failed; clearing it is the human judgment call this tool's whole design
is built around, and the industry pattern (Home Assistant's default-deny on the
`update` domain, Terraform's writes-off default) supports drawing that line as
absence-from-the-surface, not a confirmation prompt.

---

## 1. MCP servers shipped by infra/devops tools

### Grafana — `grafana/mcp-grafana`

- ~25+ tool categories (dashboards, datasources, Prometheus/Loki, alerting,
  incidents, OnCall, admin, …). <https://github.com/grafana/mcp-grafana>
- Auth: Grafana **service account tokens** (`GRAFANA_SERVICE_ACCOUNT_TOKEN`, or
  `..._TOKEN_FILE` re-read on every request to support rotation).
- Gating: `--disable-write` global read-only mode; allowlist via
  `--enabled-tools`; ~30 per-category `--disable-<category>` flags.

### GitHub — `github/github-mcp-server`

- ~24 toolsets selected via `--toolsets`/`GITHUB_TOOLSETS`; default subset is
  `context, repos, issues, pull_requests, users`.
  <https://github.com/github/github-mcp-server>
- `--read-only` / `GITHUB_READ_ONLY=1`: "Read-only mode takes priority: write
  tools are skipped … even if explicitly requested via `--tools`."
- `--lockdown-mode`: filters content authored by users without push access — a
  prompt-injection/data-provenance mitigation, not a capability gate.
- Auth: PAT or browser OAuth (token "in memory only"); hosted remote server at
  `https://api.githubcopilot.com/mcp/` with per-toolset URLs and a `/readonly`
  URL suffix variant.
  <https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md>

### Docker — MCP Gateway / Catalog / Toolkit

- Docker's model treats MCP servers themselves as untrusted workloads: each
  catalog server runs in an isolated container with restricted network and
  resources. <https://github.com/docker/mcp-gateway>,
  <https://docs.docker.com/ai/mcp-gateway/>
- Gateway defaults: `--verify-signatures` (default true), `--block-secrets`
  (default true), `--log-calls` (default true), per-server `--cpus`/`--memory`
  limits, pluggable `--interceptor`s.
  <https://github.com/docker/mcp-gateway/blob/main/docs/mcp-gateway.md>
- Docker ships no official engine/compose-control MCP server; its first-party
  server is the Docker Hub MCP server (read-only public content by default,
  Hub PAT required for management). <https://github.com/docker/hub-mcp>
- Relevance here: the **MCP Catalog is a distribution channel** this project
  could eventually list in (<https://github.com/docker/mcp-registry>), and the
  gateway's defaults (signatures, secret-blocking, call logging) are the bar a
  security-adjacent server will be judged against.

### Kubernetes ecosystem

- `containers/kubernetes-mcp-server`: pods/resources/helm toolsets; two
  distinct safety flags — `--read-only` and the softer `--disable-destructive`
  ("disable all destructive operations (delete, update, etc.)").
  <https://github.com/containers/kubernetes-mcp-server>
- `Azure/mcp-kubernetes`: tiered `--access-level readonly|readwrite|admin`,
  **default readonly**; filtering happens at tool-registration time (blocked
  tools never appear). <https://github.com/Azure/mcp-kubernetes>
- Argo CD — `argoproj-labs/mcp-for-argocd`: application CRUD + `sync_application`
  + `run_resource_action`; `MCP_READ_ONLY=true` disables all mutating tools;
  API token read only from env/headers, never from tool arguments.
  <https://github.com/argoproj-labs/mcp-for-argocd>

### Home Assistant — built-in MCP Server integration

- Exposes the intent-based Assist API at `/api/mcp` (Streamable HTTP), plus a
  read-only context-snapshot resource; sampling and notifications unsupported.
  <https://www.home-assistant.io/integrations/mcp_server/>
- Auth: OAuth (IndieAuth pattern — client ID is the LLM app's base URL) or a
  long-lived access token.
- Scope: reuses the voice-assistant **exposed entities** allowlist — "Clients
  can only control or provide information about entities that are exposed."
- Deliberate exclusions (see §6 for the update-domain finding): "No
  administrative tasks can be performed" via the LLM API.
  <https://developers.home-assistant.io/docs/core/llm/>

### HashiCorp — `hashicorp/terraform-mcp-server`

- Registry/docs reads plus HCP TF/TFE operations; it does **not** run local
  plan/apply. Write tools are **off by default** (`ENABLE_TF_OPERATIONS=true`
  required). Org allowlist (`MCP_ORGANIZATION_ALLOWLIST`); per-request bearer
  tokens for RBAC; tokens in query params rejected.
  <https://github.com/hashicorp/terraform-mcp-server>

### What the MCP spec itself provides

- Tool annotations (schema revision 2025-06-18): `readOnlyHint` (default
  false), `destructiveHint` (default true), `idempotentHint`, `openWorldHint` —
  with the explicit caveat that "Clients should never make tool use decisions
  based on ToolAnnotations received from untrusted servers."
  <https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/schema/2025-06-18/schema.ts>,
  <https://modelcontextprotocol.io/specification/2025-06-18/server/tools>
- Elicitation (introduced 2025-06-18) gives servers a sanctioned mid-call
  structured-confirmation channel (`accept`/`decline`/`cancel`); "Servers MUST
  NOT use elicitation to request sensitive information."
  <https://modelcontextprotocol.io/specification/2025-06-18/client/elicitation>
- Current spec revision is **2026-07-28** (stateless protocol, `server/discover`,
  server-initiated requests replaced by the Multi Round-Trip Requests pattern).
  <https://modelcontextprotocol.io/specification/versioning>,
  <https://modelcontextprotocol.io/specification/latest/changelog>

**Common gating patterns (synthesis):** (1) a global read-only switch is
universal, with kubectl-wrapping and Terraform servers defaulting to
writes-off; (2) registration-time toolset filtering is preferred over call-time
refusal; (3) several servers distinguish "write" from "destructive"
(mirroring `readOnlyHint` vs `destructiveHint`); (4) annotations are treated as
untrusted hints — every serious server *also* enforces gating server-side;
(5) auth is always a scoped, revocable credential of the backing platform, and
never passed through tool arguments.

## 2. Structured-CLI conventions for agent consumption

- **gh**: `--json <fields>` (bare `--json` lists available fields), built-in
  `--jq` ("The jq utility does not need to be installed"), `--template`.
  <https://cli.github.com/manual/gh_help_formatting>. Documented exit codes in
  a dedicated help topic: 0 success, 1 failure, 2 cancelled, 4 auth required.
  <https://cli.github.com/manual/gh_help_exit-codes>. Non-interactive controls:
  `GH_PROMPT_DISABLED`, `GH_TOKEN`, `NO_COLOR`/`CLICOLOR`.
  <https://cli.github.com/manual/gh_help_environment>
- **kubectl**: `-o json|yaml|jsonpath|custom-columns`,
  `--dry-run=client|server|none`; `kubectl diff` documents exit codes as state
  (0 no differences, 1 differences, >1 error).
  <https://kubernetes.io/docs/reference/kubectl/>,
  <https://kubernetes.io/docs/reference/kubectl/generated/kubectl_diff/>
- **Terraform**: `-json` on plan/apply emits JSON Lines with a **versioned
  schema** (`version.ui = "1.0"`, semver compatibility promise, "ignore unknown
  properties"); `plan -detailed-exitcode` returns 0 = no changes, 1 = error,
  2 = changes present; `-json` implies `-input=false`.
  <https://developer.hashicorp.com/terraform/internals/machine-readable-ui>,
  <https://developer.hashicorp.com/terraform/cli/commands/plan>
- **systemd**: `systemctl is-active`/`is-failed`/`status` use exit codes as
  queryable state (0 active, 3 not active, 4 no such unit).
  <https://manpages.debian.org/testing/systemd/systemctl.1.en.html>
- **clig.dev**: primary output to stdout, logs/errors to stderr; `--json` on
  request; machine-readable output "where it does not impact usability";
  honor `NO_COLOR` (<https://no-color.org/>), `TERM=dumb`, TTY detection.
  <https://clig.dev/>
- **Agent-specific CLI guidance (2026)**: Arcjet's "Designing a CLI for AI
  agents" — flags as immutable contracts, JSON-by-default for non-TTY, distinct
  exit codes including "confirmation required", replace interactive prompts
  with an exit-code + JSON envelope then rerun with `--confirm`.
  <https://blog.arcjet.com/designing-a-cli-for-ai-agents/>. Terry Li's "4
  Principles for Agent-Facing CLI Design" — one JSON envelope
  (`ok, result, error, fix, next_actions, version`), errors carrying runnable
  remediation, self-describing schema output.
  <https://terryli.ai/posts/4-principles-for-agent-facing-cli-design/>

**Where this repo already stands:** `cli.py` prints JSON to stdout for every
command, sends diagnostics to stderr, never prompts, and uses exit codes
0 (success) / 1 (operational error or failed result) / 2 (configuration error).
Gaps: no schema version in the envelope, exit codes undocumented, no "action
pending" exit code, and `status` omits the `candidates` and `attempts` tables
that exist in the SQLite store (`adapters.py`) — the exact data an agent needs
for candidate review and audit reading.

## 3. AGENTS.md conventions

- The spec ("a README for agents") came out of OpenAI Codex, Amp, Google
  Jules, Cursor, and Factory; now stewarded by the Agentic AI Foundation under
  the Linux Foundation; adoption claim 60k+ open-source projects; read by
  Codex, Jules, Cursor, Gemini CLI, Copilot, Zed, Devin, and ~20 others.
  <https://agents.md/>, <https://github.com/openai/agents.md>
- Content guidance: concrete setup commands, test commands, code-style rules,
  PR requirements; monorepo nesting with nearest-file-wins precedence.
  <https://agents.md/>
- **Claude Code does not natively read AGENTS.md**: its docs say "Claude Code
  reads `CLAUDE.md`, not `AGENTS.md`" and recommend a CLAUDE.md containing
  `@AGENTS.md` or a symlink. <https://code.claude.com/docs/en/memory>
- Practical convention: keep AGENTS.md the single source (verifiable commands,
  under ~200 lines) and make CLAUDE.md a one-line importer.

## 4. MCP SDK maturity per language (feeds the stack decision)

modelcontextprotocol.io now publishes an SDK tiering system (Tier 1 = 100%
conformance + day-of-spec features; Tier 2 = 80% conformance; Tier 3 =
experimental). <https://modelcontextprotocol.io/docs/sdk>,
<https://modelcontextprotocol.io/community/sdk-tiers>

| Language | SDK | Latest (date) | Spec | Tier | Stability posture |
|---|---|---|---|---|---|
| Python | `modelcontextprotocol/python-sdk` | mcp 2.0.0 (2026-07-28) | 2026-07-28 + all earlier | 1 | v2 is a fresh major rework; v1.x security-fixes only |
| TypeScript | `modelcontextprotocol/typescript-sdk` | split packages 2.0.0 (2026-07-27); v1 line 1.30.0 | 2026-07-28 | 1 | v2 stable-but-settling; v1 supported ≥6 months |
| Go | `modelcontextprotocol/go-sdk` | v1.7.0 (2026-07-28) | 2026-07-28 back to 2024-11-05 | 1 | Explicit no-breaking-changes promise since v1.0 (2025-09-30) — kept |
| Rust | `modelcontextprotocol/rust-sdk` (rmcp 3.1.3, 2026-08-17) | 2026-07-28 + earlier | 2 | Active and production-used, but third breaking major in ~18 months |

Sources: <https://github.com/modelcontextprotocol/python-sdk/releases>,
<https://github.com/modelcontextprotocol/typescript-sdk/releases>,
<https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.0.0>,
<https://github.com/modelcontextprotocol/go-sdk/releases>,
<https://crates.io/crates/rmcp>,
<https://github.com/modelcontextprotocol/rust-sdk>.

Notes: the official Python SDK's v1 server API *was* FastMCP 1.0, incorporated
in 2024; the standalone FastMCP project (now 3.x) remains the richer,
more-adopted framework. <https://github.com/jlowin/fastmcp>. In Go,
`mark3labs/mcp-go` (the earlier community SDK) is still alive but is not
listed on the official SDK page; the official `go-sdk` (maintained with
Google) is the canonical choice for new work.
<https://github.com/mark3labs/mcp-go/releases>

**Verdict for the sibling stack decision:** Go is the safest MCP bet (Tier 1,
day-of-spec parity, the only kept API-stability promise). Python is the
fastest to ship with the richest ecosystem, at the cost of adopting a
three-week-old v2 rework (or pinning the maintenance-mode v1 line). Rust is
capable but Tier 2 with a history of breaking majors — choose it for other
reasons, not for MCP. None of the three blocks shipping an MCP server; the
SDK axis is a tiebreaker that mildly favors Go.

## 5. Mapping this tool's human touchpoints onto an agent surface

Current touchpoints (from `cli.py`, `updater.py`, `models.py`, `adapters.py`):

| Touchpoint | Today | Agent-surface fit |
|---|---|---|
| `status` (breaker, lease, accepted digests, pending proposals) | JSON read | **Safe read** — expose as-is |
| Candidate review (candidates table: digest, first/last seen, count) | not surfaced by any command | **Safe read** — the biggest gap; agents can't review what they can't see |
| Audit trail (attempts table, JSONL notifier events) | not surfaced | **Safe read** |
| `run --mode monitor` | one observation cycle | **Safe-ish write** (mutates state DB but recreates nothing; idempotent in effect) — expose, annotated non-destructive |
| Git digest-pin proposal | created automatically in apply-mode cycles for Git-backed stacks | **Gated write** — agent-triggerable is fine because the PR-as-gate keeps a human on merge (the dominant industry pattern, §6) |
| `clear-proposal --stack --reason` | human CLI | **Gated write** with mandatory reason + actor attribution |
| `clear-breaker --reason` | human CLI, reason recorded | **Human-only. Never on the agent surface.** |
| `run --mode apply` / `auto_apply` policy | policy + human | **Human-only** (agents never cause a container recreate) |
| policy.yaml, secrets, TLS config | files on disk | **Human-only**; MCP server should not even read secrets |

**Where other security-sensitive servers draw the "never without a human"
line, applied here:**

- Home Assistant excludes the `update` domain and all admin tasks from LLM
  exposure by default — absence from the surface, not a confirmation dialog.
  (<https://developers.home-assistant.io/docs/core/llm/>; `update` and `lock`
  are absent from `DEFAULT_EXPOSED_DOMAINS` in core 2025.12.0
  `homeassistant/components/homeassistant/exposed_entities.py`.)
- The MCP spec says clients "SHOULD" keep a human in the loop, but annotations
  are untrusted — so real servers enforce server-side. Confirmation prompts
  are a client courtesy, not a security boundary.
- Applied to this tool: **the breaker is a recorded human judgment that
  automated verification failed.** An LLM can synthesize a plausible-sounding
  reason string; a reason field is not a proof of judgment. Therefore
  `clear_breaker` must not exist as an MCP tool at all — not behind a flag,
  not behind elicitation. Same for triggering apply mode: the only path by
  which an agent's action leads to a container recreate should be the Git
  proposal PR that a human merges (and even then the updater independently
  re-verifies parity and health before accepting the baseline).

## 6. Prior art: agent-operable updaters and approval flows

- **Watchtower**: archived 2025-12-17, "no longer maintained"; previously had
  an all-or-nothing token-gated `/v1/update` HTTP trigger — no per-container
  review semantics. <https://github.com/containrrr/watchtower>,
  <https://containrrr.dev/watchtower/http-api-mode/>. Maintained community
  fork: <https://github.com/nicholas-fedor/watchtower>.
- **Diun**: notification-only by design; no control API, no MCP.
  <https://github.com/crazy-max/diun>
- **WUD**: REST/JSON API incl. `POST /api/containers/{id}/triggers/{type}/{name}`
  to manually fire a per-container trigger; a docker trigger with `AUTO=false`
  yields an API-mediated approve-then-apply flow — the only real approval
  granularity in the space, but no first-party MCP (third-party wrapper
  `reimlima/wud-mcp`, 0 stars, 2026-05).
  <https://raw.githubusercontent.com/getwud/wud/main/docs/api/container.md>,
  <https://raw.githubusercontent.com/getwud/wud/main/docs/configuration/triggers/docker/README.md>
- **Renovate**: Dependency Dashboard approval — a checkbox in a Markdown issue
  gates PR *creation*; machine-writable, the closest existing "structured
  approval inbox". No MCP server for Renovate.
  <https://docs.renovatebot.com/key-concepts/dashboard/>,
  <https://docs.renovatebot.com/configuration-options/#dependencydashboardapproval>
- **Dependabot**: chat-ops merge commands (`@dependabot merge` etc.) were
  **retired 2026-01-27** in favor of native PR APIs; rebase/recreate/ignore
  remain. Direction of travel: away from bespoke chat-ops, toward generic APIs
  agents already speak.
  <https://github.blog/changelog/2026-01-27-changes-to-github-dependabot-pull-request-comment-commands/>
- **Dependabot → agents**: since 2026-04-07 Dependabot alerts are assignable
  to AI coding agents (Copilot, Claude, Codex), which open a **draft PR** for
  human review.
  <https://github.blog/changelog/2026-04-07-dependabot-alerts-are-now-assignable-to-ai-agents-for-remediation/>
- **GitOps**: Argo CD Image Updater's git write-back has an explicit
  PR mode "when you want every image update to go through a review workflow"
  (<https://argocd-image-updater.readthedocs.io/en/stable/basics/update-methods/>);
  Flux pushes image updates to a separate branch for CI-opened PRs
  (<https://fluxcd.io/flux/guides/image-update/>). The Flux Operator's MCP
  server gates agents to "reconcile from Git" plus read-only mode, RBAC
  impersonation, and secret masking — Git stays the source of truth.
  <https://fluxoperator.dev/mcp-server/>,
  <https://github.com/controlplaneio-fluxcd/flux-operator>
- **Homebrew**: ships an official first-party `brew mcp-server` (search /
  install / upgrade), with no server-side approval gating — safety rests on
  client permission prompts. A cautionary counter-example, not a model.
  <https://docs.brew.sh/MCP-Server>, <https://github.com/Homebrew/brew/pull/20041>

**Synthesis:** the dominant, still-growing approval pattern is **PR-as-gate**
(Renovate, Dependabot, Copilot coding agent, Argo CD Image Updater, Flux) —
automation fully prepares the change; a human approves by merging. This
project's Git-proposal transaction is already exactly that shape.

---

## Recommended design sketch

Opinionated; scoping into a release happens in a later human ticket.

### A. CLI hardening (do first — it is the substrate for everything else)

1. **Versioned envelope.** Add `"schema_version": "1"` (and the tool version)
   to every JSON document the CLI emits; commit to Terraform-style forward
   compatibility ("clients ignore unknown fields", additive-only within a
   major). Tradeoff: a tiny bit of noise per document buys the freedom to
   evolve output without breaking agent parsers.
2. **Documented exit codes as state.** Keep 0/1/2 (success / operational
   error / config error) and add `3` on `status` = "human attention required"
   (breaker open, or a mature candidate awaiting review) — the
   `terraform plan -detailed-exitcode` / `systemctl is-active` pattern.
   Document the table in `--help` and README. Tradeoff: exit-code semantics
   are an immutable contract once published; keep the table short.
3. **Expose the missing reads.** `status --detail` (or `status --full`)
   including the `candidates` table (digest, first_seen, last_seen, count,
   maturity remaining) and recent `attempts` — this is the candidate-review
   and audit surface. Today those tables exist in SQLite but no command
   surfaces them.
4. **Keep the non-interactive discipline** the CLI already has (no prompts, no
   color, data on stdout, diagnostics on stderr). `NO_COLOR` is trivially
   satisfied; state it in docs.
5. **`AGENTS.md`** at the repo root: verified setup/test commands (the ones in
   README "Local verification"), the safety invariants an agent must not
   weaken (fail-closed, one-service-per-run, breaker semantics), and the JSON
   contract. Add a `CLAUDE.md` containing `@AGENTS.md` for Claude Code
   (<https://code.claude.com/docs/en/memory>).

### B. MCP server (the standout feature)

Ship as a subcommand (`nas-stack-updater mcp`) over **stdio** first — it
inherits the host user's file permissions, needs no new auth surface, and is
what every client supports. Streamable HTTP later, and only with a scoped
bearer token read from a file (Grafana's re-read-on-request pattern) — never
from tool arguments (mcp-for-argocd's rule).

**Tools (registration-time filtering; writes absent unless
`--enable-writes` is passed — Terraform's writes-off default):**

| Tool | Kind | Annotations | Notes |
|---|---|---|---|
| `get_status` | read | `readOnlyHint: true` | breaker, lease, accepted digests, pending proposals |
| `list_candidates` | read | `readOnlyHint: true` | per-stack candidates with maturity countdown |
| `get_audit_trail` | read | `readOnlyHint: true` | attempts + breaker/lease/proposal events, filterable |
| `explain_stack` | read | `readOnlyHint: true` | one stack's policy, accepted digest, candidate, proposal, last attempt — the "why hasn't X updated?" answer |
| `run_monitor_cycle` | write | `readOnlyHint: false, destructiveHint: false` | one observation pass; recreates nothing |
| `create_proposal` | gated write | `destructiveHint: false` | Git-backed stacks only; produces the digest-pin PR; the human merge is the gate |
| `clear_proposal` | gated write | `destructiveHint: true` | requires `reason`; recorded with `actor: "agent"` and client identity |

**Resources:** `nsu://status`, `nsu://candidates`, `nsu://audit` as read-only
mirrors for clients that prefer resources; a `review-candidate` **prompt**
that packages a candidate's digests, age, upstream links, and health history
for an agent to write a human-readable recommendation.

**Not on the surface, ever (not behind any flag):** `clear_breaker`, apply-mode
triggering, policy/secret/TLS mutation, and anything that touches the Docker
API or Portainer beyond the reads the core already performs. Rationale in §5;
precedent: Home Assistant's default-deny on admin/update domains, and the
spec's own position that annotations and client confirmations are not security
boundaries. Tradeoff acknowledged: an agent that diagnoses a breaker cannot
finish the job — by design, it must hand the human a ready-made
`nas-stack-updater clear-breaker --reason "..."` command line (the
`next_actions` pattern from agent-CLI guidance) rather than run it.

**Attribution:** every state mutation already records reasons; extend records
with an `actor` field (`human-cli` vs `mcp:<client>`), so the audit trail
distinguishes agent actions — the property GitHub's lockdown mode and Docker's
`--log-calls` both chase.

### C. Stack-decision input

MCP SDK maturity mildly favors **Go** (Tier 1, kept stability promise),
**Python** is fully viable (Tier 1, richest ecosystem, but a fresh v2 rework),
**Rust** is the weakest MCP fit (Tier 2, breaking-major history). If the
rework stays Python, build the server on the official `mcp` v2 SDK (or
FastMCP 3.x if server composition/auth helpers earn their dependency); if it
moves to Go, the official `go-sdk` is canonical and `mark3labs/mcp-go` should
be avoided for new work.

### D. Positioning

No competitor has this. The credible claim is not "an agent can update your
containers" — it is the opposite: **an agent can observe, explain, and
prepare; only humans (or the tool's own verified pipeline) apply.** That is
both the differentiator and the safety story, and it matches where the
industry's approval patterns (PR-as-gate, default-deny exposure) already
landed.
