# Architecture

Ripen is one binary with one job: decide, safely, whether a Service should be
running a different digest, and prove it afterwards. Everything below serves
that.

## The shape

```
  surfaces        cli   daemon   mcp   web ui
                    \     |      /      /
                     \    |     /      /
  assembly            [ internal/app ]
                     /    |      \
  the decision      [ internal/updater — the Transaction ]
                   /    |     |      \        \
  ports      backend  registry  health  proposal  event sink
               |         |        |        |         |
  adapters  portainer  registry  http   github    stderr + webhook
            compose
                          \                        /
  durability               [ internal/state — SQLite ]
```

`updater` is the deep module. It owns the whole Transaction and talks only to
ports; it opens no sockets and touches no files. Every adapter is replaceable and
every one of them is faked in the tests, which is why the engine's behavior can
be tested exhaustively without Docker, Portainer, or a registry.

## The Transaction

One run, one stack at a time, at most one Service changed.

**Observe.** The backend reports what is deployed: the Compose document, the
resolved services, and — where it can prove them — the digests actually running.
The engine parses each managed Service's image reference and asks the registry
what that tag points at now.

**Baseline.** The first time a Service is seen, whatever is provably running
becomes its Baseline. Where the backend can only say "outdated", the running
digest is unknown, and Ripen refuses to baseline rather than blessing an update
nobody reviewed (`BASELINE_BLOCKED`).

**Ripen.** A digest that differs from the Baseline is a Candidate. It has to be
seen at least twice and be older than `candidate_min_age_seconds` before it is
eligible for anything. This is deliberate latency: most bad releases are
withdrawn or fixed within a day.

**Apply**, only in apply mode, only where the policy opted in, and only for one
Service in the whole run:

1. Re-observe and compare the drift fingerprint. Anything different between
   planning and applying is `DRIFTED`, and nothing happens.
2. Health-check every configured Service, including health-only ones. A stack is
   only ever mutated from a healthy state.
3. Rewrite exactly one `image:` scalar to `tag@sha256:…` and deploy.
4. Verify: every Service running and healthy, and the running digest is the one
   we asked for.

**Fail closed.** If verification does not come good inside
`verification_timeout_seconds`, roll back to the Baseline digest and open the
Circuit breaker. If the rollback does not verify either, the result is
`ROLLBACK_FAILED` and the breaker is open with a reason naming the Service.

While the breaker is open, Ripen takes **no outbound action**: no apply, no
Proposal. Monitor observation and every read carry on, so `ripen status` still
answers while you work out what happened.

**Ambiguity is not failure.** A deploy call that times out may well have landed.
Ripen re-checks image status, running digest, and health before deciding, and
accepts a deployment that actually succeeded rather than rolling back something
healthy.

## The state store

SQLite, WAL, one connection, schema v1:

| Table | Holds |
| --- | --- |
| `accepted_digests` | The Baseline per Service. |
| `candidates` | Observed digests, with first seen, last seen, and count. |
| `pending_proposals` | Open Proposals. |
| `attempts` | The audit trail: run id, actor, digests, result, detail. |
| `breaker` | The Circuit breaker and the reason it opened. |
| `lease` | The exclusive run lease. |
| `notifier_health`, `notifier_destination`, `notification_suppression` | Delivery health and what has already been paged. |

Identity is three columns — `backend`, `stack`, `service` — rather than one
string that pretended to be a name. `service` is empty for a stack-level policy.

The lease is taken with `BEGIN IMMEDIATE` for the life of a run, so two Ripen
processes can never act at once; the second reports `BUSY` and exits.

There is no migration from the Python schema. This is v1; an existing deployment
starts cold and re-baselines on its first Monitor run.

## The registry client

A deliberately minimal, read-only client: https only, anonymous or bearer token,
never pulls image content. It resolves what digest a tag points at, and for a
multi-arch index it selects the one manifest matching the platform — with ARM
variants disambiguated — rather than reporting the index digest, because that is
what the engine will report as running.

It is differential-tested against `google/go-containerregistry`: both resolve the
same index from the same fake registry and must agree. Two deliberate
divergences: an ambiguous platform match is an error rather than a fallback, and
digests must match the full expected shape.

## The surfaces

All four are built from the same `app`, so they cannot answer the same question
differently.

- **CLI** — every verb, one Response envelope (or `--pretty` on reads), four exit codes.
- **Daemon** — the CLI's `run` on an interval. Writes nothing to stdout.
- **MCP** — a strict subset of the CLI. Apply mode and clearing the breaker have
  no tools at all.
- **Web UI** — optional, off by default, read-only, embedded.

Reads need only the policy and the state store. The network clients and
credentials live behind one call that read paths never make, which is what lets
the read-only MCP surface run without loading a single secret.

## Why it is shaped this way

**Fail closed.** Every ambiguity resolves toward doing nothing. An unreadable env
file, an unexpected service, a drifted fingerprint, an unprovable digest: all of
them stop the Transaction rather than guessing.

**One thing at a time.** One Service per run, one run at a time. When something
goes wrong there is exactly one change to look at.

**The state store is the system of record.** Events report; they never inform.
Every paging Event corresponds to a state change already written.

**Absence over permission.** Where something must not be possible, there is
nothing to call: no Docker socket client, no MCP apply tool, no button to clear
the breaker, no insecure TLS flag.

**Read it back.** An apply rewrites one scalar and leaves the rest of the
document byte-for-byte intact, because the file an operator opens afterwards
should be the file they wrote.

The reasoning behind the rewrite itself is in
[`docs/adr/0001-rewrite-in-go.md`](adr/0001-rewrite-in-go.md) and
[`docs/adr/0002-release-shape.md`](adr/0002-release-shape.md); the full design
and its verification gate are in [`docs/rework/SPEC.md`](rework/SPEC.md).
