# Troubleshooting

Start here:

```bash
ripen status            # where everything stands
ripen explain <stack>   # why the next run would, or would not, act
ripen audit --limit 20  # what actually happened
```

`explain` is usually the fastest answer. It reads policy and state only — no
network — and lists the blockers in the order a run would hit them.

## Result codes

Each per-Service result in a run report is one of these.

| Result | What it means | What to do |
| --- | --- | --- |
| `baselined` | The Baseline was recorded. | Nothing. This is the first run doing its job. |
| `baseline_blocked` | An update was already pending, so the running digest could not be proven. | Deploy or roll back by hand until the stack is on a known digest, then run Monitor again. |
| `up_to_date` | Running the accepted digest. | Nothing. |
| `candidate` | A newer digest is under observation. | Wait. The detail says how many times it has been seen and how old it is. |
| `updated` | Applied and verified, or a merged Proposal was confirmed. | Nothing. |
| `proposed` | A pull request is open. | Review and merge it. |
| `rolled_back` | Verification failed; the Baseline was restored; the breaker is open. | Find out why health failed, fix it, then clear the breaker with a reason. |
| `rollback_failed` | The rollback did not verify either. | Attend to it now. The Service may be down. |
| `breaker_open` | The breaker is open, so nothing outbound happened. | Clear it once the cause is fixed. |
| `drifted` | Something changed between planning and applying, or outside Ripen. | Look at what changed. Ripen will re-baseline only what it can prove. |
| `ineligible` | The stack does not match the reviewed policy, or a precondition failed. | Read the detail; it says which. |
| `not_visible` | The policy names a stack the backend cannot see. | Check the automation user's access, or the stack name. |
| `engine_unavailable` | The backend could not be reached at all. | Fix the engine or the connection. No stack fault, no breaker. |
| `busy` | Another run holds the lease. | Wait, or find the other process. |
| `error` | Something unexpected. | Read the detail and the Event stream. |

## The breaker is open

```bash
ripen status | jq .data.breaker
```

The reason names the Service and what failed. Ripen will not apply anything or
open any Proposal while it is open; Monitor and reads carry on.

Once you have fixed the cause and confirmed the Service is healthy:

```bash
ripen clear-breaker --reason "restored the previous image by hand; jellyfin is serving"
```

The reason is mandatory and recorded. There is no button for this in the Web UI,
and no MCP tool: a breaker was opened because something needed a person, so a
person clears it.

## Nothing ever becomes mature

Maturity is two things: at least two observations of the same digest, and
`candidate_min_age_seconds` since the first one.

```bash
ripen candidates
```

If `observations` stays at 1, each run is seeing a *different* digest — a tag
being rebuilt frequently. If `mature` is false and `mature_at` is in the future,
it is simply waiting.

## Nothing is applied even though a Candidate is mature

`ripen explain <stack>` lists the reasons. The usual ones:

- the configured mode is `monitor`;
- `auto_apply` is not set on that stack or Service;
- the Circuit breaker is open;
- a Proposal is already pending review;
- the run's single update slot went to an earlier stack in the file.

## "services changed: expected […], found […]"

The live Compose service set no longer matches `expected_services`. Ripen fails
closed rather than acting on a stack it does not recognize. Review the change and
update the policy deliberately — that is the review step the exactness exists to
force.

## "a variable-interpolated image line cannot be pinned"

The Compose file's `image:` for that Service is built from variables, so there is
no literal reference to rewrite. Monitor mode works fine; apply does not. Write
the reference literally if you want Ripen to apply updates to it.

## The compose backend reports `engine_unavailable`

Ripen probes `<binary> compose version --format json` once per process. Check:

- the binary exists on `PATH` for the user running Ripen;
- it is Compose v2 (or `podman compose`), not the standalone `podman-compose`;
- the socket, if configured, is reachable by that user.

A configured socket that resolves to the privileged Docker socket refuses at
config load instead, with a message naming the path.

## The compose file is "not writable"

Writability is checked at observe time, for both the file and its directory,
because the rewrite is an atomic rename. A read-only bind mount is the usual
cause.

## Portainer says the key belongs to someone else

The run stops before touching anything. The API key is not the user
`expected_username` names — usually a rotated key, or one copied from another
instance. Generate a new key for the automation user.

## A Proposal is stuck

```bash
ripen status | jq '.data.services[] | select(.pending_proposal != null)'
```

While a Proposal is pending, that Service opens no others, whatever the registry
does. If the pull request was closed unmerged or is otherwise dead:

```bash
ripen clear-proposal <stack> --reason "closed unmerged; upstream pulled the release"
```

## The webhook is quiet

```bash
ripen notify test
ripen status | jq .data.notifier
```

`consecutive_failures` counts failed deliveries; `dropped_since_start` counts
Events this process could not even queue. Remember that paging is on state
changes: a stack that has been broken since yesterday pages once, not hourly.
Set `heartbeat_interval_seconds` if you want proof of life on a quiet system.

## Reading the Event stream

`ripen daemon` writes Events to stderr and nothing at all to stdout:

```bash
docker logs ripen 2>&1 | jq -c 'select(.event == "breaker.opened")'
ripen audit --run <run_id>
```

The audit trail is the durable record; the stream is a report of it. When they
disagree, believe the audit trail.

## Everything looks wrong after moving hosts

The state database is the system of record and does not travel with the policy.
A fresh database means a cold start: the first Monitor run re-baselines every
Service from whatever is running. That is safe, but it does mean anything running
at that moment becomes the accepted Baseline — so check what is running before
the first run on a new host.
