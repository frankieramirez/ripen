# The agent surface

Ripen's machine-facing interface is the CLI and one MCP server. They are the same
surface: every MCP tool maps to a CLI verb with the same guard, the same
parameters, and the same answer.

Neither can apply an update or clear the Circuit breaker from MCP. Those tools do
not exist — not disabled, not permissioned, absent.

> This page is about agents *operating* Ripen. Agents working on Ripen's own
> source are a different audience, served by [`AGENTS.md`](../AGENTS.md).

## Everything answers one envelope

There is no `--json` flag. The default, and the only output an agent should
consume, is one JSON object on stdout, success or failure:

```json
{
  "schema_version": 1,
  "command": "status",
  "occurred_at": "2026-08-19T09:14:22Z",
  "ok": true,
  "data": { }
}
```

A failure is the same envelope with `ok: false` and a typed error:

```json
{
  "schema_version": 1,
  "command": "propose",
  "occurred_at": "2026-08-19T09:14:22Z",
  "ok": false,
  "error": { "code": "precondition_failed", "message": "…", "retryable": false }
}
```

Failures also print one human-readable line on stderr. Machines can ignore
stderr entirely; people usually want it.

The four read verbs — `status`, `candidates`, `audit`, `explain` — accept
`--pretty`, which renders the same payload as text. It is never inferred from a
TTY: an agent running in a pty still gets the envelope unless it asked.
`--pretty` is not registered on writes, `daemon`, or `mcp`.

### Error codes

| Code | Retryable | Means |
| --- | --- | --- |
| `usage` | no | The command or its flags are wrong. |
| `config_invalid` | no | The policy would not load. |
| `not_found` | no | No such stack, or nothing to clear. |
| `precondition_failed` | no | Something must change before this can work. |
| `breaker_open` | no | The Circuit breaker is open. A person has to clear it. |
| `state_locked` | yes | Another run holds the lease. |
| `backend_unavailable` | yes | The backend could not be reached. |
| `registry_unavailable` | yes | The registry could not be reached. |
| `internal` | no | Something Ripen could not classify. |

Ignore codes you do not recognize; the set can grow without a version bump.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Operational failure. |
| `2` | Configuration or usage. |
| `3` | **A human needs to look at this.** |

Read `3` narrowly: the Circuit breaker is open, or a rollback failed. Nothing
else uses it, so it is worth alerting on.

## The verbs

### Reads

| Verb | Answers |
| --- | --- |
| `ripen status [--pretty]` | Every configured Service with its Baseline, Candidate, pending Proposal, and last result, plus the breaker, the lease, Notifier health, versions, and the effective policy. |
| `ripen candidates [--pretty]` | Every Candidate under observation and whether it has matured. |
| `ripen audit [--pretty]` | The audit trail, newest first. |
| `ripen explain [--pretty] <stack>` | Why the next run would, or would not, act on this stack. |

`status` is driven by the policy, not by what happens to be in the database: a
Service that has never been observed still appears, with `baseline: null`.

`audit` reads the attempts table — the record of what Ripen *did* — never the
Event stream, which is a notification channel. It pages:

```bash
ripen audit --limit 50
ripen audit --limit 50 --cursor 412        # from a previous next_cursor
ripen audit --run 01920e2f-…               # one run
ripen audit --stack media --result rolled_back
```

`explain` answers offline, from policy and state alone. No backend call, no
registry call, no network. It lists the blockers in the order a run would hit
them:

```json
{"blockers": ["the configured mode is monitor", "the candidate matures at 2026-08-20T09:14:22Z"]}
```

### Writes

| Verb | Does |
| --- | --- |
| `ripen run --mode monitor` | One observation cycle. Never mutates a Stack. |
| `ripen run --mode apply` | One Transaction that may change one Service. |
| `ripen propose <stack>` | Open the Proposal for a matured Candidate. |
| `ripen clear-proposal <stack> --reason "…"` | Drop a reviewed, stale Proposal. |
| `ripen clear-breaker --reason "…"` | Close the Circuit breaker. |

Both `clear-` verbs require a reason, and it is recorded. An operator saying what
they fixed is the point of the gate.

### Process verbs

| Verb | Does |
| --- | --- |
| `ripen daemon [--once]` | Run on the configured interval. Writes nothing to stdout. |
| `ripen mcp [--enable-writes]` | Serve MCP over stdio. |
| `ripen notify test` | Send a real `notifier.test` through the real webhook path. |
| `ripen schema` | The JSON Schema for every response. |
| `ripen version` | Build metadata and every schema version. |

## Identity on the wire

Every read carries `backend`, `stack`, and `service`, with `service` null for a
stack-level policy. The state store's internal key never appears in JSON.

`actor` — `cli`, `daemon`, or `mcp` — is recorded on every write and every Event.
It is determined by the surface that ran the code. There is no parameter for it,
and no caller can declare its own.

Every run has a UUIDv7 `run_id`, persisted on attempts, echoed in `status`, and
stamped on Events. It is how you tie an alert to what actually happened:

```bash
ripen audit --run "$(ripen run --mode monitor | jq -r .data.run_id)"
```

## Schemas

`ripen schema` emits a JSON Schema per command, and the same documents are
checked into [`docs/schema/v1/`](schema/v1) and shipped in every release archive.
A test asserts the two are identical, so the published schema cannot drift from
what the binary emits.

Receivers should ignore unknown fields and unknown result codes. Adding either
is not a breaking change and does not bump `schema_version`.

## MCP

```bash
ripen mcp                    # four read tools
ripen mcp --enable-writes    # plus three write tools
```

Stdio, tools only. Nothing but protocol frames is ever written to stdout.

| Tool | CLI equivalent |
| --- | --- |
| `status` | `ripen status` |
| `candidates` | `ripen candidates` |
| `audit` | `ripen audit` |
| `explain` | `ripen explain <stack>` |
| `run_monitor_cycle` | `ripen run --mode monitor` (writes only) |
| `create_proposal` | `ripen propose <stack>` (writes only) |
| `clear_proposal` | `ripen clear-proposal <stack> --reason …` (writes only) |

Results carry the identical Response envelope as `structuredContent`. A failure
is `isError: true` with the failure envelope attached, never a JSON-RPC protocol
error, so a caller can see what went wrong and correct itself.

**Read-only mode is structural.** Without `--enable-writes` the write tools are
never registered and the write path is never built: the process loads no
credentials and opens no clients. Apply mode and `clear_breaker` have no tool in
either mode.

### Container packaging

Read-only, which is what the Docker MCP Catalog entry ships:

```yaml
services:
  ripen-mcp:
    image: ghcr.io/frankieramirez/ripen:latest
    command: ["mcp"]
    stdin_open: true
    volumes:
      - ./policy.yaml:/config/policy.yaml:ro
      - ./data:/data:ro
```

Writes enabled, which is a deliberate choice for your own infrastructure:

```yaml
    command: ["mcp", "--enable-writes"]
    volumes:
      - ./policy.yaml:/config/policy.yaml:ro
      - ./data:/data
```

The difference is one flag and one `:ro`.
