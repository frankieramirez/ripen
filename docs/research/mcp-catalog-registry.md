# Research: does the Docker MCP Catalog require Docker Hub?

- **Ticket:** [#50](https://github.com/frankieramirez/ripen/issues/50), part of the [release plumbing map](https://github.com/frankieramirez/ripen/issues/46)
- **Date:** 2026-08-19
- **Method:** every claim below was checked against [docker/mcp-registry](https://github.com/docker/mcp-registry) at commit `fd36a38a452e54a166a6cd3413ba2ff726361d24` (2026-07-29, "Update Temporal MCP server entry (#4508)") — a shallow clone read directly — plus `docs.docker.com` source pages and the GitHub API for PR history. Blog summaries were not used. File paths and line numbers below are against that commit.

## Answer

**No. Docker Hub is not load-bearing for the Docker MCP Catalog.** The `image:` field in `server.yaml` is a free-form string. Nothing in the repo's tooling validates its registry, namespace, or tag: `cmd/validate/main.go` runs ten checks and **not one of them reads the top-level `image:` field at all**. Ten catalog entries today point at `ghcr.io`, one at `mcr.microsoft.com`. A GHCR-only Ripen can be listed.

So the Catalog does **not** add weight to keeping Docker Hub. Whatever case there is for `docker.io/frankieramirez/ripen` — self-hoster discovery, per the map's standing premise — has to stand on its own; the Catalog listing does not need it.

What the choice does change is *who builds the image*, and that is a real trade rather than a formality:

| | Option A — Docker builds it | Option B — you supply the image |
| --- | --- | --- |
| `image:` value | `mcp/ripen` (Docker Hub, Docker's namespace) | anything publicly pullable |
| Who builds | Docker, from your repo's Dockerfile at the pinned commit | you, in your own pipeline |
| Signing / SBOM / provenance | added by Docker | not added; yours are not surfaced |
| Nightly automated pin-bump PRs | yes | **no** |
| Pull/star counts on the catalog tile | yes | always 0 |

Option A does not put Ripen's image on Docker Hub under *our* account either — it puts a **third** image, `mcp/ripen`, in Docker's namespace, built by Docker's pipeline from our Dockerfile. It is not the `docker.io/frankieramirez/ripen` GoReleaser publishes. Choosing Option A therefore does not retire the Docker Hub question; it adds an image.

**The real gate on a launch-day listing is not the registry.** No new server has merged into `docker/mcp-registry` since **2026-04-30**, and 885 PRs are open, 722 of them titled "add". Detail in §5.

---

## 1. Submission format

A submitter adds one directory, `servers/<name>/`. `cmd/validate/main.go:111-128` stats `servers/<name>/server.yaml` and requires `server.Name` to equal the directory name.

| File | Required | Evidence |
| --- | --- | --- |
| `server.yaml` | always | present in all 328 server directories |
| `tools.json` | only if the server cannot list its tools without configuration, or is remote | `CONTRIBUTING.md:162-198`; present in 82 of 328 |
| `readme.md` | remote servers only | `CONTRIBUTING.md:260`; present in 77 of 328 |

**No Dockerfile goes into the registry repo.** `CONTRIBUTING.md:12` requires "a Dockerfile in the source repository" — yours, not theirs. Nothing under `servers/` is ever read as a Dockerfile; Docker's build is a `docker buildx build` against a git URL (`cmd/build/main.go:130-138`, `pkg/servers/server.go:29-49`). If you supply your own image, no Dockerfile is exercised by any tool in the repo.

**The submitter does supply an image reference**, at the top level of the schema (`pkg/servers/types.go:35-50`):

```go
type Server struct {
	Name        string          `yaml:"name" json:"name"`
	Image       string          `yaml:"image,omitempty" json:"image,omitempty"`
	Type        string          `yaml:"type" json:"type"`
	...
	Source      Source          `yaml:"source,omitempty" json:"source,omitempty"`
	Run         Run             `yaml:"run,omitempty" json:"run,omitempty"`
	Config      Config          `yaml:"config,omitempty" json:"config,omitempty"`
```

`type:` takes three values in practice: `server` (249 entries — a local container over stdio), `remote` (76 — HTTP/SSE), and `poci` (3 — per-tool images, no top-level `image:`; enforced at `pkg/catalog/tile.go:134-136`).

## 2. Docker-built or submitter-hosted: the distinguishing field

There is no `docker_built` flag. **The distinction is a string prefix on `image:`** (`cmd/build/main.go:55`):

```go
isMcpImage := strings.HasPrefix(server.Image, "mcp/")

if isMcpImage {
	if err := buildMcpImage(ctx, server); err != nil {
		return err
	}
} else {
	if !pullCommunity {
		return fmt.Errorf("server is not docker built (ie, in the 'mcp/' namespace), you must either build it yourself or pull it with `docker pull %s` if you want to use it", server.Image)
	}
	if err := pullCommunityImage(ctx, server); err != nil {
		return err
	}
}
```

`mcp/` means Docker builds it; anything else is "community" and is pulled as-is. The same prefix test appears three more times, each granting a privilege that only `mcp/` images get:

- `cmd/ci/update_pins.go:91` — `if !strings.HasPrefix(server.Image, "mcp/") { continue }`. **The nightly automated commit-pin-bump PRs only ever fire for `mcp/` images.** A self-hosted entry is never automatically refreshed.
- `pkg/catalog/tile.go:140-147` — Docker Hub pull and star counts are fetched only for `mcp/` images (`pkg/hub/hub.go:32`). A self-hosted entry shows zero of both.
- `cmd/ci/helpers.go:97-113` — security-review scoping.

The docs say the same thing in prose. `CONTRIBUTING.md:138-144`:

> If you want to provide a specific Docker image built by your organisation instead of having Docker build the image, you can specify it with the `--image` flag: […] 🔒 If you don't provide a Docker image, we will build the image for you and host it in [Docker Hub's `mcp` namespace](https://hub.docker.com/u/mcp), the benefits are: image will include cryptographic signatures, provenance tracking, SBOMs, and automatic security updates. Otherwise, self-built images still benefit from container isolation but won't include the enhanced security features of Docker-built images.

`README.md:25-33` names the two paths outright — "🏗️ Option A: Docker-Built Image (Recommended)" and "📦 Option B: Self-Provided Pre-Built Image".

The escape hatch is a CLI flag: `cmd/create/main.go:49` defines `--image` as "Image to use for the mcp server, instead of building from the repository", applied at `:116-119` (`tag := "mcp/" + name; if userProvidedImage != "" { tag = userProvidedImage }`) and gating the build at `:130`.

## 3. Which registries are accepted

**Any OCI registry, provided the image is publicly pullable.** No allowlist, no denylist, no host validation. `cmd/validate/main.go`'s ten checks (`run()`, lines 37-85) cover name, directory, title, YAML/prettier formatting, commit pin, secrets, config env, license, icon, remote, OAuth-dynamic, and poci — none inspects `image:`. The only occurrences of `Image` in that file are at `:515-517`, for per-tool `poci` images. The sole runtime contact with a community image is `docker pull` (`cmd/build/main.go:155-161`).

Counts at `fd36a38a`, verified by grepping `servers/*/server.yaml`:

| | Count |
| --- | ---: |
| Server directories | 328 |
| `type: server` (local container) | 249 |
| Images on `mcp/` (Docker-built) | 224 |
| **Images not on `mcp/`** | **25** |
| — `ghcr.io` | 10 |
| — `mcr.microsoft.com` | 1 |
| — Docker Hub, non-`mcp` namespace | 14 |

The ten GHCR entries: `ghcr.io/github/github-mcp-server` (`servers/github-official/server.yaml:2`), `ghcr.io/apollographql/apollo-mcp-server` (`servers/apollo-mcp-server`, PR [#60](https://github.com/docker/mcp-registry/pull/60), merged `43c2bac877eec4273a57b0827afda2b710f52c99`), `ghcr.io/pab1it0/prometheus-mcp-server` (PR [#163](https://github.com/docker/mcp-registry/pull/163), merged `ee18fcdaca6c761e1319f7b906273ac147f79908`), `ghcr.io/stackgenhq/stackgen` (PR [#227](https://github.com/docker/mcp-registry/pull/227)), `ghcr.io/supadata-ai/mcp` (PR [#82](https://github.com/docker/mcp-registry/pull/82)), the three `ghcr.io/victoriametrics-community/mcp-*` entries, `ghcr.io/jpicklyk/task-orchestrator`, and `ghcr.io/saidsef/mcp-github-pr-issue-analyser:latest`.

The cleanest proof that the host is unconstrained is `servers/azure/server.yaml:2` → `mcr.microsoft.com/azure-sdk/azure-mcp`, added by PR [#146](https://github.com/docker/mcp-registry/pull/146) ("Fix image name for Azure MCP server", merged 2025-08-19, `edcda237c27aec886fe8ba51dd8f41eb942772fa`) with no review comments. Neither Docker Hub nor GHCR, merged without argument.

The fourteen non-`mcp` Docker Hub entries include `hashicorp/terraform-mcp-server` and `hummingbot/hummingbot-mcp`, but also several individual developer accounts (`arvindand/maven-tools-mcp:latest`, `yashtekwani/gmail-mcp`, `codygreen719/opine-mcp-server`, `souhardyak/mcp-db-server`). No "verified publisher" status is implied or required anywhere.

No `quay.io` entry exists today, but nothing forbids one — the tooling would treat it identically.

**What still applies to a self-hosted image:** a `type: server` entry must carry a GitHub `source.project` and a 40-character lowercase `source.commit` (`cmd/validate/main.go:185-208`):

```go
if server.Source.Commit == "" {
	return fmt.Errorf("local server must specify source.commit to pin the audited revision")
}
```

All 25 self-hosted entries satisfy it. "Bring your own image" still means "and point at a public GitHub repo pinned to a commit". The repo is honest that this proves nothing about the image — `cmd/ci/helpers.go:108-109`: "We can't guarantee provenance between source and image, but reviewing the source is better than nothing." Note the second-order cost of Option B here: `cmd/ci/update_pins.go:91` skips non-`mcp/` images, so **that pin goes stale and nobody bumps it for you**.

The nearest thing to a registry policy statement in any primary source is the docs FAQ (`docker/docs`, `content/manuals/ai/mcp-catalog-and-toolkit/faqs.md:17-33`): "In addition to Docker-built servers, the catalog includes select servers from **trusted registries such as GitHub and HashiCorp**." That is descriptive, not a rule, and the fourteen individual-developer Docker Hub images contradict reading it as an allowlist.

## 4. What is required of the image

**OCI labels: none required from the submitter.** The only label anywhere in the codebase is one Docker's own build adds: `--label org.opencontainers.image.revision=<sha>` (`cmd/build/main.go:131`, `cmd/create/main.go:140,143`). A pulled community image is never inspected for labels. Ripen's GoReleaser-set labels are therefore neither required nor read.

**Base image: unconstrained. `FROM scratch` is acceptable.** There is no `FROM`-line policy, no distroless or Alpine requirement, and no ban. Nothing reads the Dockerfile of a self-provided image at all. The only functional requirement is that the container speak MCP over stdio when launched as (`internal/mcp/client.go:69`):

```go
args := []string{"run", "--rm", "-i", "--init", "--cap-drop=ALL"}
```

A scratch image with a static binary entrypoint reading stdin satisfies that exactly. `Dockerfile.goreleaser` in this repo already produces one.

**Size: no limit anywhere.** The only size check in the repo is on the icon (under 2 MB, at most 512×512, `cmd/validate/main.go:320-351`), and even those are warnings.

**Architecture: `linux/amd64` + `linux/arm64` in practice, enforced by reviewers rather than code.** CI runs on `ubuntu-latest` (`.github/workflows/ci.yaml:7`) with no `--platform` flag anywhere in the repo, so CI only ever exercises amd64. The requirement surfaces in review — PR [#82](https://github.com/docker/mcp-registry/pull/82), reviewer `cmrigney`: "it looks like you'll need to also push an arm version of the build." Ripen already publishes both.

**Public pullability is enforced by review, not code.** Same PR: "If I try to build this locally, I'm getting: `unauthorized`. Is that a private image?"

**Non-root: not required and not checked.** No `USER`/`runAsUser` policy exists; `run.user` is an optional submitter-chosen field (`pkg/servers/types.go:124`). The hardening is `--cap-drop=ALL`, applied regardless. Ripen's `USER 65532:65532` is fine and unremarkable.

**Stdio, tools-only servers are the primary in-scope case.** `type: server` entries are driven over stdio (`internal/mcp/stdio.go`, `internal/mcp/client.go:62-69`). Remote/SSE/HTTP is the separate `type: remote` track, with `remote.transport_type` validated against `{stdio, sse, streamable-http}` (`cmd/validate/main.go:376-386`). Tools are mandatory: a server whose `Capabilities.Tools` is nil is rejected with "tools not supported" (`internal/mcp/helper.go:220-222`). There is no prompts- or resources-only path. Ripen's four read tools clear this; its deliberate absence of prompts and resources costs nothing.

Every tool argument needs a description — `cmd/validate/main.go:438-447`: `tool "%s" has argument "%s" which is missing a description ("desc" is required)`.

**License is a hard, code-enforced gate.** `internal/licenses/check.go:31-36` rejects any GitHub license key prefixed `gpl`, `agpl`, or `npl`, applied at validate (`cmd/validate/main.go:290`) and again at catalog generation, where it panics (`pkg/catalog/tile.go:53-55`). Ripen is MIT; no issue.

**Ripen's runtime shape is expressible.** `run.command` and `run.volumes` both exist (`pkg/servers/types.go:117-124`, `docs/configuration.md:80-160`), and are widely used — `servers/aks/server.yaml`, `servers/arm-mcp/server.yaml`, `servers/ast-grep/server.yaml`. `run.command` is appended after the image in `docker run`, so it overrides the image's `CMD` while leaving `ENTRYPOINT ["/ripen"]` intact. `run: {command: [mcp], volumes: ["{{ripen.policy_path}}:/config/policy.yaml:ro"]}` with `run.env` carrying `RIPEN_CONFIG`, or the config path passed as an argument, both work. Secrets and env must be declared under `config:` with a JSON Schema (`docs/configuration.md`, `CONTRIBUTING.md:108-136`); secret names must match `^[A-Za-z0-9_-]+\.[A-Za-z0-9._-]+$` (`cmd/validate/main.go:213, 236-242`).

## 5. Review and verification gates

Process, from `CONTRIBUTING.md:36-45`: fork, add `servers/<name>/server.yaml`, make CI pass, and then "Every pull request requires a review from the Docker team before merging. [Share test credentials using this form](https://forms.gle/6Lw3nsvu2d6nFg8e6)."

The gates in order:

1. **CI** (`.github/workflows/ci.yaml`) builds the `validate`/`build`/`catalog`/`clean` binaries **from `main`, not from the PR** (lines 9-28), then runs `scripts/ci-validation.sh` against the PR workspace: `validate --name X` → `build --tools --pull-community X` → `catalog X` (`scripts/ci-validation.sh:63-81`). `--pull-community` is what lets a non-`mcp/` image pass CI at all.
2. **An automated LLM security review** (`.github/workflows/security-review-trigger.yaml`) dispatched to a private reviewer repo, told to "Hunt aggressively for intentionally malicious behavior" and emitting a `security-risk:{critical|high|medium|low|info}` label plus `security-blocked` where release must halt (`agents/security-reviewer/prompt-template.md:15-16, 37-43`). It is gated on `github.event.pull_request.head.repo.full_name == github.repository` (line 17), so **it does not run on fork PRs** — which is how every external submission arrives.
3. **Human review** by `@docker/ai-tools-team` (`.github/CODEOWNERS`) against the checklist in `.github/ISSUE_TEMPLATE/mcp-submission.md:31-42`: license verified, MCP compliance, builds a Docker image, community health, documentation, no significant duplication.

**Signing, provenance, "Docker Official", and "verified publisher" impose nothing on the submitter.** Those are things Docker adds to `mcp/` images it builds, not things you must supply. Ripen's own cosign signatures, SBOMs, and GitHub attestations are neither required nor surfaced by the Catalog.

**Repo ownership is not proven.** Nothing verifies that the submitter owns the source repo or the image. `servers/stackgen/server.yaml` points `source.project` at a Homebrew tap while its image is `ghcr.io/stackgenhq/stackgen`; the two are not cross-checked.

### Turnaround: the claim, and the observed reality

Both primary sources make the same claim — `CONTRIBUTING.md:199-206` and `docker/docs` `catalog.md:133-134`: "When your pull request is reviewed and approved, your MCP server is available within 24 hours." **That 24 hours is post-merge propagation.** It says nothing about time to merge.

Observed time-to-merge for community submissions: 2 days (#227 stackgen), 10 days (#60 apollo), 13 days (#146 azure), 18 days (#163 prometheus), 28 days (#82 supadata), 90 days (#1014 okta), 143 days (#661 alfresco).

### 🚩 The finding that should actually shape Phase 7

**The new-submission pipeline has been stalled for roughly four months.** Verified against the GitHub API on 2026-08-19:

- The most recently merged PR of any kind is [#4508](https://github.com/docker/mcp-registry/pull/4508), 2026-07-29. Before it, a batch of automated pin bumps on 2026-07-08.
- The most recently merged PR that *adds a server* is [#1014](https://github.com/docker/mcp-registry/pull/1014) ("feat: add okta mcp server"), merged **2026-04-30** — 111 days ago. Before that: zscaler (2026-04-16), alfresco (2026-04-03), proxmox (2026-04-03), incident.io (2026-04-01). May, June, July, and August 2026: no new servers.
- **885 PRs are open. 722 have "add" in the title.**

The probable cause is documented by a third party in open issue [#3896](https://github.com/docker/mcp-registry/issues/3896) (2026-06-05, no maintainer response): the auto-merge gate requires a `security-review/*` check that stopped being posted around 2026-05-22. "`Trigger Security Review` completes successfully, but no `security-review/*` check appears. The auto-merge workflow runs to completion, but the merge step is skipped — which lines up with the gate in `wait-for-checks.sh`: `REQUIRED_CHECK_PREFIXES=("security-review/")`." That squares with the fork-PR gate in §5.2.

Separately, open issue [#4662](https://github.com/docker/mcp-registry/issues/4662) (2026-08-09) reports the published `mcp/docker-mcp-catalog:latest` artifact containing only 98 of 328 entries, and only 22 of 249 container-based ones. A commenter reports recovery the next day, so it may be transient — but "merged into the repo" and "visible in the catalog" are separately fallible.

**Consequence for Phase 7: a Catalog listing cannot be planned as a launch-day deliverable.** File the PR at launch and treat the listing as arriving whenever it arrives.

## 6. What a submitter runs locally

`Taskfile.yml` (needs Go 1.24+, Docker Desktop, [Task](https://taskfile.dev), and `npx`):

| Task | Command |
| --- | --- |
| `task wizard` / `task remote-wizard` | interactive generators |
| `task create -- --category X [--image myorg/img] <github-url>` | scaffolds `server.yaml` |
| `task validate -- --name <server>` | the ten checks |
| `task build -- --tools <server>` | build or pull, then list tools |
| `task catalog -- <server>` | writes `catalogs/<name>/catalog.yaml` |
| `task import` / `task reset` | `docker mcp catalog import` / `reset` |

The PR template (`.github/PULL_REQUEST_TEMPLATE.md:24-30`) requires exactly two boxes: `task validate -- --name SERVER_NAME` and `task build -- --tools SERVER_NAME`.

Two traps worth knowing before we file:

- `task build -- --tools X` **fails on a self-hosted image** unless `--pull-community` is added (`cmd/build/main.go:61-63`). The PR template's own checklist item is wrong for Option B submitters. This is the tail of open issue [#39](https://github.com/docker/mcp-registry/issues/39).
- `task validate` shells out to `npx --yes prettier --check servers/<name>/server.yaml` (`cmd/validate/main.go:170-183`) and hard-fails on any formatting difference. Also, `about.title` must not contain "MCP" or "Server", and every word must be capitalized except "and"/"for" (`cmd/validate/main.go:132-167`); `name` must match `^[a-z0-9-]+$` (`:97`). So the entry is titled "Ripen", and the description carries the rest.

## 7. Consequences for this repo

1. **The Catalog is not a reason to keep Docker Hub.** Remove it from the discovery argument in [#46](https://github.com/frankieramirez/ripen/issues/46); the self-hoster-discovery premise has to carry the decision alone.
2. **`docs/agents.md`'s container-packaging section is already correct.** It shows the read-only form on `ghcr.io/frankieramirez/ripen` and says that is what the Catalog entry ships. Nothing there needs changing.
3. **`Dockerfile.goreleaser` cannot serve Option A.** It does `COPY ripen /ripen` from a context GoReleaser prepares with an already-compiled binary. Docker's Option A build is `docker buildx build` against a git URL (`cmd/build/main.go:130-138`), where no such binary exists, so the build would fail. Option A would require a second, self-contained Dockerfile that compiles from source — a new artifact to maintain and keep honest against the real release image. **Option B costs nothing extra and reuses the image we already publish and sign.** Recommend Option B, `image: ghcr.io/frankieramirez/ripen:vX.Y.Z`.
4. **Option B means the `source.commit` pin never auto-updates** (`cmd/ci/update_pins.go:91`). Refreshing the Catalog entry at each release is a manual follow-up PR, and given §5 it may sit for months. Pin the entry's `image:` to a released tag rather than `latest`, so a stale `source.commit` and the image stay consistent with each other.
5. **Nothing in the release pipeline has to change for a listing.** No required labels, no base-image constraint, `FROM scratch` accepted, non-root fine, both architectures already built, MIT license clear, stdio tools-only in scope. The listing is a `server.yaml` in someone else's repo, not a change to how Ripen is released.
