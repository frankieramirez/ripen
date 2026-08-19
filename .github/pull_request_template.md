## What this changes

<!-- What it does, and why. Name the invariant or behavior-inventory row if one
     is involved. -->

## How it was verified

<!-- `go test -race ./...`, `golangci-lint run ./...`, plus whatever you did by
     hand. -->

- [ ] **Ripen still fails closed.** This change does not make it act on less
      certainty than before, and anything touching update, timeout, health,
      rollback, or breaker handling has a regression test.
