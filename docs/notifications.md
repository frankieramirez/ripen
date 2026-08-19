# Notifications

Ripen has one Event stream and two places it can go. The stream, on stderr, is
always on and records everything. The webhook Notifier is optional, filtered, and
pages on state changes.

Configuring nothing is silent-but-logging, not silent.

## The Event stream

Every Event is one line of JSON on stderr:

```json
{"schema_version":1,"event":"breaker.opened","occurred_at":"2026-08-19T09:14:22Z",
 "run_id":"01920e2f-…","backend":"docker-compose","stack":"media","service":"web",
 "actor":"daemon","data":{"reason":"media/web: the functional health check timed out"}}
```

`stack` and `service` are null when an Event is not about one. `actor` is the
surface that emitted it — `cli`, `daemon`, or `mcp` — and is never something a
caller can set.

`ripen daemon` writes nothing to stdout, so a container's stdout is empty and its
stderr is a clean stream of Events.

### The payload cannot carry a secret

Event payloads are one closed set of fields. There is no free-form map, so there
is nowhere for a token to end up, and a test walks the payload type to keep it
that way. This replaces the older approach of scrubbing values by matching key
names at emit time, which only works if you think of every name.

## The catalogue

Seventeen Events. Every one may be named in `notifier.webhook.events`.

| Event | When |
| --- | --- |
| `run.finished` | A cycle finished, with its counts. |
| `run.failed` | A cycle could not finish. |
| `baseline.recorded` | A Service's Baseline was written. |
| `baseline.blocked` | A Baseline could not be proven, so none was written. |
| `candidate.observed` | A Candidate digest was seen. |
| `candidate.matured` | A Candidate passed the waiting window. |
| `transaction.started` | An apply began. |
| `transaction.succeeded` | An apply was verified and accepted. |
| `transaction.rolled_back` | An apply failed and the Baseline was restored. |
| `transaction.rollback_failed` | The rollback itself did not verify. |
| `breaker.opened` | The Circuit breaker opened, with the reason. |
| `breaker.cleared` | An operator cleared it, with their reason. |
| `proposal.created` | A pull request was opened or found. |
| `proposal.deployed` | A merged Proposal was confirmed running and healthy. |
| `proposal.cleared` | An operator dropped a stale Proposal. |
| `stack.error` | Something went wrong with one stack. |
| `stack.recovered` | A Service came back after a failure. |

Two more exist outside the catalogue. `notifier.test` is what `ripen notify test`
sends, and it is never filtered — the point is to exercise the real path.
`notifier.delivery_failed` never leaves the stream: a Notifier cannot page about
its own inability to page.

## The webhook

```yaml
notifier:
  heartbeat_interval_seconds: 86400
  webhook:
    url_file: /run/secrets/webhook-url
    token_file: /run/secrets/webhook-token
    timeout_seconds: 10
    events:
      - breaker.opened
      - transaction.rolled_back
      - stack.error
      - stack.recovered
```

Omit `events` and you get the paging set: `run.failed`,
`transaction.succeeded`, `transaction.rolled_back`, `transaction.rollback_failed`,
`breaker.opened`, `breaker.cleared`, `proposal.created`, `stack.error`,
`stack.recovered`. A name that is not in the catalogue is a startup error — a
typo that silently pages about nothing is worse than a refusal to start.

Each delivery is a `POST` of the Event envelope, with
`Authorization: Bearer …` when a token file is configured. The URL must be https
unless it points at this host.

### Delivery is at-most-once and fail-open

A run must never wait on a webhook, and must never fail because of one:

- Deliveries go through a bounded queue on their own goroutine. If the queue
  fills, Events are dropped and counted, and the run carries on.
- One attempt plus two retries with short backoff. A 4xx is not retried: the
  destination understood and refused.
- Failures are reported on the stream as `notifier.delivery_failed`, with the
  attempt count — and without the destination URL.

`ripen status` carries the health that survives a restart: `last_success_at`,
`consecutive_failures`, and this process's `dropped_since_start`.

### Paging is on changes

Ripen pages when something becomes true, not for as long as it stays true. A
stack that has been broken for a week pages once.

Suppression is keyed on `(event, stack, service)` and remembers the state that
was last delivered. Recovery re-arms the failure: `stack.recovered` clears the
suppression for `stack.error`, and `breaker.cleared` clears it for
`breaker.opened`. Break again and you hear about it again.

Point Ripen at a different destination and suppression resets, because the new
destination knows nothing. It gets told the current state once, which is right.

Only the hash of the destination is stored, never the URL.

### The heartbeat

`heartbeat_interval_seconds` lets one otherwise-suppressed `run.finished`
through when nothing has been delivered for that long. Without it, a Notifier
that quietly died and a system where nothing is happening look identical.

## Testing it

```bash
ripen notify test
```

Sends a real `notifier.test` through the real path — same queue, same retries,
same webhook — and reports whether it landed, plus the persisted health. Nothing
about it is a special case except that it is never filtered or suppressed.

## The invariant behind all of it

Every paging Event corresponds to a durable state change that was **already
written**. The state database is the system of record; the stream is a report.
That ordering means a Notifier can never tell you something `ripen status`
cannot confirm — including when the process dies between the two.
