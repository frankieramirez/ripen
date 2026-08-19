# Proposals

If a stack is deployed from Git, Ripen does not deploy it. It opens a pull
request that pins the digest, and stops. Merging and deploying stay where they
already are: your repository, your review, your workflow.

## Turning it on

Two things, both explicit:

```yaml
github:
  repository: frankieramirez/nas-infrastructure
  base_branch: main
  token_file: /run/secrets/github-token

stacks:
  blog:
    enabled: true
    backend: docker-compose
    file: /srv/blog/compose.yaml          # the deployed copy Ripen reads
    git_path: stacks/blog/compose.yaml    # the same file inside the repository
    auto_apply: true
    expected_services: [ghost]
    health: { target: http://127.0.0.1:2368/ }
```

`git_path` is what makes a stack a Proposal stack. Ripen never infers it — it
does not go looking for a `.git` directory, and a Compose stack without
`git_path` is edited in place. For Portainer, a stack the API reports as
Git-backed is *also* a Proposal stack; without `git_path` it is `INELIGIBLE`
rather than detached from Git by a direct update.

`git_path` must be a relative path to a YAML file inside the repository. Absolute
paths, `..`, and backslashes are configuration errors.

### The token

A fine-grained personal access token scoped to that one repository:

- **Metadata**: read
- **Contents**: read and write
- **Pull requests**: read and write

Nothing else. No administration, no workflows. Store it with mode `0600` — a
token file readable by group or others is refused at startup, because a token the
rest of the host can read is a token to treat as already leaked.

## What happens

When a Candidate matures on a Proposal stack, apply mode:

1. Checks the preconditions. The Circuit breaker must be closed, no Proposal may
   already be pending for that Service, the running digest must still be the
   Baseline, and every configured health check must pass.
2. Reads the repository's copy of the file on the base branch and compares it to
   the live reviewed document. If they differ, it stops: the repository is the
   source of truth for a Git-backed stack, and a difference is a human question,
   not something to paper over with another commit.
3. Creates the branch `ripen/<stack>-<service>-<first 12 of the digest>` — the
   same Service and digest always name the same branch, which is what makes the
   whole thing idempotent.
4. Writes the file with exactly one image pinned to `tag@sha256:…`.
5. Opens one pull request, titled `Pin <stack>/<service> to <short digest>`.
6. Records the Proposal — digest and URL — in the state database.

The result is `PROPOSED` and `updates_applied` stays `0`. Nothing was deployed.

Run it again and nothing happens twice: the branch is found, the content already
matches, the open pull request is reused, and the result reports
`created: false`. No second commit, no second PR.

## While a Proposal is open

**No second Proposal is opened for that Service, whatever the registry does
next.** A reviewer is looking at one change, not a queue that grows behind them.
If a newer digest appears, the Service reports `INELIGIBLE` with "a different
proposal is still pending review" and waits.

The Baseline does not move. Monitor runs keep observing.

## After the merge

Your workflow deploys, as it always did. The next Monitor run notices that the
running digest is no longer the Baseline and checks three things together:

- the live Compose document shows the digest pin;
- the running digest matches it;
- every configured health check passes.

All three, and the digest is accepted: result `UPDATED` with `updates_applied: 0`
(Ripen did not deploy it), the Proposal is cleared, and the audit trail records
it.

If health fails, the digest is **not** accepted, the result is `ERROR`, and the
Circuit breaker opens. Something merged that does not work, and Ripen stops
until a person looks.

## Clearing a stale Proposal

Sometimes a Proposal is closed unmerged, or superseded, or simply wrong. Clearing
it is an operator's decision and takes a reason:

```bash
ripen clear-proposal blog --reason "closed the PR; upstream pulled the release"
```

The next run may then propose again. Ripen never closes a pull request itself.

## Manual proposing

```bash
ripen propose blog
```

Same preconditions, same result, on demand rather than on a schedule. Agents can
call it as `create_proposal` — it is the one write in the MCP surface that
changes anything outside Ripen, and all it can do is open a pull request for a
human to read.
