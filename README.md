# Ripen

[![CI](https://github.com/frankieramirez/ripen/actions/workflows/ci.yaml/badge.svg)](https://github.com/frankieramirez/ripen/actions/workflows/ci.yaml)
[![Latest release](https://img.shields.io/github/v/release/frankieramirez/ripen)](https://github.com/frankieramirez/ripen/releases)
[![License](https://img.shields.io/github/license/frankieramirez/ripen)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/frankieramirez/ripen/badge)](https://securityscorecards.dev/viewer/?uri=github.com/frankieramirez/ripen)

**Fail-closed image updates for Portainer and Compose. A digest ripens. You apply.**

Ripen watches the registries behind the images you already run. When a new digest
appears it waits, watches it again, and tells you. If you have said so explicitly,
it will update one service — pinned to an exact digest, verified afterwards, and
rolled back the moment health fails. Then it stops and waits for you.

It never mounts the Docker socket.

```console
$ ripen status
{"schema_version":1,"command":"status","occurred_at":"2026-08-19T09:14:22Z","ok":true,"data":{
  "breaker":{"open":false,"reason":null},
  "services":[{"backend":"docker-compose","stack":"media","service":null,
    "baseline":"sha256:6f8c…","candidate":{"digest":"sha256:19ab…","observations":2,"mature":true}}]}}
```

> [!WARNING]
> Ripen recreates containers. Start in monitor mode, read what it records, and
> only then decide whether any stack should carry `auto_apply: true`.

## Quick start

Ripen needs a policy file and somewhere to keep its state. Nothing else.

```bash
# 1. Get the binary
go install github.com/frankieramirez/ripen/cmd/ripen@latest
# or: docker pull ghcr.io/frankieramirez/ripen
# or: grab a signed archive from the Releases page

# 2. Describe exactly what Ripen may look at
cp config.example.yaml policy.yaml
$EDITOR policy.yaml

# 3. Watch, and only watch
ripen run --mode monitor --config policy.yaml
```

A minimal policy for one Compose stack:

```yaml
mode: monitor
state_file: /var/lib/ripen/ripen.db

stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    expected_services: [jellyfin]
    health:
      target: http://127.0.0.1:8096/health
```

Or as a container, which is how most people run it:

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
      - /srv/media/compose.yaml:/srv/media/compose.yaml   # only for compose backends
```

No socket mount. Ever.

The first run records what is running now as the **Baseline** — nothing else.
Later runs report a **Candidate** when the registry moves. After
`candidate_min_age_seconds` (a day, by default) and a second sighting, that
Candidate is mature and apply mode may act on it.

```bash
ripen status       # every configured service and where it stands
ripen candidates   # what is waiting, and whether it has matured
ripen explain media  # why the next run would, or would not, act
ripen audit        # what Ripen has actually done
```

Run it on a schedule with `ripen daemon`, which does the same thing every
`check_interval_seconds` and writes its Event stream to stderr.

## How a Transaction works

1. **Observe.** Read what is deployed and what is running, and ask the registry
   what the tag points at now.
2. **Baseline.** The first time, record the running digest — and only if it can
   be proven. If an update is already pending, Ripen refuses to guess.
3. **Ripen.** A new digest becomes a Candidate. It must be seen twice and be
   older than the maturity window before it is eligible for anything.
4. **Apply**, in apply mode, on a stack that opted in: check every configured
   service's health first, pin exactly one image to `tag@sha256:…`, deploy, and
   verify every service again.
5. **Roll back** if verification fails: restore the Baseline digest and open the
   Circuit breaker. Ripen takes no further outbound action until a person clears
   it with a reason.

Git-backed stacks replace step 4 with a Proposal: one deterministic pull request
pinning the digest, which Ripen opens and never merges.

## What it will not do

- **No privileged Docker socket.** Permanently out of scope, not a roadmap item.
- **No unattended sprees.** One service per run, and only where you opted in.
- **No self-merging.** A Proposal is a pull request for a human to review.
- **No insecure TLS.** A CA file or an exact fingerprint. There is no bypass.
- **No agent path to apply.** The MCP surface cannot apply an update or clear
  the breaker; those tools do not exist.

[`ROADMAP.md`](ROADMAP.md) has the full list of non-goals and what may come later.

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

## Contributing

Issue first, then a pull request — see [`CONTRIBUTING.md`](CONTRIBUTING.md).
This is a project maintained for its author's own use; contributions are
welcome and reviewed on a best-effort basis.

## License

MIT. See [`LICENSE`](LICENSE).
