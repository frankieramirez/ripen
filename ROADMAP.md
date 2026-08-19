# Roadmap

Ripen is maintained for its author's own use, and shared because it may be
useful. There are no dates here. What follows is what it will never do, and what
it might do next.

## Non-goals

These are decided, not deferred. An issue asking for one of them will be closed
with a link to this section.

### The privileged Docker socket

Ripen will never mount `/var/run/docker.sock` or an equivalent. A socket that
grants the full host engine API is root on the host, and an updater with root on
the host is a worse problem than an out-of-date image. A rootless user socket is
a narrower, opt-in connection and is supported; the privileged one is refused at
config load, symlinks and all.

### Unattended updating

One Service per run, only where the policy opted in, only after a Candidate has
matured. Ripen will not gain a mode that sweeps a host, updates everything, or
runs continuously without a waiting window. The latency is the feature.

### Merging its own Proposals

Ripen opens a pull request and stops. It will not merge, will not close, and will
not deploy from Git. That is your workflow's job, and the review in the middle is
the point.

### Insecure TLS

A CA file or an exact certificate fingerprint. There will be no
`insecure_skip_verify`, no "just this once" flag, and no environment variable
that turns verification off.

### An agent or browser path to applying

The MCP surface has no tool to apply an update or clear the Circuit breaker, and
the Web UI has no button for either. Not disabled — absent. Applying an update
and clearing a breaker are decisions a person makes at a terminal, with a reason
that gets recorded.

### Anything that deletes

Ripen does not prune images, remove volumes, delete stacks, or start stopped
ones. It changes one image reference, or it changes nothing.

## Possible later

Roughly in order of how likely they are, and each one still subject to the
non-goals above.

- **More Notifier adapters.** The Event stream already fans out to sinks; a
  second sink is a small amount of code. The webhook covers most setups through
  a relay.
- **Metrics.** A Prometheus endpoint on the daemon, read-only, off by default.
- **Other forges.** GitLab and Gitea Proposals. The proposal port exists; only
  the adapter is missing.
- **Non-HTTP health checks.** TCP connect, and a command exit status.
- **Private registries.** Authenticated pulls, with credentials handled the way
  every other secret here is: a file, never the policy document.
- **Maintenance windows.** Apply only inside a stated window.
- **A fuller operator interface.** The Web UI is deliberately a read-only view of
  what Ripen already knows. It could show more of that.
- **Kubernetes.** A genuinely different orchestrator with genuinely different
  semantics. Not soon, and not at the cost of the Transaction's shape.
- **NAS vendor interfaces and Quadlets.** Interesting, unexplored.

## How to ask for something

Open an issue. Say what you are trying to do rather than which feature you want,
and say what your setup looks like. If it lands under a non-goal, the answer will
be no with a reason, which is more useful than a maybe.
