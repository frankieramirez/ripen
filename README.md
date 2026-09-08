# Ripen

![Ripen — a pixel-art peach beside golden lettering](docs/assets/ripen-banner.svg)

[![CI](https://github.com/frankieramirez/ripen/actions/workflows/ci.yaml/badge.svg)](https://github.com/frankieramirez/ripen/actions/workflows/ci.yaml)
[![Latest release](https://img.shields.io/github/v/release/frankieramirez/ripen)](https://github.com/frankieramirez/ripen/releases)
[![License](https://img.shields.io/github/license/frankieramirez/ripen)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/frankieramirez/ripen/badge)](https://securityscorecards.dev/viewer/?uri=github.com/frankieramirez/ripen)

**Fail-closed container image updates for Portainer, Docker Compose, and Podman Compose.**

Ripen watches image registries and waits for a new digest to mature before it
can update a service. You choose which stacks may update automatically. Ripen
verifies service health after each update and rolls back if verification fails.
Git-backed stacks receive a pull request for human review.

[Quick start](#quick-start) · [Run continuously](#run-continuously) ·
[How it works](#how-a-transaction-works) · [Safety limits](#safety-limits) ·
[Documentation](#documentation)

> [!WARNING]
> Ripen recreates containers. Start in monitor mode, review what it records, and
> only then decide whether any stack should carry `auto_apply: true`.

## Quick start

### 1. Install Ripen

Download a binary from [Releases](https://github.com/frankieramirez/ripen/releases)
(see [release verification](#security)), or install with Go:

```bash
go install github.com/frankieramirez/ripen/cmd/ripen@latest
```

With Nix, run `nix run github:frankieramirez/ripen -- version`.
For the container image, see [Run in a container](#run-in-a-container).

### 2. Create a policy

For a local Compose stack, run Ripen where the Docker or Podman Compose CLI can
reach your engine. The Compose file and its directory must be writable.
Read the [Compose setup guide](docs/compose.md) for engine requirements and
rootless connections. Ripen refuses the privileged Docker socket.

Save this as `policy.yaml`, adapting the stack path, service name, and health URL
to your deployment:

```yaml
mode: monitor
state_file: ./ripen.db

stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    expected_services: [jellyfin]
    health:
      target: http://127.0.0.1:8096/health
```

For Portainer, follow the [Portainer setup guide](docs/portainer.md) to configure
its API credentials and TLS trust. The [example policy](config.example.yaml)
shows both backends; [Configuration](docs/configuration.md) documents every field.

### 3. Run once in monitor mode

```bash
ripen run --mode monitor --config policy.yaml
```

The first run records the running digest as the **Baseline**. Later runs report a
**Candidate** when the registry moves. A Candidate matures after
`candidate_min_age_seconds` (one day by default) and a second observation.
Monitor mode leaves your services unchanged.

### 4. Inspect the results

| Command | What it shows |
| --- | --- |
| `ripen status --config policy.yaml --pretty` | Configured services and their current state |
| `ripen candidates --config policy.yaml --pretty` | Candidates and whether they have matured |
| `ripen explain media --config policy.yaml --pretty` | Why Ripen would or would not act on the stack |
| `ripen audit --config policy.yaml --pretty` | Recorded actions |

Omit `--pretty` for the JSON Response envelope used by scripts and agents. Ripen
never infers this flag from a TTY. See [Agents](docs/agents.md) for the response
format and exit codes.

## Run continuously

```bash
ripen daemon --config policy.yaml
```

The daemon runs every `check_interval_seconds` and writes its Event stream to
stderr. Keep `mode: monitor` while reviewing observations; see
[Configuration](docs/configuration.md) before enabling Apply.

**Check progress in the logs.** `status` reads stored state, so a successful
response does not prove that the daemon is making progress. Look for
`run.finished` Events. Notifications are off unless configured; use
`ripen notify test --config policy.yaml` to verify delivery after following the
[Notifications guide](docs/notifications.md).

**An open Circuit breaker blocks updates and Proposals.** When Apply reports an
open breaker, the daemon runs Monitor in the same cycle to keep Candidate
observations current. Blocked Apply runs emit `run.finished` with
`breaker_open: true` and the recorded reason. A person must clear the breaker
before updates or Proposals can resume.

### Run in a container

The published image includes Ripen and CA certificates. Use it with a Portainer
policy; local Compose backends also need an engine CLI, which this image does
not include.

Set `state_file: /data/ripen.db` in your policy. Create a writable `./data`
directory for the container's UID/GID `65532:65532`, and mount the credential
file at the path configured by `portainer.api_key_file`:

```yaml
services:
  ripen:
    image: ghcr.io/frankieramirez/ripen:latest
    command: ["daemon", "--config", "/config/policy.yaml"]
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    volumes:
      - ./policy.yaml:/config/policy.yaml:ro
      - ./data:/data
      - ./portainer-api-key:/run/secrets/portainer-api-key:ro
```

Ensure the container user can read the policy and credential file. If you use a
custom CA, mount that file read-only at `portainer.tls_ca_file` too. Health check
URLs must be reachable from inside the container.

## How a Transaction works

1. **Observe.** Read what is deployed and what is running, and ask the registry
   what the tag points at now.
2. **Baseline.** The first time, record the running digest only if it can be proven. If an update is already pending, Ripen refuses to guess.
3. **Ripen.** A new digest becomes a Candidate. It must be seen twice and be
   older than the maturity window before it is eligible for anything.
4. **Apply**, in apply mode, on a stack that opted in: check every configured
   service's health first, pin exactly one image to `tag@sha256:…`, deploy, and
   verify every service again.
5. **Roll back** if verification fails: restore the Baseline digest and open the
   Circuit breaker. Further updates and Proposals stay blocked until a person clears
   it with a reason. Monitor and reads continue.

Git-backed stacks replace step 4 with a Proposal: one deterministic pull request
pinning the digest, which Ripen opens and never merges.

## Safety limits

| Limit | Behavior |
| --- | --- |
| Privileged Docker socket | Ripen refuses it at configuration load. |
| Update scope | One service per run, only where you opted in. |
| Proposals | Ripen opens a pull request and leaves merging to a person. |
| Portainer TLS | An explicit CA file or exact certificate fingerprint is required. |
| Agent permissions | MCP has no tools to apply updates or clear the Circuit breaker. |

[Roadmap](ROADMAP.md) covers non-goals and possible future work.

## Documentation

| Page | What it covers |
| --- | --- |
| [Configuration](docs/configuration.md) | Every policy field, and what refusing to start protects |
| [Portainer](docs/portainer.md) | The API backend, its least-privilege user, and TLS trust |
| [Compose](docs/compose.md) | Docker and Podman Compose, drift, and rootless sockets |
| [Agents](docs/agents.md) | The CLI and MCP surface, envelopes, exit codes |
| [Proposals](docs/proposals.md) | Git-backed stacks and the pull-request transaction |
| [Notifications](docs/notifications.md) | The Event stream, the webhook Notifier, suppression |
| [Architecture](docs/architecture.md) | How the pieces fit and why they are shaped this way |
| [Troubleshooting](docs/troubleshooting.md) | What each result code means and what to do about it |

The vocabulary in all of them is defined once in [`CONTEXT.md`](CONTEXT.md).

## Security

Ripen holds credentials for the systems that run your services. Read
[`SECURITY.md`](SECURITY.md) before deploying it, and report anything you find
through GitHub's private vulnerability reporting rather than an issue.

Every release archive carries GitHub build provenance. Check the one you
downloaded before you extract it:

```bash
gh attestation verify ripen_<version>_linux_amd64.tar.gz --repo frankieramirez/ripen
```

The checksums file is attested the same way.

## Contributing

Open an issue before a pull request. See [`CONTRIBUTING.md`](CONTRIBUTING.md).
This is a project maintained for its author's own use; contributions are
welcome and reviewed on a best-effort basis.

## License

MIT. See [`LICENSE`](LICENSE).
