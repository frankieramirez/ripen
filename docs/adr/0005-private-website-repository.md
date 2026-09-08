# The official website has a private repository

Status: accepted. Supersedes [ADR 0004](0004-site-colocated-in-site.md).

The official website lives in private `frankieramirez/ripen-site`. People forking
Ripen inherit its application and operator documentation. Branding assets and
Cloudflare deployment configuration belong to the website repository.

`docs/` and `CONTEXT.md` stay canonical here. The website checks out an exact Ripen
commit and reads those files at build time. Published routes and GitHub edit links
stay unchanged. Site history was extracted; this repository's history was not
rewritten, so previously public website source remains available in old commits.

An upstream-only workflow uses GitHub's push path filter to notify the private
site when docs change on main. Website checks gate publication independently
of Go CI. After PR CI completes, the notifier reads GitHub metadata to request
private validation; docs PRs, including fork PRs, receive a `site/docs` commit
status. The notifier never executes PR code.

Private validation builds the PR's merge commit using trusted website code.
Status credentials live in separate jobs from the build. Contributors see fixed
pass/fail messages; maintainers read the private logs and relay useful details.
Forks skip the official notifier and need no website secrets to run Go CI.

The site owns deployment credentials. Its production job checks out current main
from both repositories after acquiring the deployment lock, records the actual
source revisions, then builds and publishes. Delayed notifications therefore
build current content. Failed builds leave the currently serving site in place;
failed notifications and checks use manual retries. Recovery uses Cloudflare's
existing version rollback.

Adding an operator doc still requires a publication decision in the private
sidebar map. Coordinate that change with a website maintainer. The build fails
for an unmapped page, so the split does not silently publish new documentation.

See [website operations](../maintainers/website.md) for credential ownership and
the cutover procedure.
