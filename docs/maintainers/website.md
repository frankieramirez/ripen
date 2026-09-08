# Website operations

The official site is built in private `frankieramirez/ripen-site`. Its README
documents local development, deployment controls, and rollback. Operator docs
remain in this repository.

## Notifications and PR checks

`Site notify` uses GitHub's native push path filter to publish changes to
`docs/**` or `CONTEXT.md` on main. PR validation runs after `Go CI`, using only
GitHub metadata and trusted main code. The exact repository guard makes it skip
forks. Publication depends on the website build, so a Go CI failure does not
hold up a documentation correction.

`SITE_WORKFLOW_TOKEN` is a fine-grained PAT scoped to `ripen-site` with Actions:
write. Save it as an Actions secret here. The site repository holds
`RIPEN_STATUS_TOKEN`, scoped to this repository with Commit statuses: write.
Record each token's expiry and rotate it under the same secret name.

Docs PRs receive `site/docs`. The status is informational; it was not added to a
branch protection rule during the split. Failed dispatches report an error.
Retry failed or interrupted checks through the private validation workflow;
content failures need a fix. Detailed build logs remain private.

If a docs PR introduces a new page, ask a website maintainer to update its sidebar
map. A new file fails website validation until its publication is decided.

## Deployment ownership

The existing Cloudflare Worker, routes, and hostnames are unchanged. Cloudflare
credentials belong only in `ripen-site` after cutover. Its `SITE_DEPLOY_ENABLED`
variable controls automatic deployments. Retry failed notifications or deployments
through GitHub Actions.

Before merging the extraction PR, follow the pre-merge cutover procedure in the
private site's [MIGRATION.md](https://github.com/frankieramirez/ripen-site/blob/main/MIGRATION.md).

After the split, verify both existing hostnames, `https://ripen.dev` and
`https://ripen-site.cloudflare-punctual727.workers.dev`, serve the root page,
`/docs/configuration/`, and the custom 404 page. Verify that `/_deploy.txt` on
both hosts reports the same checked-out site and source revisions as the
deployment.

For recovery, pause the active deployment owner first and restore a known-good
Worker version through Cloudflare's rollback command or dashboard. The pre-split
fallback is Ripen commit `49f3ee72294f61212bdaa0b25e60aff8007fda03`; it contains the
original site and deployment workflow. Do not run both owners at once.
