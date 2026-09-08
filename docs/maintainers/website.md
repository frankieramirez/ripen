# Website operations

The official site is built in private `frankieramirez/ripen-site`. Its README
documents local development, deployment controls, and rollback. Operator docs
remain in this repository.

## Notifications and PR checks

`Site notify` runs after `Go CI`. It checks GitHub metadata using trusted main
code and dispatches a site build for changed `docs/**` or `CONTEXT.md`. The exact
repository guard makes it skip forks.

`SITE_WORKFLOW_TOKEN` is a fine-grained PAT scoped to `ripen-site` with Actions:
write. Save it as an Actions secret here. The site repository holds
`RIPEN_STATUS_TOKEN`, scoped to this repository with Commit statuses: write.
Record each token's expiry and rotate it under the same secret name.

Docs PRs receive `site/docs`. The status is informational; it was not added to a
branch protection rule during the split. Failed dispatches report an error and
hourly private reconciliation retries missing or stalled checks. Content failures
need a fix or a maintainer's manual retry. Detailed build logs remain private.

If a docs PR introduces a new page, ask a website maintainer to update its sidebar
map. A new file fails website validation until its publication is decided.

## Deployment ownership

The existing Cloudflare Worker, routes, and hostnames are unchanged. Cloudflare
credentials belong only in `ripen-site` after cutover. Its `SITE_DEPLOY_ENABLED`
variable controls automatic deployments; `SITE_CHECKS_ENABLED` enables hourly
PR reconciliation.

During migration, the old deployment job recognizes `SITE_DEPLOY_OWNER=private`
in this repository. Set that variable and drain old deployment jobs before
enabling the new deployment. After verifying the new serving revision pair,
remove `site/`, the old deployment jobs, and both Cloudflare secrets here.

For recovery, pause the active deployment owner first. The private repository
supports rebuilding a known-good pair of site and Ripen SHAs. The pre-split
fallback is Ripen commit `49f3ee72294f61212bdaa0b25e60aff8007fda03`; it contains the
original site and deployment workflow. Do not run both owners at once.
