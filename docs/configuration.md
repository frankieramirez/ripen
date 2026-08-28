# Configuration

Everything Ripen may touch is in one file. A stack that is not in the policy
does not exist as far as Ripen is concerned.

The file is validated completely at startup, and validation is fail-closed:
unknown fields, ambiguous rules, and unsafe values stop the process rather than
producing a warning nobody reads. A typo must never silently disable a safety
rule.

Start from [`config.example.yaml`](../config.example.yaml). Copy it to
`policy.yaml`, edit it, and keep it out of Git — it names your hosts and the
paths to your secrets.

The example file is a working starting point, not the reference. It carries no
annotations and shows only the sections a first install needs. This page is the
reference: every field, its default, and what refuses. [Everything
together](#everything-together) at the end is the whole policy in one block,
optional sections included.

## Where the file comes from

```bash
ripen status --config /etc/ripen/policy.yaml
RIPEN_CONFIG=/etc/ripen/policy.yaml ripen status
```

`--config` wins, then `RIPEN_CONFIG`, then `/etc/ripen/policy.yaml`.

## Run settings

| Field | Default | What it does |
| --- | --- | --- |
| `mode` | `monitor` | `monitor` observes and records. `apply` may additionally change one Service or open one Proposal. |
| `max_updates_per_run` | `1` | v1 requires `1`. One Service per run, always. |
| `candidate_min_age_seconds` | `86400` | How long a new digest must exist before it may be applied. |
| `verification_timeout_seconds` | `300` | How long health checks have to come good after a deploy. |
| `lease_ttl_seconds` | `1800` | How long one run may hold the exclusive lease. |
| `check_interval_seconds` | `86400` | How often `ripen daemon` runs a cycle. |
| `state_file` | `/data/updater.db` | The state database. |

The state database is the system of record. Baselines, Candidates, Proposals,
the audit trail, the Circuit breaker, and Notifier health all live there. Back it
up; losing it means Ripen re-baselines from scratch on the next Monitor run.

## Stacks

```yaml
stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    project: media
    auto_apply: false
    git_path: stacks/media/compose.yaml
    expected_services: [jellyfin]
    health:
      type: http
      target: http://127.0.0.1:8096/health
      accepted_status: [200]
      timeout_seconds: 5
```

| Field | Applies to | Meaning |
| --- | --- | --- |
| `enabled` | all | A stack Ripen may observe. Off by default. |
| `backend` | all | `portainer` (default), `docker-compose`, or `podman-compose`. |
| `file` | compose | The Compose file Ripen reads and pins. Required; symlinks are resolved at load. |
| `project` | compose | The Compose project name. Defaults to the stack name and is always passed explicitly. |
| `auto_apply` | single-service | Apply mode may act on this stack. Off by default, and required on top of `mode: apply`. |
| `git_path` | all | The same Compose file's path inside the repository. Its presence turns the stack into a Proposal stack. |
| `expected_services` | all | The exact service set. Any difference is `INELIGIBLE`. |
| `health` | single-service | The functional health check. |
| `services` | multi-service | Per-service rules. Required whenever more than one service is expected. |

**Declaration order matters.** With one update per run, the first mature
Candidate in file order is the one that goes.

### Multi-service stacks

A stack expecting more than one service must describe each of them, and may not
also set stack-level `auto_apply` or `health` — a rule that could be read two
ways is a configuration error:

```yaml
    expected_services: [radarr, sonarr, flaresolverr]
    services:
      radarr:
        auto_apply: true
        health: { target: http://127.0.0.1:7878/ping }
      sonarr:
        auto_apply: true
        health: { target: http://127.0.0.1:8989/ping }
      flaresolverr:
        enabled: false
        health: { target: http://127.0.0.1:8191/health }
```

`enabled: false` makes a Service **health-only**: Ripen never resolves its image
against a registry and never updates it, but its health check still gates its
siblings. If it is unhealthy, nothing in the stack is updated. At least one
Service must be enabled, and a disabled Service cannot set `auto_apply`.

Flags must be real YAML booleans. `"false"` in quotes is a string, and a string
where a boolean belongs is a startup error rather than a silent `true`.

### Health checks

```yaml
    health:
      type: http            # the only type in v1
      target: http://host:port/path   # `url:` is accepted as a synonym
      accepted_status: [200, 302]
      timeout_seconds: 5
```

| Field | Default | Meaning |
| --- | --- | --- |
| `type` | `http` | The only type in v1. |
| `target` | — | Required. `url` is accepted as a synonym. |
| `accepted_status` | `[200]` | Any other status is unhealthy. |
| `timeout_seconds` | `5` | A check that does not answer in time is unhealthy. |

A check that times out or refuses the connection is unhealthy — that is the
question it exists to answer. A check Ripen cannot run at all (an unsupported
type, a target that is not an http URL) is a configuration error.

## Backends

Configure only the ones you use. See [Portainer](portainer.md) and
[Compose](compose.md) for the detail.

```yaml
portainer:
  base_url: https://portainer.example:9443   # https only
  api_key_file: /run/secrets/portainer-api-key
  expected_username: ripen
  tls_fingerprint_sha256: "…64 hex characters…"   # or tls_ca_file, never both

compose:
  docker:
    binary: docker
    socket: /run/user/1000/docker.sock    # optional, rootless only
  podman:
    binary: podman
```

| Field | Default | Meaning |
| --- | --- | --- |
| `portainer.base_url` | — | Required for the Portainer backend. https only. |
| `portainer.api_key_file` | — | Required. The file holding the API key. |
| `portainer.expected_username` | — | Required. The run stops if the key belongs to someone else. |
| `portainer.tls_fingerprint_sha256` | — | 64 hex characters. Exactly one of this and `tls_ca_file`. |
| `portainer.tls_ca_file` | — | A CA bundle, as the alternative to a pin. |
| `compose.docker.binary` | `docker` | The engine Ripen shells out to. |
| `compose.podman.binary` | `podman` | The same, for Podman. |
| `compose.<engine>.socket` | unset | Optional rootless socket. The engine's own default is used when unset. |

There is no insecure mode for Portainer: a `base_url` that is not https, or
both or neither of the two TLS fields, refuses at startup.

Ripen shells out to the engine's own compose implementation and never touches a
privileged socket. A configured Compose socket that resolves to
`/var/run/docker.sock` or `/run/docker.sock` — directly or through a symlink
chain — refuses at config load. The privileged socket is out of scope
permanently.

## Proposals

```yaml
github:
  repository: owner/repository
  base_branch: main
  token_file: /run/secrets/github-token
```

All three fields are required whenever any stack sets `git_path`; there is no
default branch. The token file must not be readable by group or others — mode
`0600` — and Ripen refuses to start otherwise. Ripen opens a pull request
pinning the digest and never deploys it; your existing workflow does that. See
[Proposals](proposals.md).

## Notifications

```yaml
notifier:
  heartbeat_interval_seconds: 86400
  webhook:
    url_file: /run/secrets/webhook-url
    token_file: /run/secrets/webhook-token
    timeout_seconds: 10
    events: [breaker.opened, stack.error, stack.recovered]
```

| Field | Default | Meaning |
| --- | --- | --- |
| `heartbeat_interval_seconds` | unset | Deliver something even when nothing changed. Off when absent. |
| `webhook.url_file` | — | Required. The file holding the endpoint URL. |
| `webhook.token_file` | unset | Optional bearer token. |
| `webhook.timeout_seconds` | `10` | Per-delivery timeout. |
| `webhook.events` | the paging set | Which Events page. An unknown name is a startup error. |

Absent means silent-but-logging: the Event stream on stderr is always on and
records everything, and the webhook adds one outbound sink that pages on state
changes only. See [Notifications](notifications.md).

## The web interface

```yaml
ui:
  enabled: true
  address: 127.0.0.1:7476
  token_file: /run/secrets/ui-token
```

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Off unless set. |
| `address` | `127.0.0.1:7476` | A non-loopback address requires a token. |
| `token_file` | unset | Or the `RIPEN_UI_TOKEN` environment variable. |

It reads; it cannot change anything. A non-loopback address refuses to start
without a token. There is no insecure escape hatch.

## Exclusions

```yaml
exclude:
  - portainer
```

Names Ripen must never act on. A stack cannot be both enabled and excluded;
saying both is a contradiction, so it is an error.

## What validation refuses

Every one of these stops the process at startup:

- an unknown field anywhere in the document;
- `max_updates_per_run` greater than `1`;
- a stack that is both enabled and excluded;
- a multi-service stack without per-service rules, or with stack-level
  `auto_apply` or `health` alongside them;
- duplicate names in `expected_services`, or a `services` map that does not match
  it exactly;
- a stack where every Service is disabled;
- a quoted string where a boolean belongs;
- `accepted_status` that is empty or contains something that is not an HTTP
  status code;
- a Portainer `base_url` that is not https;
- both or neither of `tls_ca_file` and `tls_fingerprint_sha256`;
- a fingerprint that is not exactly 64 hex characters;
- `git_path` without a `github` section, or a path that is absolute, escapes the
  repository, or is not a YAML file;
- a Compose socket that resolves to the privileged Docker socket;
- an unknown Event name in `notifier.webhook.events`.

## Everything together

One policy with every section filled in, including the optional ones.
[`config.example.yaml`](../config.example.yaml) is the shorter starting point;
this is the shape of the whole thing.

```yaml
mode: monitor

max_updates_per_run: 1
candidate_min_age_seconds: 86400
verification_timeout_seconds: 300
lease_ttl_seconds: 1800

check_interval_seconds: 86400
state_file: /data/updater.db

compose:
  docker:
    binary: docker
    socket: /run/user/1000/docker.sock
  podman:
    binary: podman
    socket: /run/user/1000/podman/podman.sock

portainer:
  base_url: https://portainer.example:9443
  api_key_file: /run/secrets/portainer-api-key
  expected_username: ripen
  tls_fingerprint_sha256: "0000000000000000000000000000000000000000000000000000000000000000"

github:
  repository: owner/repository
  base_branch: main
  token_file: /run/secrets/github-token

notifier:
  heartbeat_interval_seconds: 86400
  webhook:
    url_file: /run/secrets/webhook-url
    token_file: /run/secrets/webhook-token
    timeout_seconds: 10
    events:
      - run.failed
      - transaction.succeeded
      - transaction.rolled_back
      - transaction.rollback_failed
      - breaker.opened
      - breaker.cleared
      - proposal.created
      - stack.error
      - stack.recovered

ui:
  enabled: true
  address: 127.0.0.1:7476
  token_file: /run/secrets/ui-token

stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    project: media
    auto_apply: false
    expected_services:
      - jellyfin
    health:
      type: http
      target: http://127.0.0.1:8096/health
      accepted_status: [200]
      timeout_seconds: 5

  arr:
    enabled: true
    backend: portainer
    expected_services:
      - radarr
      - sonarr
      - flaresolverr
    services:
      radarr:
        auto_apply: true
        health:
          target: http://127.0.0.1:7878/ping
      sonarr:
        auto_apply: true
        health:
          target: http://127.0.0.1:8989/ping
      flaresolverr:
        enabled: false
        health:
          target: http://127.0.0.1:8191/health

  blog:
    enabled: true
    backend: docker-compose
    file: /srv/blog/compose.yaml
    git_path: stacks/blog/compose.yaml
    auto_apply: true
    expected_services: [ghost]
    health:
      target: http://127.0.0.1:2368/

exclude:
  - portainer
```

`media` is a single-service Compose stack. `arr` is a multi-service Portainer
stack where `flaresolverr` is health-only. `blog` sets `git_path`, so it is a
Proposal stack: Ripen opens a pull request against `stacks/blog/compose.yaml`
and deploys nothing itself.
