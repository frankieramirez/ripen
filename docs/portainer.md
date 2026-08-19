# The Portainer backend

Ripen drives Portainer through its HTTP API as a dedicated, limited user. It
never touches the Docker socket, and it never acts on a stack the policy does
not name.

```yaml
portainer:
  base_url: https://portainer.example:9443
  api_key_file: /run/secrets/portainer-api-key
  expected_username: ripen
  tls_fingerprint_sha256: "…64 hex characters…"

stacks:
  arr:
    enabled: true
    backend: portainer      # the default, so this line is optional
    expected_services: [radarr, sonarr]
    services:
      radarr: { auto_apply: true, health: { target: http://127.0.0.1:7878/ping } }
      sonarr: { auto_apply: true, health: { target: http://127.0.0.1:8989/ping } }
```

## The automation user

Create a **Standard User** in Portainer, give it access to exactly the stacks
Ripen may manage, and generate an API key for it. Nothing else.

`expected_username` is checked before any inventory work happens, on every run.
If the key authenticates as somebody else — because it was rotated, copied from
another instance, or pasted wrong — the run stops before it looks at a single
stack. A key that acts as an administrator can see and change things the
reviewed policy never described, so this is not a per-stack problem to report
and continue past.

Store the key in a file with mode `0600` and mount it read-only. A key with
interior whitespace is rejected at startup; a trailing newline is fine.

## TLS trust

Configure exactly one:

- `tls_fingerprint_sha256` — the exact SHA-256 of Portainer's leaf certificate,
  64 hex characters. Chain and hostname verification are replaced by a
  constant-time comparison against that pin, checked on resumed sessions too.
  This is the right choice for the usual self-signed NAS certificate.
- `tls_ca_file` — a PEM bundle to trust instead of the system roots.

Both is a configuration error. Neither is a configuration error. `http://` is a
configuration error. There is no insecure mode and no flag to add one.

To read the fingerprint from the server:

```bash
openssl s_client -connect portainer.example:9443 </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256
```

## What Ripen asks Portainer for

- The authenticated user, once per run, before anything else.
- The stack list, to find the stacks the policy names and to see which are
  Git-backed.
- The stack file, which is the Compose document Ripen reads and rewrites.
- The stack's image status, for a single-service stack.
- Running container digests, scoped by the stack's Compose project label, for a
  multi-service or Git-backed stack.

Container discovery is always filtered to the stack's own Compose project and to
running containers. Ripen never lists every container on the host, and it will
refuse a response containing a container from another project.

When a running container was created from a `tag@sha256:…` reference, that pin is
what Ripen believes — even when the image store lists several repository digests
for the same local image.

## Updates

An update is one `PUT` of new Compose content for one stack, with `Prune: false`.
Only the target Service's `image:` line differs from what was there.

- For a multi-service stack, the new content pins that one image to
  `tag@sha256:…` and `RepullImageAndRedeploy` is false: the digest says exactly
  what to run.
- For a single-service stack observed only through image status, the content is
  unchanged and repull is true.

Deployments get a longer timeout than reads (ten minutes by default), because a
pull can take that long. A deploy that times out is treated as *ambiguous*, not
failed: Ripen re-checks image status and health before deciding, and accepts a
deployment that actually landed rather than rolling back something healthy.

## Git-backed stacks

If Portainer reports a stack as Git-backed, Ripen never updates it directly —
the adapter refuses before making any HTTP call. Set `git_path` on the stack and
Ripen will open a [Proposal](proposals.md) instead.

## What can go wrong

| Symptom | Cause |
| --- | --- |
| The run fails with "the Portainer API key belongs to …" | The key is not the expected user. Nothing was touched. |
| `not_visible` | The stack is in the policy but the automation user cannot see it. |
| `ineligible: stack "x" is not active` | Portainer reports the stack as stopped. Ripen never starts stopped stacks. |
| `ineligible: services changed` | The live Compose service set no longer matches `expected_services`. Review it and update the policy deliberately. |
| `portainer returned no running containers for project "x"` | The Compose project label does not match the stack name, or nothing is running. |

More in [Troubleshooting](troubleshooting.md).
