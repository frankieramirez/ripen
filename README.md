# Ripen

Fail-closed image updates for Portainer. A digest ripens. You apply.

A safety-focused updater for Portainer-managed Docker Compose stacks. It checks
OCI registries for image changes without mounting the Docker socket, waits for a
candidate release to mature, redeploys an explicitly authorized stack, verifies
functional health, and rolls back to the previously accepted digest when needed.
For Git-backed stacks, it opens an exact digest-pin pull request and waits for
the repository's existing deployment workflow instead of mutating Portainer.

> [!WARNING]
> This project is alpha software that can recreate containers. Start in monitor
> mode, use a least-privilege Portainer account, and maintain tested backups.

## Safety defaults

- Runs in monitor mode unless apply mode is explicitly selected.
- Uses a dedicated Portainer Standard User and sees only stacks assigned to it.
- Requires two observations of a candidate digest separated by 24 hours.
- Updates at most one service in one stack per run.
- Opens a circuit breaker after every rollback.
- Never cleans up images or activates stopped stacks.
- Never merges its own pull requests or directly mutates Git-backed stacks.
- Allows up to ten minutes for Portainer image pulls and stack redeployments.
- Treats a timed-out redeploy response as successful only when both the image status and functional health check prove the update completed.

## Current scope

The updater intentionally supports a narrow transaction:

- Portainer-managed single-service stacks and explicitly reviewed multi-service stacks;
- one literal OCI image reference per managed service;
- public registries that support the OCI/Docker Registry HTTP API;
- HTTP functional health verification; and
- one service update per run.

Multi-service policies must define every expected service separately. Before and
after changing one service, the updater verifies every configured sibling's
health. The changed image remains on its reviewed tag but is pinned to the
selected platform digest, and rollback changes only that service. Private
registry credentials, scheduled maintenance windows, and image cleanup are not
currently supported.

For a Git-backed stack, set `github` at the policy root and `git_path` on the
stack. A mature candidate creates one deterministic, idempotent pull request.
The repository file must exactly match Portainer's reviewed live Compose source,
or the transaction fails closed. After an external merge and deployment, the
updater accepts the new baseline only when the running digest, committed digest
pin, and every configured health check agree.

A service may set `enabled: false` when its image channel is intentionally
retired or otherwise unsuitable for registry monitoring. It remains part of
the exact Compose shape and its health check still gates sibling updates, but
the updater does not resolve, baseline, or mutate its image.

When a running container was created from a `tag@sha256:digest` reference, that
container-level pin is authoritative. This remains unambiguous even when the
Docker image cache lists multiple repository digests for one local image ID.

## Quick start

1. Create a dedicated Portainer Standard User and grant it access only to the
   stacks the updater may manage.
2. Copy `config.example.yaml` to `policy.yaml` and keep that file out of Git.
3. Store the user's Portainer API key in `secrets/portainer-api-key` with mode
   `600`.
4. For Git proposals, store a fine-grained token in `secrets/github-token` with
   mode `600`. Scope it to one repository with Metadata read, Contents
   read/write, and Pull requests read/write. Do not grant administration or
   workflow permissions.
5. Create the private external Docker networks referenced by the Compose file
   and attach Portainer plus the health-checked application as appropriate.
6. Start with `docker compose -f compose.monitor.yaml up --build -d` and inspect
   the baseline before enabling apply mode.

## Local verification

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -e '.[dev]'
.venv/bin/pytest
.venv/bin/python -m compileall -q src tests
```

## Commands

```bash
ripen --config policy.yaml run --mode monitor
ripen --config policy.yaml status
ripen --config policy.yaml clear-breaker --reason "health verified manually"
ripen --config policy.yaml clear-proposal --stack arr/radarr --reason "PR closed"
ripen --config policy.yaml daemon --mode monitor
```

`clear-breaker` requires a human reason. Apply mode is additionally gated by `auto_apply: true`, a mature candidate, unchanged Compose/environment hashes, an available update slot, and a closed breaker.

## Configuration

Copy `config.example.yaml` to `policy.yaml`. Unknown fields are rejected. Configure exactly one TLS trust mechanism:

- `tls_ca_file` for a certificate chain trusted from a mounted CA file; or
- `tls_fingerprint_sha256` for the exact Portainer server certificate.

There is no insecure TLS mode.

Each enabled stack must list its exact expected service names. A multi-service
stack must also define an exact `services` map with per-service `auto_apply` and
health policy; ambiguous stack-level settings are rejected. If its Compose
shape, service set, running digest, or Portainer environment changes after
observation, the updater fails closed instead of applying an unreviewed
deployment.

## Container deployment

`compose.monitor.yaml` is the safest starting point. It has no published ports
and no Docker socket. It expects:

- the protected API key at `./secrets/portainer-api-key`;
- the optional fine-grained GitHub token at `./secrets/github-token`;
- a writable `./data` directory owned by UID/GID `1031`;
- an external private Docker network named `stack-control` shared with Portainer;
- any additional private network needed to reach the application's health URL;
- a completed `policy.yaml` next to the Compose file.

`compose.portainer.yaml` is a generic Portainer stack example using relative
bind mounts. Adjust its UID/GID, image publishing strategy, paths, and private
network names for your host. Creating networks and attaching existing services
are deployment operations and are intentionally not automated here.

## Update lifecycle

1. The first successful observation records each service's proven running digest.
2. A new digest must be observed twice and remain present for
   `candidate_min_age_seconds`.
3. Apply mode verifies the Portainer identity, stack shape, Compose hash,
   environment hash, running digest, and all configured service health checks.
4. Only the selected service image is changed to `tag@sha256:digest`; sibling
   service image references remain untouched.
5. Every configured service must pass its health check after deployment.
6. A failed update pins only the selected service back to its accepted digest
   and opens the circuit breaker for human review.

For Git-backed stacks, steps 4–6 are replaced by a pull-request transaction:
the updater verifies source parity and health, creates or reuses one deterministic
PR, records it as pending, and performs no deployment. Merge and deployment stay
outside the updater. A later cycle records success only after the live digest,
Compose pin, and health checks prove that exact proposal was deployed.

## Development

Pull requests should keep fail-closed behavior and include regression tests for
changes to update, timeout, health, or rollback handling. Run the local
verification commands before opening a pull request.

## Security

See [SECURITY.md](SECURITY.md). Never commit a real policy, Portainer API key,
GitHub token, state database, certificate, Compose environment secret, or
private host data.

## License

MIT
