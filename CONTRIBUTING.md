# Contributing

Ripen is maintained for its author's own use. Contributions are welcome and
reviewed on a best-effort basis — there is no service level here, and a pull
request may sit for a while.

There is no CLA and no DCO. Contributions are MIT, like the rest of the project.

## Issue first

Open an issue before writing code. Not for bureaucracy: Ripen has a small,
deliberate scope and a list of permanent [non-goals](ROADMAP.md#non-goals), and it
is better to find out in a paragraph than in a branch.

Good issues say what you were trying to do, what happened instead, and what your
setup looks like. If it is a bug, `ripen version` and the relevant `ripen status`
or `ripen audit` output usually answer half the questions.

Do not open a public issue for a suspected vulnerability. See
[`SECURITY.md`](SECURITY.md).

## Before you open a pull request

```bash
go test -race ./...
golangci-lint run ./...
gofmt -l .
```

All three clean. If you touched `internal/response`, regenerate the published
schemas with `RIPEN_UPDATE_SCHEMAS=1 go test ./internal/response/` — a test
compares them to what `ripen schema` emits.

[`AGENTS.md`](AGENTS.md) is the guide to how the code is laid out and written.
It is aimed at agents, but it is the same information a person needs.

## What review looks for

**Fail-closed behavior, still.** Ripen's value is that it refuses to guess. A
change that makes it act on less certainty will not be merged, however
convenient. When in doubt, do less.

**The invariants hold.** Ten of them, listed in `AGENTS.md`, each with a test. If
your change breaks one, the change is wrong.

**Regression tests.** Anything touching update, timeout, health, rollback, or
breaker handling needs a test that would have caught the bug. Test names are
sentences about behavior.

**The vocabulary.** `CONTEXT.md` defines the terms. Please use them and do not
introduce synonyms.

**One thing per pull request.** Easier to review, easier to revert.

## What not to include

Never commit a real policy file, Portainer API key, GitHub token, webhook URL,
state database, certificate, Compose environment secret, or anything else from a
private host. If you need to show configuration, use the shapes in
`config.example.yaml`.
