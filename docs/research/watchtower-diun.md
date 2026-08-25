# Research: what Watchtower and Diun actually do

- **Ticket:** [#100](https://github.com/frankieramirez/ripen/issues/100), part of the [ripen.dev site map](https://github.com/frankieramirez/ripen/issues/98)
- **Date:** 2026-08-25
- **Method:** every claim below was checked against the projects' own repositories at pinned commits — [`nicholas-fedor/watchtower@f2fe32e`](https://github.com/nicholas-fedor/watchtower/tree/f2fe32e824ffd0507d1e9aabefacde2b330cc32c) (2026-08-24), [`crazy-max/diun@269cb27`](https://github.com/crazy-max/diun/tree/269cb27295944aeacfe549d24ab7ac483e600aa9) (2026-07-11), and [`containrrr/watchtower@ca0e86e`](https://github.com/containrrr/watchtower/tree/ca0e86e824ec05389ab972ea97d04d4bf0476e90) (2025-12-17, the archive commit) — plus the GitHub API for release and activity history. No comparison blog posts were used.

  Diun's published documentation at `crazymax.dev/diun` is reachable and is linked directly. Watchtower's fork documentation at `watchtower.nickfedor.com` returns HTTP 403 to non-browser clients, so links for it go to the Markdown files in the repo that generate that site. They are the same text.

## Answer

**The single most important fact is a maintenance one, and getting it wrong would sink the table: `containrrr/watchtower` was archived on 2025-12-17 and is read-only. Its last release, `v1.7.1`, is from 2023-11-11.** The live Watchtower is [`nicholas-fedor/watchtower`](https://github.com/nicholas-fedor/watchtower), at `v1.21.0` (2026-08-19), which continues the `containrrr` version series and ships releases roughly weekly. Any comparison table has to be against the fork, and has to be against a version of Watchtower that is *more* careful than the one most people remember.

That matters because the fork has closed two of the gaps the table would most obviously have claimed:

- It has a **maturity window**. `--cooldown-delay` refuses to update an image younger than a configured age, and fails closed when the age cannot be determined. Off by default, but it exists, and it is described in its own docs as a supply-chain defense.
- It has a **read-only-ish surface** — an HTTP API with `check`, `containers`, `images`, `history`, `events`, `status`, `config` endpoints — that the archived version did not.

Diun is what it says it is and has been consistent about it since 2019: it watches registries and sends notifications, and it will never touch a container. The maintainer said so directly in [issue #2](https://github.com/crazy-max/diun/issues/2#issuecomment-500403408): "Diun is not intended to update containers but only to send notifications when an image is updated on a registry." It is actively maintained, at `v4.33.0` (2026-05-30), and the most recent release added features rather than fixing rot.

**The table is not a clean sweep, and it must not read as one.** Watchtower is easier to start and has more notification targets than Ripen. Diun watches more platforms than Ripen and reads registries more cheaply than Ripen does. Diun's opt-in-by-default posture matches Ripen's exactly. Detail and proposed wording in §3 and §4.

---

## 1. Watchtower

### 1.1 Which Watchtower

`containrrr/watchtower` is archived. The README's first line, at the archive commit, reads:

> ### ⚠️ This project is no longer maintained
> See https://github.com/containrrr/watchtower/discussions/2135 for details.

[Discussion #2135](https://github.com/containrrr/watchtower/discussions/2135), "Goodbye containrrr/watchtower!", posted 2025-12-17 by `simonaronsson`, gives the reason and a caution that is worth quoting in full, because it is a fact about the ecosystem and not an opinion we should launder:

> Neither @piksel, nor I, are big users of docker anymore, and frankly lost interest (and time) in maintaining the project.
>
> There are a few forks out there - unfortunately I know nothing about them so can't really vouch for their legitimity. If you want to continue using Watchtower, please assess them yourself without switching. A few of the active forks I've looked at are full of AI slop and while they might work, I wouldn't advice using any of them.

The original maintainers named no successor and endorsed none. That is the honest state of the world, and the table should not imply otherwise in either direction.

Numbers as of 2026-08-25, from the GitHub API:

| | `containrrr/watchtower` | `nicholas-fedor/watchtower` | `beatkind/watchtower` |
| --- | --- | --- | --- |
| Archived | **yes**, 2025-12-17 | no | no |
| Stars | 24,666 | 4,323 | 314 |
| Latest release | `v1.7.1`, 2023-11-11 | `v1.21.0`, 2026-08-19 | `v2.3.2`, 2025-06-22 |
| Last commit | 2025-12-17 | 2026-08-24 | 2026-08-20 |
| Open issues | 219 | 10 | — |
| Commits since 2026-02-01 | 0 | 100+ | — |

`nicholas-fedor/watchtower` is the live one by every available measure: stars, release cadence (six releases in the last two months), open-issue backlog, and continuity of the version series. It is a detached fork — the GitHub API reports `fork: false` and no parent — but the lineage is unambiguous. Its `CHANGELOG.md` carries entries like "Merge containrrr/main to resolve 37 commits behind" and "Remove references to containrrr for nicholas-fedor", its `go.mod` retracts `[v1.7.2, v1.7.9]` as prematurely published continuations of the old series, and it keeps the original `com.centurylinklabs.watchtower.*` label namespace and the full upstream contributor list. It also carries a matching fork of Shoutrrr at [`nicholas-fedor/shoutrrr`](https://github.com/nicholas-fedor/shoutrrr).

`beatkind/watchtower` is the only other fork with meaningful traction and its last release is over a year old, despite recent commits.

**For the table: say "Watchtower" and mean the fork; footnote the archive.** Calling Watchtower dead is wrong. Calling `containrrr/watchtower` maintained is also wrong.

### 1.2 Access model

**It requires the Docker socket, and there is no socket-less mode.** The docs are unambiguous ([`docs/getting-started/usage/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/getting-started/usage/index.md)):

> ### Docker Socket Requirement
>
> Since Watchtower needs to interact with the Docker API in order to monitor and update containers, you need to mount `/var/run/docker.sock` to the Watchtower container with the `-v` flag.

Every quick-start in every document mounts it read-write:

```bash
docker run -d \
  --name watchtower \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --restart unless-stopped \
  nickfedor/watchtower
```

The access is genuinely broad and cannot be narrowed by intent. Watchtower stops containers, removes them, creates them, starts them, renames them (during self-update), and — when lifecycle hooks are enabled — runs arbitrary commands inside monitored containers through Docker's exec API ([`docs/advanced-features/lifecycle-hooks/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/advanced-features/lifecycle-hooks/index.md)). That is the full API.

Two mitigations are documented, and both should be acknowledged rather than ignored:

- **Remote daemon over TLS.** `--host` / `DOCKER_HOST` accepts `tcp://`, with `--tlsverify` and `--cert-path` for client certificates ([`docs/configuration/docker-connection/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/configuration/docker-connection/index.md)). This moves the socket, it does not narrow it.
- **Socket proxies.** [`docs/advanced-features/secure-connections/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/advanced-features/secure-connections/index.md) has a "Docker Socket Proxies" section pointing at `Tecnativa/docker-socket-proxy` and `11notes/docker-socket-proxy`, prefaced with "You are **highly** encouraged to perform your own due diligence before using any software that interacts directly with the Docker socket." It names them as an option and does not document a working endpoint allowlist, so the operator is on their own for which API paths to permit. A proxy that permits container create, start, stop, remove and exec is the same authority with an extra hop.

Registry credentials are optional and only for private registries: `REPO_USER` / `REPO_PASS`, or a mounted `config.json`.

### 1.3 What it does on an update

**It pulls and recreates, unattended, across every container it can see, by default.** [`docs/configuration/introduction/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/configuration/introduction/index.md):

> By default, Watchtower monitors all containers running on the Docker daemon it connects to.

Scope is narrowed by naming containers as arguments, by `--label-enable` plus a `com.centurylinklabs.watchtower.enable=true` label, or by `--disable-containers`. The default is opt-out.

The cycle is a 24-hour interval (`--interval` / `WATCHTOWER_POLL_INTERVAL`, default `86400`) or a six-field cron expression via `--schedule` ([`docs/configuration/scheduling/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/configuration/scheduling/index.md)). Every stale container in scope is updated in one cycle. Linked containers are sorted into dependency order; `--rolling-restart` does them one at a time instead of stopping the batch and recreating it.

**There is a maturity window, and this is the fact most likely to be got wrong.** [`--cooldown-delay`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/advanced-features/image-cooldown/index.md) sets a minimum image age before an update is applied. Its own documentation frames it exactly as Ripen frames maturity:

> By requiring that an image has existed in the registry for a certain period of time, the cooldown provides a defense against supply-chain attacks, where a compromised image is published and immediately pulled by automated update tools.

And it fails closed:

> !!! Warning "When Watchtower cannot determine the image creation time, the update is **always deferred**."
>     This is a deliberate security decision. It is safer to miss an update than to pull an unverified image.

Age comes from the `created` field of the registry config blob, fetched over the OCI Distribution API without downloading layers. The differences from Ripen's window are real but they are differences of mechanism, not of presence:

| | Watchtower `--cooldown-delay` | Ripen `candidate_min_age_seconds` |
| --- | --- | --- |
| Default | empty, disabled | `86400`, on |
| Clock it measures | image **build** time, from the config blob's `created` | time since **Ripen first observed** the digest |
| Second signal | none | the digest must also be seen twice |
| Documented weakness | "An image built days ago but only just tagged and pushed will appear old, potentially bypassing the intended cooldown window"; "A compromised image could include a fabricated creation timestamp" | none of those; the clock is Ripen's own |
| When the signal is missing | defer | there is nothing to miss |
| Scope | global, overridable per container by label, `"0"` disables | policy-wide |

Watchtower's own docs name both weaknesses in a "Limitations" section. That is a project being honest, and the table should reflect the feature's existence rather than its imperfections.

The archived `containrrr` version has **no** cooldown. [`containrrr.dev/watchtower/arguments`](https://containrrr.dev/watchtower/arguments/) lists 34 flags and none of them is a delay or maturity window. This is a fork-only feature.

### 1.4 Digest handling

**It follows a tag and never pins to a digest. It refuses to work on containers that are already digest-pinned.**

Staleness is a HEAD request for the manifest of the container's exact image reference, compared against the local image's `RepoDigests` ([`pkg/container/image.go`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/pkg/container/image.go)). The new container is created from the old container's config, so `Config.Image` stays whatever it was: `nginx:1.29`, not `nginx:1.29@sha256:…`.

The clearest proof is that Watchtower opts out of digest-pinned containers entirely. From the doc comment on `Update` in [`internal/actions/update.go`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/internal/actions/update.go):

> Containers with pinned images (referenced by digest) are skipped to preserve immutability.

and from `IsImagePinnedByDigest` in `pkg/container/image.go`, which gates both the update check and the pull. A stack pinned the way Ripen pins is invisible to Watchtower.

Tag semantics are precise and worth crediting: a container on `nginx:1.29` updates only when `nginx:1.29` moves, never when `nginx:latest` or `nginx:1.30` does ([`docs/getting-started/introduction/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/getting-started/introduction/index.md)).

**It records nothing durably.** There is no database in `go.mod` — no bbolt, no SQLite, no file store. `/v1/history` is explicitly "historical scan results from the **in-memory ring buffer** (up to 500 entries)" ([`docs/http-api/overview/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/http-api/overview/index.md)). Restart Watchtower and its history is gone. What was running before an update survives only as the old image on disk, and only if `--cleanup` is off — which is the default, so the image is usually still there, but nothing records which container it belonged to or when it was replaced.

### 1.5 Rollback and health

**No rollback. Health is checked in one mode only, and a failure there is a log line.**

The string "rollback" does not appear anywhere in the repository. Nor does "revert". The only recovery machinery is `TryRecoverOrphanedContainer` in `internal/actions/update.go`, which starts a *Watchtower* container left in the `created` state by a failed self-update. It has nothing to do with the containers being updated.

Health verification exists only under `--rolling-restart`. From [`docs/configuration/update-behavior/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/configuration/update-behavior/index.md):

> When containers have health checks configured, Watchtower waits for each container to become healthy before proceeding to the next one.
>
> !!! Note
>     If a container fails to become healthy within 5 minutes, Watchtower logs a warning but continues with the next container to avoid blocking the entire update process.

The code says the same thing more bluntly. After `client.WaitForContainerHealthy` returns an error:

```go
log.Warn().
    Err(waitErr).
    Fields(fields).
    Msg("Failed to wait for container to become healthy")

// Don't fail the update, just log the warning
```

In the default (non-rolling) mode there is no post-update health check at all. A container that comes up and immediately crash-loops is a successful update as far as Watchtower is concerned.

Lifecycle hooks are the nearest thing to a gate, and they are worth stating accurately because they are a genuine capability. With `--enable-lifecycle-hooks`, a **pre-update** hook that exits `75` (`EX_TEMPFAIL`) skips that container, and any other non-zero exit aborts the update process. But **post-update** and **post-check** hooks are, per the same table, "Failures are logged but ignored; update process continues" ([`docs/advanced-features/lifecycle-hooks/index.md`](https://github.com/nicholas-fedor/watchtower/blob/f2fe32e824ffd0507d1e9aabefacde2b330cc32c/docs/advanced-features/lifecycle-hooks/index.md)). You can veto an update before it happens. You cannot undo one after it happens.

**There is no circuit breaker.** Nothing halts the next cycle because the last one went badly. Nothing requires a human to acknowledge anything. A cycle that fails is a cycle that failed, and the next one runs on schedule.

### 1.6 Notification-only mode

**Watchtower has one, and the table must say so.** `--monitor-only` / `WATCHTOWER_MONITOR_ONLY`, settable globally or per container via `com.centurylinklabs.watchtower.monitor-only`:

> Monitors for new images, sends notifications, and runs lifecycle hooks without updating containers.

Two caveats that are ours to state fairly, both from Watchtower's own docs:

- "Images may still be pulled due to Docker API limitations for digest comparison." Monitor-only is not read-only against the host; it still writes to the local image store. `--no-pull` avoids that but then only watches the local image cache, which defeats the purpose.
- Cooldown is skipped for monitor-only containers, since they are never updated.

Notification delivery goes through Shoutrrr, in its forked form at `nicholas-fedor/shoutrrr` v0.17.1. That library ships **27 service packages**: Bark, Discord, Google Chat, Gotify, IFTTT, Join, Lark, Matrix, Mattermost, MQTT, Notifiarr, ntfy, OpsGenie, PagerDuty, Pushbullet, Pushover, Rocket.Chat, Signal, Slack, SMTP, Teams, Telegram, Twilio, WeCom, Zulip, plus a generic webhook and a logger sink. Notifications are Go-templated and can be split per container.

There is also a substantial HTTP API in the fork, off by default and token-gated: `POST /v1/update`, `POST /v1/check`, and `GET` endpoints for `containers`, `containers/details`, `history`, `images`, `config`, `status`, `metrics`, plus an SSE `events` stream and a Swagger UI. Endpoints are enabled individually. Enabling only the read endpoints gives a read-only HTTP surface — but the process behind it still holds the socket, so this is a narrower API, not a narrower privilege.

---

## 2. Diun

### 2.1 What it is, and what it is not

Diun watches container images in registries and notifies. It has never updated anything and never will. The maintainer's own words, [issue #2, 2019-06-10](https://github.com/crazy-max/diun/issues/2#issuecomment-500403408):

> Like I said in #1, unlike other tools like WatchTower, Diun is not intended to update containers but only to send notifications when an image is updated on a registry.

Seven years later this is still the framing on the [front page of the docs](https://crazymax.dev/diun/): "**D**ocker **I**mage **U**pdate **N**otifier helps you keep track of container image updates without manually watching registries."

**This is verifiable from the code, not just the docs.** The entire Docker-facing surface is three read calls — `ContainerList`, `ContainerInspect`, `ImageInspect` — in [`pkg/docker/container.go`](https://github.com/crazy-max/diun/blob/269cb27295944aeacfe549d24ab7ac483e600aa9/pkg/docker/container.go) and [`pkg/docker/image.go`](https://github.com/crazy-max/diun/blob/269cb27295944aeacfe549d24ab7ac483e600aa9/pkg/docker/image.go). There is no create, no start, no stop, no remove, no exec anywhere in the project.

### 2.2 Access model

**It depends entirely on the provider, and two providers need no container runtime at all.** Diun discovers images from seven sources ([providers docs](https://crazymax.dev/diun/providers/docker/)): Docker, Swarm, Kubernetes, Nomad, containerd, Dockerfile, and File.

- The [**File provider**](https://crazymax.dev/diun/providers/file/) takes a YAML list of image references. No socket, no cluster, no runtime. This is a genuinely socket-less mode and it is a first-class, documented path.
- The **Dockerfile provider** parses `FROM` lines out of Dockerfiles. Same: nothing but files.
- The [**Kubernetes provider**](https://crazymax.dev/diun/providers/kubernetes/) ships an RBAC example scoped to `get`, `watch`, `list` on `pods`. Least privilege, documented, working.
- The [**Docker provider**](https://crazymax.dev/diun/providers/docker/) needs the socket, and **every example in every Diun document mounts it read-write**: `"/var/run/docker.sock:/var/run/docker.sock"`, in `docs/install/docker.md`, `docs/providers/docker.md`, `docs/providers/swarm.md`, `docs/usage/basic-example.md` and `docs/faq.md`. Diun makes only read calls, so a `:ro` mount or a GET-only socket proxy would work, but Diun documents neither. The gap between what it needs and what it asks for is a documentation gap, not a capability gap — and it is worth saying so in exactly those terms, because overstating it would be unfair.

Registry credentials are configured per registry or per image through [`regopts`](https://crazymax.dev/diun/config/regopts/), with name- or image-based selectors.

The one piece of persistent state is a bbolt file, `diun.db` by default, holding image manifests ([`docs/config/db.md`](https://crazymax.dev/diun/config/db/)).

### 2.3 What it does on an "update"

It sends a notification. That is the whole of it.

Discovery is opt-in by default on the Docker provider: `watchByDefault` is `false`, so a container without `diun.enable=true` is ignored. Scheduling is a cron expression with configurable jitter, and `runOnStartup` defaults to `true` ([`docs/config/watch.md`](https://crazymax.dev/diun/config/watch/)).

There is no maturity window and no delay before notifying. There would be nothing for one to gate.

The [**script notifier**](https://crazymax.dev/diun/notif/script/) is the seam people use to bolt updates on themselves. It runs an arbitrary command with the finding in environment variables — `DIUN_ENTRY_IMAGE`, `DIUN_ENTRY_DIGEST`, `DIUN_ENTRY_STATUS`, `DIUN_ENTRY_METADATA_CTN_ID` and friends. Diun hands you the digest and the container ID and gets out of the way. Whatever happens next is not Diun's, and Diun does not verify, sequence, or undo it.

### 2.4 Digest handling

**Diun tracks the digest a registry reference resolves to, and compares it against the last digest Diun itself saw. It does not compare against what is running.**

The check, in [`pkg/registry/manifest.go`](https://github.com/crazy-max/diun/blob/269cb27295944aeacfe549d24ab7ac483e600aa9/pkg/registry/manifest.go): a HEAD request for the manifest digest; if `compareDigest` is on (the default) and it matches the stored digest, stop there. Otherwise fetch the manifest and, for multi-platform images, compare the platform-specific instance digests rather than the index digest. **Layers are never downloaded.** This is cheaper and cleaner than Watchtower's monitor-only, which may still pull.

The status logic, in [`internal/app/job.go`](https://github.com/crazy-max/diun/blob/269cb27295944aeacfe549d24ab7ac483e600aa9/internal/app/job.go), is three states: `new` when Diun has no stored manifest for the reference, `update` when the stored digest differs from the remote one, `unchange` otherwise.

**The consequence is worth understanding before writing any row about it.** The Docker provider reads only `ctn.Image` — the image *reference* the container was started from — and hands that reference off to the registry watcher ([`internal/provider/docker/container.go`](https://github.com/crazy-max/diun/blob/269cb27295944aeacfe549d24ab7ac483e600aa9/internal/provider/docker/container.go)). The container's actual running digest never enters the comparison. So on a fresh install, Diun records whatever the registry currently holds as the baseline and reports `new` — and `firstCheckNotif` defaults to `false`, so it says nothing. **If your container is already three versions behind when Diun starts, Diun will not tell you.** It tells you about the next change after it started watching.

Ripen's Baseline is the opposite: it is the digest proven to be running, and it is recorded only if it can be proven.

Diun does record what it saw before, in bbolt, keyed by image reference. But it is a registry history, not a deployment history. It has no notion of an update having been applied, so it has nothing to record about one.

### 2.5 Rollback and health

Not applicable, and it would be a category error to score it. Diun changes nothing, so there is nothing to verify, nothing to undo, and no failure state to break a circuit on.

Diun does have health signals, both about itself: a [Healthchecks.io integration](https://crazymax.dev/diun/config/watch/#healthchecks) that pings start and success events per run, a `diun healthcheck` command for Docker's `HEALTHCHECK`, and a token-gated Prometheus `/metrics` endpoint. These monitor the watcher, not the workloads.

### 2.6 Maintenance status

**Actively maintained, at an unhurried cadence, by one person.**

| | Value |
| --- | --- |
| Latest release | `v4.33.0`, 2026-05-30 |
| Last commit | 2026-07-11 |
| Stars | 4,875 |
| Open issues | 101 |
| Commits since 2026-02-01 | 100+ |
| Created | 2017-12-30 |

`v4.33.0` is a feature release, not a maintenance one: a new containerd provider, a container `healthcheck` command, multiple Nomad namespaces, proxy support for HTTP notifiers, Teams workflow webhook cards. Releases are irregular — v4.29.0 in December 2024, v4.30.0 in August 2025, three since — but the project is not in a maintenance-only state and describing it as one would be wrong.

The single-maintainer risk is real and should not be raised in a comparison table we wrote, because Ripen carries exactly the same risk and says so in its own `CONTRIBUTING.md`: "This is a project maintained for its author's own use."

Notification targets: 17 notifiers ([Amqp, Apprise, Discord, Elasticsearch, Gotify, Mail, Matrix, MQTT, ntfy, Pushover, Rocket.Chat, Script, SignalRest, Slack, Teams, Telegram, Webhook](https://crazymax.dev/diun/config/notif/)). Fewer than Shoutrrr's 27 by count, but the set is differently shaped: Apprise is a gateway to a hundred more services, and Script, Webhook, AMQP, MQTT and Elasticsearch are integration seams rather than chat destinations.

---

## 3. Row-by-row: what is fair to claim

Ripen, for reference on each row: no privileged socket ever, a rootless socket at most and only for the Compose backend; one Service per Transaction on stacks that opted in; a Candidate must be seen twice and be older than `candidate_min_age_seconds` (24h default); apply pins `tag@sha256:…` into the Compose document; every configured sibling's health is checked before and after; a failure restores the Baseline digest and opens the Circuit breaker, which only a human clears with a reason; SQLite is the system of record and `ripen audit` reads it.

### Genuinely favorable, and defensible

| Row | Why it holds |
| --- | --- |
| **Privileged Docker socket** | Watchtower requires it and cannot function without it. Diun's Docker provider requires it and every documented example mounts it read-write. Ripen refuses it at config load, including through a symlink chain. This is the strongest row on the page and it is true. |
| **Digest pinning** | Ripen writes `tag@sha256:…` into the document. Watchtower follows the tag and *skips* digest-pinned containers outright. Diun watches a reference and pins nothing. |
| **Post-update verification** | Ripen re-checks every configured sibling. Watchtower checks health only under `--rolling-restart` and treats a failure as a warning. Diun has nothing to verify. |
| **Rollback** | Ripen restores the Baseline digest. Neither tool has any rollback, and neither claims to. |
| **Circuit breaker** | Human-reset halt after a failed Transaction. Nothing comparable exists in either tool. |
| **Blast radius per run** | Ripen: one Service. Watchtower: every stale container in scope, in one cycle. Diun: not applicable, which the row should say rather than leaving blank. |
| **Durable audit trail** | Ripen: SQLite, `ripen audit`. Watchtower: a 500-entry in-memory ring buffer, lost on restart. Diun: a manifest store, which is a registry history and not a deployment record. |
| **Records what was actually running** | Ripen's Baseline is a proven running digest. Diun never reads the running digest at all, so it cannot tell you that you are already behind. Watchtower compares against the local image but persists nothing. |
| **Agent surface** | Ripen has a versioned JSON CLI and an MCP server that structurally cannot apply. Watchtower's HTTP API includes `POST /v1/update`. Diun has none. |

### Honestly a wash

| Row | Why |
| --- | --- |
| **Maturity window** | The fork's `--cooldown-delay` is a real maturity window, framed by its own docs as a supply-chain defense and failing closed on missing data. Ripen's is on by default, uses its own observation clock rather than the image's self-reported build time, and requires a second sighting. Better, and defensibly so. Not unique, and claiming it as unique would be false. |
| **Monitor / notification-only mode** | All three have one. Ripen's is the default; Watchtower's is a flag; Diun's is the entire product. |
| **Opt-in by default** | Ripen requires an explicit policy entry. Diun's `watchByDefault` is `false`. Only Watchtower defaults to opt-out. This is a Ripen-and-Diun row, not a Ripen row. |
| **Registry reads without pulling** | Ripen reads manifests. Diun reads manifests and never downloads layers. Watchtower's cooldown check reads manifests only, though its monitor-only mode may still pull. Diun is at least Ripen's equal here. |
| **Maintenance** | All three are actively maintained by a small number of people. Ripen is the newest and least proven of the three; that is a fact about Ripen, not a point against them. |

### Honestly a loss

| Row | Why |
| --- | --- |
| **Time to first useful run** | Watchtower is one `docker run` with one volume and no configuration file. Ripen needs a policy file describing every stack, service, and health target before it will start, and refuses to start if the policy is wrong. That refusal is the design, and it is still a cost the reader pays on day one. |
| **Notification targets** | Watchtower has 27 through Shoutrrr. Diun has 17, one of which is Apprise. Ripen has one webhook. |
| **Platforms watched** | Diun covers Docker, Swarm, Kubernetes, Nomad, containerd, Dockerfiles, and plain YAML lists. Ripen covers Portainer and Compose. |
| **Watching things you do not run** | Diun's File and Dockerfile providers watch any image reference at all, with no runtime involved. Ripen only knows about services in stacks it is configured to manage. |
| **Non-Compose orchestrators** | Ripen cannot update a Kubernetes workload or a Swarm service. Watchtower cannot either, but Diun can at least watch them. |

### Would read as a smear if stated baldly

Five framings to keep off the page:

1. **"Watchtower is unmaintained."** It reads as a fact and it is half a fact. `containrrr/watchtower` is archived; Watchtower is not dead. Stating the first without the second is the exact error that would discredit the table.

2. **"Watchtower updates blindly with no delay."** True of `v1.7.1`. False of `v1.21.0`, which has `--cooldown-delay` and documents it as a supply-chain defense. If we say "no maturity window" about a tool that has one, every knowledgeable reader stops reading.

3. **"Watchtower has no rollback and no health check, so it is dangerous."** The first two clauses are true. The third is our threat model applied to someone else's stated scope. Watchtower's own README says: "Watchtower is intended to be used in homelabs, media centers, local dev environments, and similar. We do **not** recommend using Watchtower in a commercial or production environment." A tool that scopes itself honestly and then meets that scope is not dangerous, it is different. Quote their scope statement and let the reader draw the line.

4. **"Diun does not do X."** Every row where Diun has no cell is a row Diun deliberately does not play. Filling those cells with red marks turns a design choice into a failure. Diun's row should read as "by design, not applicable" wherever that is true, and the table should say once, in prose, that Diun's scope is narrower on purpose and that its author has said so since 2019.

5. **"Diun mounts the Docker socket read-write."** Literally true of every example in its docs, and misleading as a security claim, because Diun makes only three read calls and could not write through that socket if it wanted to. Saying "requires the socket, though it only reads through it" is both accurate and harder to argue with than the bald version.

---

## 4. Proposed wording for the contested rows

Wording to use as-is; the qualifiers are load-bearing.

**Maturity window**

> **Ripen:** Required. A digest must be seen twice and be older than 24 hours (configurable) before apply mode will touch it. The clock is Ripen's own first sighting.
> **Watchtower:** Available, off by default. `--cooldown-delay` defers an update until the image's registry-reported build time is older than a configured age, and defers when that time cannot be read.
> **Diun:** Not applicable — Diun never applies anything.

**Monitor / notify-only**

> **Ripen:** The default mode. Apply requires both `mode: apply` and per-stack `auto_apply`.
> **Watchtower:** `--monitor-only`, globally or per container. Images may still be pulled to compare digests.
> **Diun:** The entire product.

**Docker socket**

> **Ripen:** Never the privileged socket. A rootless user socket is optional and only for the Compose backend; a path resolving to `/var/run/docker.sock` refuses at config load.
> **Watchtower:** Required, read-write. It creates, starts, stops, removes and (with lifecycle hooks) execs into containers. A socket proxy is suggested in the docs; TLS to a remote daemon moves the socket rather than narrowing it.
> **Diun:** Required for the Docker, Swarm and containerd providers, and every documented example mounts it read-write — though Diun only lists and inspects. Its File and Dockerfile providers need no socket at all.

**Maintenance**

> **Ripen:** v1.0.0, August 2026. One maintainer.
> **Watchtower:** Active as [`nicholas-fedor/watchtower`](https://github.com/nicholas-fedor/watchtower), v1.21.0 (August 2026). The original [`containrrr/watchtower`](https://github.com/containrrr/watchtower) was [archived in December 2025](https://github.com/containrrr/watchtower/discussions/2135); its last release was v1.7.1 in 2023.
> **Diun:** Active, v4.33.0 (May 2026). One maintainer.

**Notification targets**

> **Ripen:** One webhook, with per-event filtering.
> **Watchtower:** 27, via Shoutrrr.
> **Diun:** 17, including Apprise, a generic webhook, and an arbitrary script.

**Setup**

> **Ripen:** A policy file naming every stack, service and health target. Ripen refuses to start if it is wrong.
> **Watchtower:** One `docker run` with one volume. No configuration file.
> **Diun:** A short YAML file plus one label per container you want watched.

**Scope of a single run**

> **Ripen:** One Service, on one stack that opted in.
> **Watchtower:** Every container in scope that has a newer image, in one cycle. Scope is every container on the daemon unless narrowed.
> **Diun:** Not applicable.

---

## 5. Consequences for the landing page

1. **Head the Watchtower column with the fork and footnote the archive.** Getting this backwards is the single highest-cost error available, in either direction.
2. **Do not claim the maturity window as unique.** Claim it as on-by-default and observation-clocked. §4 has the wording.
3. **Put the losses in the table, not in a footnote.** Setup cost, notification targets, and platform coverage are three rows where Ripen loses outright. A table with three honest losses in it reads as a table someone checked. A clean sweep reads as marketing, and the reader discounts everything.
4. **Give Diun "by design" cells rather than empty or red ones**, and quote the maintainer's 2019 statement once in prose. It is the cleanest possible evidence that Diun's narrowness is intentional.
5. **Quote Watchtower's own scope statement** — "We do **not** recommend using Watchtower in a commercial or production environment" — instead of characterizing its risk in our words. It makes the same point, in theirs, and cannot be argued with.
6. **Recheck before publishing.** The fork ships roughly weekly, and `--cooldown-delay` is evidence it is moving toward, not away from, the ground Ripen occupies. Any row asserting an absence in Watchtower has a shelf life.
