# Security Policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
vulnerability reporting feature for this repository.

Include the affected version, reproduction steps, and impact. Do not include
real Portainer API keys, GitHub tokens, Compose environment secrets, or private
network data.

## Deployment boundaries

`ripen` can redeploy authorized Portainer stacks. Treat its API key,
policy file, state database, and Docker networks as sensitive infrastructure.

- Give the Portainer automation user access only to stacks it may update.
- Multi-service updates read running container image digests through the
  authorized Portainer Docker proxy; keep Portainer RBAC limited to reviewed
  stacks and do not grant direct Docker-socket access.
- Mount the API key from a read-only file; never put it in Compose or Git.
- Do not mount the Docker socket.
- Use HTTPS with a CA file or an exact certificate fingerprint.
- Begin in monitor mode and inspect the baseline before enabling apply mode.
