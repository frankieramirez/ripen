# Working on Ripen

This file is for agents and people working on Ripen's own source. If you are
looking for how an agent *operates* Ripen, that is [`docs/agents.md`](docs/agents.md).

## What this project is

Ripen updates container images and refuses to guess. Every design decision bends
toward doing nothing when something is unclear. If you are about to make Ripen
more capable and less certain, stop and reconsider.

## Getting around

| Path | What lives there |
| --- | --- |
| `cmd/ripen` | The entry point. Nothing but a call into `internal/cli`. |
| `internal/updater` | The Transaction. The deep module; everything else serves it. |
| `internal/backend` | The orchestrator port every backend implements. |
| `internal/portainer`, `internal/compose` | The two backends. |
| `internal/registry` | A minimal read-only OCI registry client. |
| `internal/composefile` | Reading and pinning Compose files as text. |
| `internal/state` | SQLite, schema v1. The system of record. |
| `internal/app` | Assembly, and the read payloads every surface shares. |
| `internal/cli`, `internal/mcpserver`, `internal/webui`, `internal/daemon` | The four surfaces. |
| `internal/response` | The Response envelope and its generated schemas. |
| `internal/event`, `internal/notifier` | The Event stream and the webhook sink. |
| `docs/rework/SPEC.md` | The design, the behavior inventory, and the invariants. |
| `CONTEXT.md` | The vocabulary. Capitalized terms mean exactly what it says. |

## Before you change anything

```bash
go test -race ./...
golangci-lint run ./...
gofmt -l .
```

All three must be clean. CI runs the same, plus `govulncheck` and a cross-compile
of the release matrix.

If you changed anything in `internal/response`, regenerate the published schemas:

```bash
RIPEN_UPDATE_SCHEMAS=1 go test ./internal/response/
```

A test asserts `ripen schema` and `docs/schema/v1/` are identical, so forgetting
this fails the build rather than shipping a schema that lies.

## The rules that are not negotiable

These are invariants, each with a dedicated test. If a change makes one of them
fail, the change is wrong — not the test.

1. Every paging Event corresponds to a durable state change **already written**.
2. Nothing but protocol frames is written to stdout by `ripen mcp`.
3. `ripen daemon` writes nothing to stdout.
4. MCP read-only mode registers no write tools and constructs no network clients.
5. `actor` is set by the surface and can never be supplied by a caller.
6. An open breaker blocks Apply *and* Proposal creation; Monitor and reads continue.
7. No second Proposal while one is pending review.
8. `ripen schema` matches `docs/schema/v1/`.
9. No Event payload field can carry a secret.
10. A Compose socket resolving to the privileged Docker socket refuses at config load.

The full list, with the test that holds each one, is in
[`docs/rework/SPEC.md`](docs/rework/SPEC.md#invariants-to-test-explicitly).

## How code here is written

**Ports and adapters.** `internal/updater` talks to interfaces and touches no
network or filesystem. Every adapter is faked in tests. If you find yourself
adding an HTTP call to the engine, add a port instead.

**Fail closed.** New behavior chooses the outcome that does less. An unreadable
file, an unexpected service, a digest that cannot be proven: all of them stop the
Transaction. Prefer `INELIGIBLE` over acting on a guess.

**Absence over permission.** When something must not be possible, do not add a
check that could be misconfigured — arrange for there to be nothing to call.
This is why the MCP write tools are never registered rather than refused.

**Names are the ones in `CONTEXT.md`.** Transaction, Baseline, Candidate, Circuit
breaker, Proposal, Event, Notifier, Actor, Agent surface, Web UI. Do not
introduce a synonym.

**Comments explain why.** The code already says what it does. A comment earns its
place by recording a constraint, a decision, or a trap — not by narrating the
next line.

**Errors are lowercase and specific.** They are read by operators in a terminal
and by agents in an envelope. Say what was refused and, where useful, what would
fix it.

## Tests

Test names are sentences about behavior:

```go
func TestMonitorRefusesToBaselineWhenAnUpdateIsAlreadyPending(t *testing.T)
func TestAnUnhealthySiblingBlocksTheUpdateEntirely(t *testing.T)
```

Arrange, act, assert, with a blank line between the three. Table tests only where
the cases really are the same test with different data.

Fakes belong in the package's `*_test.go`, not in production code. The engine's
fake backend behaves like a real one — it deploys by replacing the document it
holds and re-reads running digests out of that document's pins — so the pinning,
drift, and rollback paths are exercised end to end.

Where a test proves an invariant or claims a behavior-inventory row, say so in a
comment above it.

## The behavior inventory

`docs/rework/SPEC.md` carries a row for every behavior the Python implementation
had, each claimed by a named Go test. It is fully claimed, and it stays that way:
if you change behavior a row describes, update the row and the test together.

## Pull requests

One thing per pull request. Say what changed and why in the body, name the
invariants or inventory rows involved, and confirm the fail-closed behavior still
holds. Regression tests are expected for anything touching update, timeout,
health, rollback, or breaker handling.

Do not commit a real policy, API key, token, state database, certificate, or
anything from a private host.
