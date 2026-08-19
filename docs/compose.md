# The Compose backend

Ripen drives Docker Compose and Podman Compose by running the engine's own
compose command. There is no daemon client, no socket mount, and no third-party
tooling: if `docker compose ps` works in your shell, Ripen works.

```yaml
compose:
  docker:
    binary: docker
    socket: /run/user/1000/docker.sock   # optional
  podman:
    binary: podman

stacks:
  media:
    enabled: true
    backend: docker-compose      # or podman-compose
    file: /srv/media/compose.yaml
    project: media               # defaults to the stack name
    expected_services: [jellyfin]
    health:
      target: http://127.0.0.1:8096/health
```

Podman goes through `podman compose`, the engine's built-in wrapper — never the
separate `podman-compose` Python tool.

## The startup probe

Before it touches a stack, Ripen runs `<binary> compose version --format json`
once per process. A missing binary, an unusable engine, or a compose that cannot
speak JSON is `ENGINE_UNAVAILABLE`: that backend's stacks drop out of the run and
everything else carries on. It is not a stack fault and never opens the breaker.

## Identity

The stack name in the policy is the identity everywhere: in state, in events, in
the audit trail. `project:` defaults to it, and Ripen always passes
`--project-name` explicitly rather than letting the engine infer one from a
directory name.

## Connection

The compose CLI runs as whoever runs Ripen. To point it at a rootless engine,
give the backend a socket:

```yaml
compose:
  podman:
    socket: /run/user/1000/podman/podman.sock
```

That becomes `DOCKER_HOST` for Docker and `CONTAINER_HOST` for Podman.

A socket that resolves to `/var/run/docker.sock` or `/run/docker.sock` — directly
or through any symlink in the chain — **refuses at config load**. The privileged
socket is root on the host, and it is permanently out of scope. Every step of the
chain is checked, so pointing at a path that is itself a symlink to the
privileged socket does not get around it.

## What drift means here

Ripen fingerprints, at observe time:

- the raw bytes of the Compose file;
- the bytes of every env file the document declares, plus the implicit `.env`
  beside it, recorded by path;
- the resolved service-name set.

Anything in that set changing between planning and applying cancels the apply as
`DRIFTED`. An env file the document declares but that is not there makes the
stack `INELIGIBLE` — its absence changes what the engine would deploy, and Ripen
does not act on a document it cannot read in full.

Symlinked Compose paths are resolved when the policy loads, so drift is recorded
against the real file.

## Writability, checked early

At observe time Ripen proves it could rewrite the file: the file itself, and the
directory, because the rewrite is an atomic rename. A read-only mount is a
problem to find while observing, not halfway through a Transaction.

## Applying

An apply rewrites exactly one `image:` scalar in place, pinning it to
`tag@sha256:…`, and writes the file through a temporary file and a rename. The
rest of the document — comments, anchors, key order, the header your future self
will read — is byte-for-byte what it was.

Then `<binary> compose --project-name … --file … up --detach --no-build`.

Two consequences worth knowing:

- **An interpolated image line cannot be pinned.** If the file says
  `image: ghcr.io/example/web:${TAG}`, there is no literal reference to rewrite,
  and the stack is `INELIGIBLE` for apply rather than rewritten wrongly. Monitor
  mode still watches it perfectly well.
- **There is no repull path.** A compose apply pins an exact digest, so there is
  nothing for a repull to resolve.

## Verification

Verification is conjunctive. Every configured Service must have a running
container, and one with a healthcheck must be reporting healthy, **and** every
configured functional health check must pass. One stopped sibling blocks the
whole Transaction.

## Rollback

The Compose file is restored before the engine is asked to converge — always,
even if the engine call then fails. What is written back is the pre-apply
document with that one image scalar pinned to the Baseline digest. In steady
state that is byte-for-byte what was there, since every apply pins. The pin
matters on the first apply over a mutable tag: restoring the bare tag would
redeploy the new image the engine has now cached, which is the opposite of a
rollback.

## Git-backed Compose stacks

A Compose stack with `git_path` set is a Proposal stack: Ripen opens a pull
request and never edits the local file. This is explicit and never inferred —
Ripen does not go looking for a `.git` directory. See [Proposals](proposals.md).
