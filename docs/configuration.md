# Configuration

Everything Ripen may touch is in one file. A stack that is not in the policy
does not exist as far as Ripen is concerned.

The file is validated completely at startup, and validation is fail-closed:
unknown fields, ambiguous rules, and unsafe values stop the process rather than
producing a warning nobody reads. A typo must never silently disable a safety
rule.

Start from [`config.example.yaml`](../config.example.yaml).

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

A configured Compose socket that resolves to `/var/run/docker.sock` or
`/run/docker.sock` — directly or through a symlink chain — refuses at config
load. The privileged socket is out of scope permanently.

## Proposals

```yaml
github:
  repository: owner/repository
  base_branch: main
  token_file: /run/secrets/github-token
```

Required whenever any stack sets `git_path`. The token file must not be readable
by group or others; Ripen refuses to start otherwise. See
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

Absent means silent-but-logging: the Event stream on stderr still records
everything. An unknown Event name in `events` is a startup error. See
[Notifications](notifications.md).

## The web interface

```yaml
ui:
  enabled: true
  address: 127.0.0.1:7476
  token_file: /run/secrets/ui-token
```

Off unless `enabled: true`. A non-loopback address refuses to start without a
token, supplied by `token_file` or `RIPEN_UI_TOKEN`. There is no insecure
escape hatch.

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
