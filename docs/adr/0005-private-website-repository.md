# The official website has a private repository

Status: accepted. Supersedes [ADR 0004](0004-site-colocated-in-site.md).

The official website lives in private `frankieramirez/ripen-site`. People forking
Ripen inherit its application and operator documentation. Branding assets and
Cloudflare deployment configuration belong to the website repository.

`docs/` and `CONTEXT.md` stay canonical here. The website checks out an exact Ripen
commit and reads those files at build time. Published routes and GitHub edit links
stay unchanged. Site history was extracted; this repository's history was not
rewritten, so previously public website source remains available in old commits.

After Go CI completes, an upstream-only workflow reads GitHub metadata and
dispatches the private site's workflows. Successful main builds notify deployment
when docs changed. Docs PRs, including fork PRs, receive a `site/docs` commit
status from private validation. The notifier never executes PR code.

Private validation builds the PR's merge commit using trusted website code.
Status credentials live in separate jobs from the build. Contributors see fixed
pass/fail messages; maintainers read the private logs and relay useful details.
Forks skip the official notifier and need no website secrets to run Go CI.

The site owns deployment credentials and an hourly reconciliation workflow for
missed notifications. It records both source revisions in its deployment stamp,
serializes production updates, and refuses to deploy stale notifications. A failed
build leaves the currently serving site in place.

Adding an operator doc still requires a publication decision in the private
sidebar map. Coordinate that change with a website maintainer. The build fails
for an unmapped page, so the split does not silently publish new documentation.

See [website operations](../maintainers/website.md) for credential ownership and
the cutover procedure.
