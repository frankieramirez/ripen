# Research: does Cloudflare Pages fit ripen.dev?

- **Ticket:** [#99](https://github.com/frankieramirez/ripen/issues/99), part of the [ripen.dev map](https://github.com/frankieramirez/ripen/issues/98)
- **Date:** 2026-08-25
- **Method:** every claim below is checked against the vendor that owns it — `developers.cloudflare.com` (fetched as Markdown via each page's `index.md`, so the quotes are the published text and not a rendering of it), `docs.astro.build`, `docs.github.com`, the GitHub REST API, `hstspreload.org`'s API, and Namecheap's own knowledge base. No blog posts and no third-party write-ups. Where a fact could be observed rather than read — the GitHub App's permission set, `ripen.dev`'s current DNS, this repo's workflow triggers — it was observed, and the observation is shown.

## Answer

**The premise is half right, and the half that is wrong is the half the spec would have written down.**

Cloudflare does everything the map assumed: per-PR preview URLs, an Astro build from a subdirectory, a pinnable Node, apex on the zone apex with automatic TLS, and path-filtered builds. None of that is in doubt.

Two things are wrong.

**1. The product name is wrong.** Cloudflare's own documentation now opens the Pages landing page with a header that reads "Are you sure you want to use Pages?" and answers it: *"Workers supports most Pages use cases and offers a broader feature set. It is Cloudflare's primary platform for building applications. Start new projects with Workers."* ([Pages docs index](https://developers.cloudflare.com/pages/), page last updated 2026-08-25 — today). Astro's own deployment guide repeats it: *"Cloudflare recommends using Cloudflare Workers for new projects"* ([Deploy your Astro Site to Cloudflare](https://docs.astro.build/en/guides/deploy/cloudflare/)). Pages is not deprecated and no sunset date is published, but writing "Cloudflare Pages" into a spec for a site that does not exist yet is writing down the product its vendor tells you not to start on. **The spec should say Cloudflare Workers with Static Assets, built by Workers Builds.** Detail in §1.

**2. The security argument for Cloudflare is backwards.** The map chose Cloudflare partly "to keep site deploys off GitHub Actions and clear of `release.yaml`." The Cloudflare GitHub App's actual permission set, read from the GitHub API rather than from anyone's description of it, is:

```json
"permissions": {
  "administration": "write",
  "checks": "write",
  "contents": "write",
  "deployments": "write",
  "metadata": "read",
  "pull_requests": "write"
}
```

`release.yaml` in this repo triggers on `push: tags: v*`. `contents: write` is enough to push a tag. This repo has no rulesets and no branch protection on `main` (both verified). So installing the app does not keep anything clear of `release.yaml` — it grants a third party a standing, unexpiring credential that can reach it, where the GitHub Pages alternative would have needed a scoped, per-run `pages: write` + `id-token: write` in a new workflow file and could not have touched the release path at all. The mitigation is real but partial, and it is worth knowing before the spec repeats the original reasoning. Detail in §2.

Everything else holds. The recommendation is still Cloudflare — but for the reason that actually survives inspection (preview URLs per PR, which GitHub Pages genuinely cannot do), on the product Cloudflare actually recommends, with the permission grant written down honestly rather than described as a way of avoiding one.

---

## 1. Pages or Workers: which name goes in the spec

### What Cloudflare says today

The Pages documentation index carries a callout above everything else:

> Are you sure you want to use Pages?
>
> [Workers supports most Pages use cases and offers a broader feature set](https://developers.cloudflare.com/workers/static-assets/migration-guides/migrate-from-pages/). It is Cloudflare's primary platform for building applications. Start new projects with Workers.

That is the first prose on [`developers.cloudflare.com/pages/`](https://developers.cloudflare.com/pages/), and the page's own "Last updated" stamp reads 2026-08-25. Pages is still listed "Available on all plans"; the docs are still maintained; nothing announces an end of life. The posture is "supported, not recommended," not "deprecated."

The [migration guide](https://developers.cloudflare.com/workers/static-assets/migration-guides/migrate-from-pages/) (last updated 2026-08-14) is where the comparison lives, and it is maintained from the Workers side — the compatibility matrix is a Workers doc that describes Pages, not the reverse. Its framing:

> Like Pages, requests for static assets on Workers are free, and Pages Functions invocations are charged at the same rate as Workers, so you can expect a similar cost structure.
>
> Unlike Pages, Workers has a distinctly broader set of features available to it […]

### What Workers loses relative to Pages

The compatibility matrix is the honest place to look, because it is Cloudflare enumerating its own gaps. For a static Astro site the only rows that are worse on Workers are:

| Feature | Workers | Pages |
| --- | --- | --- |
| [Early Hints](https://developers.cloudflare.com/pages/configuration/early-hints/) | 🟡 workaround | ✅ |
| [Custom domains outside Cloudflare zones](https://developers.cloudflare.com/pages/configuration/custom-domains/#add-a-custom-cname-record) | ❌ | ✅ |
| [Branch Deploy Controls](https://developers.cloudflare.com/pages/configuration/branch-build-controls/) | 🟡 less configurable | ✅ |
| [Custom Branch Aliases](https://developers.cloudflare.com/pages/how-to/custom-branch-aliases/) | ⏳ coming soon | ✅ |
| [File-based Routing](https://developers.cloudflare.com/pages/functions/routing/) / [Pages Plugins](https://developers.cloudflare.com/pages/functions/plugins/) | 🟡 | ✅ |

None of them bites here. Early Hints is a performance nicety Workers can still do by sending `Link` headers with the zone setting on (matrix footnote 1). "Custom domains outside Cloudflare zones" is irrelevant precisely because the plan moves the zone *to* Cloudflare. File-based routing and Plugins are Pages Functions features, and a static site has no Functions. **Branch Deploy Controls is the one real loss**, and §2 explains what it costs.

Workers gains things Pages does not have, several of which matter for a small site run by one person: [Workers Logs](https://developers.cloudflare.com/workers/observability/), [Logpush](https://developers.cloudflare.com/workers/observability/logs/logpush/), source maps, gradual deployments, and the [Cloudflare Vite plugin](https://developers.cloudflare.com/workers/vite-plugin/) (✅ Workers, ❌ Pages) — the last of which matters because Astro is a Vite application.

### The static-only shape

A static Astro build needs no Worker script and no adapter. Astro's guide is explicit that the adapter is only for on-demand rendering: *"If your site uses on-demand rendering, install the [@astrojs/cloudflare adapter](https://docs.astro.build/en/guides/integrations-guide/cloudflare/)."* The whole configuration is a `wrangler.jsonc` with three keys:

```jsonc
{
	"name": "my-astro-app",
	"compatibility_date": "YYYY-MM-DD",
	"assets": {
		"directory": "./dist"
	}
}
```

Billing for that shape is nothing: *"Requests to static assets are free and unlimited"* and *"There is no additional cost for storing Assets"* ([Billing and Limitations](https://developers.cloudflare.com/workers/static-assets/billing-and-limitations/)). The free-tier request cap applies only to invocations of a Worker script, and a static site has none.

**Verdict on this bullet.** Pages works and is not going away tomorrow, but the spec should not name it. Say Workers Static Assets, deployed by Workers Builds. The migration guide exists in one direction only, and writing Pages now buys a migration later for no gain today.

---

## 2. Preview deploys, and what the GitHub App actually asks for

### Per-PR preview URLs: yes, on both products

**Pages** is the more explicitly documented of the two: *"Every time you open a new pull request on your GitHub repository, Cloudflare Pages will create a unique preview URL, which will stay updated as you continue to push new commits to the branch"* ([Preview deployments](https://developers.cloudflare.com/pages/configuration/preview-deployments/)). Each deployment gets a `<hash>.<project>.pages.dev` address, and each branch additionally gets a stable alias — *"Branch name aliases are lowercased and non-alphanumeric characters are replaced with a hyphen — for example, the `fix/api` branch creates the `fix-api.<project>.pages.dev` alias."*

**Workers** gets there by a different route. Every uploaded version has a preview URL of the form `<VERSION_PREFIX OR ALIAS>-<WORKER_NAME>.<SUBDOMAIN>.workers.dev`: *"Every time you create a new version of your Worker, a unique static version preview URL is generated automatically"* ([Preview URLs](https://developers.cloudflare.com/workers/configuration/previews/)). Workers Builds ties that to pull requests by way of *non-production branch builds*, which must be turned on:

> When you connect a git repository to Workers, commits made on the production git branch will produce a Workers Build. If you want to take advantage of preview URLs and pull request comments, you can additionally enable "non-production branch builds" in order to trigger a build on all branches of your repository.
> — [Build branches](https://developers.cloudflare.com/workers/ci-cd/builds/build-branches/)

With it on, the non-production deploy command *"defaults to `npx wrangler versions upload`, producing a preview URL"* ([Build configuration](https://developers.cloudflare.com/workers/ci-cd/builds/configuration/)), and the GitHub App comments it onto the PR:

> If a commit is on a pull request, Cloudflare will automatically post a comment on the pull request with the status of the build. […] A preview URL will be provided for any builds which perform `wrangler versions upload`.
> — [GitHub integration](https://developers.cloudflare.com/workers/ci-cd/builds/git-integration/github-integration/)

Two consequences worth writing into the spec rather than discovering later:

- **Workers builds every branch, not every PR.** "Non-production branch builds" is a repo-wide on/off switch. Pages' [Branch Deploy Controls](https://developers.cloudflare.com/pages/configuration/branch-build-controls/) can restrict which branches build; Workers "does not yet have the same level of configurability" (matrix footnote 4). Given how many stale `frankieramirez/*` branches this repo carries, the build-watch-path filter in §5 is doing double duty — it is not only a Go/site separation, it is the thing that stops every branch push from burning a build.
- **Preview URLs live on `workers.dev`, not on `ripen.dev`.** That is fine for review, and it means previews are never covered by the zone's Universal SSL certificate — they get Cloudflare's own. No action needed; just do not expect `preview.ripen.dev`.

### Fork PRs: no, and that is the correct behavior

Pages says so plainly: *"Preview URLs will not be created for pull requests from repository forks"* ([GitHub integration](https://developers.cloudflare.com/pages/configuration/git-integration/github-integration/)).

The Workers Builds documentation does **not** state this either way. The mechanism makes the same outcome nearly certain — the build is driven by an App installation on *this* repository, and a fork's head repository is a different repository — but I am not going to claim as a fact something the vendor has not written down. **Treat "no previews on fork PRs" as certain for Pages and expected-but-undocumented for Workers.**

This is a feature, not a gap. A fork PR that produced a deployment would let any drive-by contributor run arbitrary build code against this project's Cloudflare account. Ripen's own posture on exactly this shape is already on record: the Docker MCP registry's LLM security review is gated on `head.repo.full_name == github.repository` and so never runs on the fork PRs that are the only way external submissions arrive ([`docs/research/mcp-catalog-registry.md` §5](https://github.com/frankieramirez/ripen/blob/research/mcp-catalog-registry/docs/research/mcp-catalog-registry.md), on its own research branch). Same trade, and the fail-closed side is the right one.

### What the App actually asks for

Neither the [Pages](https://developers.cloudflare.com/pages/configuration/git-integration/github-integration/) nor the [Workers](https://developers.cloudflare.com/workers/ci-cd/builds/git-integration/github-integration/) integration page publishes a permission list. Both point at GitHub. So ask GitHub — `GET /apps/{app_slug}` is public for public apps:

```console
$ gh api /apps/cloudflare-workers-and-pages
```
```json
{
  "id": 85455,
  "slug": "cloudflare-workers-and-pages",
  "name": "Cloudflare Workers and Pages",
  "owner": { "login": "cloudflare" },
  "created_at": "2020-10-19T20:23:00Z",
  "updated_at": "2024-12-19T22:06:10Z",
  "permissions": {
    "administration": "write",
    "checks": "write",
    "contents": "write",
    "deployments": "write",
    "metadata": "read",
    "pull_requests": "write"
  },
  "events": ["pull_request", "push"]
}
```

That set has been stable since 2024-12-19. Against [GitHub's own definitions](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps):

| Permission | What it grants | Why Cloudflare wants it |
| --- | --- | --- |
| `metadata: read` | repository metadata | mandatory for every App |
| `contents: write` | create, update, delete files; push commits and tags | reading the repo to build it needs `read`; **`write` is for the "create a repo from a Cloudflare template" flow**, per the App's own description |
| `administration: write` | repository settings, collaborators, branch protection, and `DELETE /repos/{owner}/{repo}` | same template flow — creating the repository |
| `checks: write` | create/update check runs | the per-build check run |
| `deployments: write` | create deployments and statuses | the deployment record |
| `pull_requests: write` | comment on PRs | the preview-URL comment |

The App's description confirms the two heavy ones are there for a flow this project will never use: *"It can also create a new repository on your GitHub account when you get started with a Cloudflare template."*

**Three things follow.**

1. **`contents: write` reaches `release.yaml`.** Not by editing it — GitHub requires a separate `workflows` permission for that (*"If your app specifically needs to access or edit Actions files in the `.github/workflows` directory, request the 'Workflows' repository permission"*, [Choosing permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app)), and the App does not have it. But `release.yaml` triggers on `push: tags: v*`, and pushing a tag is `contents: write`. `gh api /repos/frankieramirez/ripen/rulesets` returns `[]` and `main` returns "Branch not protected," so nothing stands between that permission and a real release run. The map's phrase "clear of `release.yaml`" does not survive this.
2. **`administration: write` includes repository deletion.** For a template-creation flow this project will not use.
3. **The scoping control is repository selection, not permission selection.** GitHub Apps are accept-all-or-nothing on permissions; what you *can* limit is which repositories the installation covers, and Cloudflare says to: *"Cloudflare recommends that you limit the scope of the application to only the repositories you intend to build with Workers […] select **Only select repositories**."* Uninstalling is coarse — *"Removing access to the Cloudflare Workers and Pages app will revoke Cloudflare's access to all repositories from that GitHub account"* — though *"your previous deployments will continue to be hosted."*

**Verdict on this bullet.** Preview deploys work, per PR, on same-repo branches, on both products, with a PR comment carrying the URL. The App's grant is materially broader than the map assumed and is not narrowable. Scope the installation to `frankieramirez/ripen` alone, and write the grant into the spec as a cost rather than describing Cloudflare as the option that avoids one.

---

## 3. Build: Astro, from `site/`, on a pinned Node

### Astro out of the box

**On Pages**, Astro is a named framework preset with build command `npm run build` and build directory `dist` ([Build configuration](https://developers.cloudflare.com/pages/configuration/build-configuration/)). **On Workers Builds** there is no preset list; you set the commands yourself, and Astro's guide gives them: build `npx astro build`, deploy `npx wrangler deploy`, with `assets.directory` pointing at `./dist` ([Astro → Cloudflare](https://docs.astro.build/en/guides/deploy/cloudflare/)). Either way there is no adapter and no server.

### `site/` as the project root

Both products have it, and it is the same idea under two names.

Pages: *"The root directory is where your site's content lives. If not specified, Cloudflare assumes that your linked git repository is the root directory. The root directory needs to be specified in cases like monorepos, where there may be multiple projects in one repository."*

Workers: *"Specify the path to your project. The root directory defines where the build command will be run."* The [monorepo guide](https://developers.cloudflare.com/workers/ci-cd/builds/advanced-setups/) is explicit that this is where the Wrangler config lives: *"Set the root directory for each Worker to specify the location of its `wrangler.jsonc` and where build and deploy commands should run."* So: root directory `site/`, config at `site/wrangler.jsonc`, assets at `./dist` relative to it.

**One thing to verify at build time rather than assume.** The map's standing premise is that Astro globs the repository's root `docs/*.md` — which sits *outside* `site/`. The root directory sets the working directory, not the checkout; the whole repository is cloned, so the files are on disk. But Vite restricts filesystem access outside the project root by default, and neither Cloudflare nor Astro documents this combination. This is a build-configuration detail for whoever scaffolds the site, not a hosting question, and it is identical on Pages, on Workers, and on GitHub Pages — it does not affect the host decision. Flagging it so the spec does not silently depend on it.

### Node, and pinning it

| | Default Node | Override by env var | Override by file |
| --- | --- | --- | --- |
| [Pages build image v3](https://developers.cloudflare.com/pages/configuration/build-image/) | `22.16.0` | `NODE_VERSION` | `.nvmrc`, `.node-version` |
| [Workers Builds image](https://developers.cloudflare.com/workers/ci-cd/builds/build-image/) | `24.18.0` | `NODE_VERSION` | `.nvmrc`, `.node-version` |

Astro's requirement is *"Node.js - `v22.12.0` or higher. Odd-numbered versions like `v23` are not supported"* ([Install and set up Astro](https://docs.astro.build/en/install-and-setup/)). Both defaults clear it, and both are even-numbered.

Two traps:

- **Pages v3 dropped `package.json` → `engines`.** The v3 build system does not read it, and does not accept codenames like `lts/hydrogen`. Only the env var and the two dotfiles work. Workers Builds documents the same two files.
- **Cloudflare moves the default.** *"The default versions will be updated regularly to the latest minor version. No major version updates will be made without notice."* Pages sits on 22 and Workers on 24, so the "no major without notice" promise has already been exercised once across products. **Pin it.** A `.nvmrc` in `site/` costs one line and removes the failure mode where a site that has not been touched in three months stops building.

Workers Builds' [limits](https://developers.cloudflare.com/workers/ci-cd/builds/limits-and-pricing/) on the free plan: 3,000 build minutes per month, 1 concurrent build, 20-minute timeout, 2 vCPU, 8 GB memory. A static Astro build is a minute or two. The concurrency of 1 is the only one that will ever be felt, and only if several branches push at once.

**Verdict on this bullet.** Yes, out of the box, with `site/` as root directory, on Node 22 or 24 depending on product. Pin the Node version in `site/.nvmrc` and treat it as a fact of the spec rather than a preference.

---

## 4. Apex, the nameserver move, and TLS

### Apex works, and requires the zone

Pages: *"If you are deploying to an apex domain (for example, `example.com`), then you will need to add your site as a Cloudflare zone and configure your nameservers"* ([Custom domains](https://developers.cloudflare.com/pages/configuration/custom-domains/)).

Workers is the same and says it in one sentence: *"Custom Domains are routes to a domain or subdomain (such as `example.com` or `shop.example.com`) within a Cloudflare zone where the Worker is the origin"* — with `example.com`, the apex, given as the first example. And *"After you set up a Custom Domain for your Worker, Cloudflare will create DNS records and issue necessary certificates on your behalf"* ([Custom Domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/)).

There is no CNAME-flattening question to answer, because there is no CNAME. Cloudflare creates a record pointing directly at the Worker. One caution applies and does not bite here: *"You cannot create a Custom Domain on a hostname with an existing CNAME DNS record."* `ripen.dev` currently has an `A`, not a `CNAME` (below).

### What the nameserver move actually involves

Four steps, from Cloudflare's [full setup guide](https://developers.cloudflare.com/dns/zone-setups/full-setup/setup/) and [Add a site](https://developers.cloudflare.com/fundamentals/manage-domains/add-site/): add the domain and pick a plan; review the DNS records; change the nameservers at the registrar; finish SSL/TLS setup.

The record review is the step with a trap in it. Cloudflare runs a quick scan, and warns about it: *"Since the quick scan is not guaranteed to find all existing DNS records, you need to review your records,"* with the failure mode named — *"If you activate your domain on Cloudflare without setting up the correct DNS records for your domain, your visitors may experience DNS_PROBE_FINISHED_NXDOMAIN errors."*

For `ripen.dev` that review is nearly empty, which is the good case. Observed today:

```console
$ dig +short NS ripen.dev
dns1.registrar-servers.com.
dns2.registrar-servers.com.
$ dig +short A ripen.dev
162.255.119.218
$ dig +short DS ripen.dev
$
```

Namecheap parking nameservers, one parking `A` record, **no `DS` record — DNSSEC is not enabled.** That last one matters: Cloudflare's guidance is that DNSSEC must be disabled before a nameserver change or the domain becomes unreachable. It already is. There is no mail on the domain and nothing else to preserve, so the migration is: add zone, delete the parking record, point the apex at the Worker, change nameservers.

At the registrar, Namecheap's own instructions ([How to change DNS for a domain](https://www.namecheap.com/support/knowledgebase/article.aspx/767/10/how-to-change-dns-for-a-domain/)): Domain List → Manage → Nameservers → **CustomDNS** → enter Cloudflare's two assigned nameservers → save. No fee, no transfer, the registration stays at Namecheap. Namecheap's own propagation estimate is *"up to 24 hours (more, in rare cases)."*

### TLS: automatic, and `.dev` is genuinely preloaded

`.dev` preload status, from the list's own API rather than from anyone's summary of it:

```console
$ curl -s "https://hstspreload.org/api/v2/status?domain=dev"
{
  "name": "dev",
  "status": "preloaded",
  "bulk": false,
  "preloadedDomain": "dev"
}
```

The whole TLD is on the list, so every browser that ships the list will refuse plaintext to `ripen.dev` before a request leaves the machine. HTTPS is not a configuration choice here; it is a precondition for the site being reachable at all.

Cloudflare covers it with no manual step. [Universal SSL](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/enable-universal-ssl/): *"By default, Cloudflare issues — and renews — free, unshared, publicly trusted SSL certificates to all domains added to and activated on Cloudflare."* For a full setup, *"your domain should automatically receive its Universal SSL certificate within 15 minutes to 24 hours of domain activation,"* and the certificate *"will cover your zone apex (`example.com`) and all first-level subdomains."* So `ripen.dev` and `www.ripen.dev` are both covered by the one automatic certificate.

**The one real risk, and why it costs nothing here.** There is a window between activation and certificate issuance — up to 24 hours — and because `.dev` is preloaded, a visitor in that window gets a hard TLS failure rather than a plaintext page. Cloudflare's [minimize-downtime](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/enable-universal-ssl/#minimize-downtime) advice exists for live sites in exactly this position. `ripen.dev` serves a parking page today. **Do the cutover before there is anything to break, and the worst case is that a domain nobody visits is unreachable for a day.** If the order were reversed — build the site, then move DNS — that window would land on a live site.

**Verdict on this bullet.** Apex works on both products, the move is a four-step registrar change with no DNSSEC obstacle and essentially no records to preserve, and TLS is automatic with no manual step. Sequence the cutover early, while the downside is zero.

---

## 5. Path filtering

Both products have it, under the same name, with the same semantics: [Build watch paths](https://developers.cloudflare.com/workers/ci-cd/builds/build-watch-paths/) (Workers) and [Build watch paths](https://developers.cloudflare.com/pages/configuration/build-watch-paths/) (Pages). The compatibility matrix scores it ✅/✅.

Defaults are include `[*]`, exclude `[]` — build on everything. The wildcard *"will match zero or more characters,"* including path separators, so `docs/*` matches `docs/guides/setup.md` and not just `docs/README.md`. Evaluation order is documented and excludes win: paths matching an exclude are filtered out first, the survivors are tested against the includes, and *"a build triggers if any matching path is found; otherwise it's skipped."*

Two documented bypasses, where the filter is ignored and a build happens anyway:

- a push containing **0 file changes** (the escape hatch for forcing a build)
- a push containing **3,000+ file changes or 20+ commits**

Neither is reachable by normal work on this repo.

**What to include.** Not just `site/*`. The map's premise is that Astro globs the repository's root `docs/*.md` at build time, so a docs edit changes the published site and must rebuild it. The include list is therefore `site/*, docs/*`. Everything else — `cmd/`, `internal/`, `go.mod`, `.github/`, `flake.nix` — falls outside it, and a `main` push that only touches Go does not deploy. That is exactly the behavior the map asked for.

Two footnotes:

- Including `docs/*` means a regenerated `docs/schema/v1/` triggers a site rebuild it does not need. Harmless (a build minute out of 3,000), and cheaper than an exclude list that has to be kept in sync with what Astro globs. If it ever grates, exclude `docs/schema/*`, `docs/rework/*`, and `docs/adr/*` — the three the map already says must never be published.
- **Whether watch paths are relative to the repository root or to the configured root directory is not documented.** Pages' worked example (`project-a/*, packages/*` for a monorepo) reads as repository-root-relative, and the Workers monorepo guide pairs per-Worker root directories with per-Worker watch paths in a way that only makes sense if the paths are repo-relative. Almost certainly repo-root. Verify on the first build rather than assuming.
- On Workers, check runs follow the filter: *"when using build watch paths, only projects that trigger a build will generate a check run."* So a Go-only PR gets no Cloudflare check at all, rather than a green one. Worth knowing before someone adds it as a required status check.

**Verdict on this bullet.** Yes, on both products, and it is the same mechanism. `site/*, docs/*` as includes. This is also the control that keeps "build every non-production branch" (§2) from being expensive.

---

## 6. Analytics and CSP

### The claim, tested

The map asserts "no cookies, so no consent banner." The first half is documented in Cloudflare's own words, in a place strong enough to quote — the [RUM beacon](https://developers.cloudflare.com/speed/observatory/rum-beacon/) page, which is the script Web Analytics actually ships:

> The RUM beacon script does not store any data in the browser or access any storage data, such as cookies, localStorage, sessionStorage, IP address, or IndexedDB. The data we collect is performance data from the browser performance APIs. This performance data is ephemeral and only relates to the current webpage that is being viewed. […] This data is not stored or accessed from anywhere on the device, it is only available as in-memory data.

Corroborating, independently: the [Cloudflare cookies reference](https://developers.cloudflare.com/fundamentals/reference/policies-compliances/cloudflare-cookies/) enumerates every cookie Cloudflare sets — `__cflb`, `__cf_bm`, `__cfseq`, `cf_clearance`, `cf_ob_info`, `cf_use_ob`, `__cfwaitingroom`, `__cfruid`, `_cfuvid`, and three challenge cookies — and **not one of them is Web Analytics or the beacon.** A negative from the vendor's own exhaustive list is better evidence than an assurance.

And on IP: *"Although the RUM service receives the client/source IP address from the beacon as part of normal HTTP request handling process, it discards the IP address at the nearest Cloudflare data center and does not store it in core databases or logs."* Cloudflare's flat statement is *"Cloudflare Web Analytics does not collect or use your visitors' personal data"* ([About](https://developers.cloudflare.com/web-analytics/about/)).

The counting mechanism is the corroboration that matters most, because it is what a cookie would otherwise be for. Web Analytics reports [two counts](https://developers.cloudflare.com/web-analytics/data-metrics/high-level-metrics/): *"**Page views** - A successful HTTP response with a content-type of HTML"* and *"**Visits** - A page view that originated from a different website or direct link. Cloudflare checks where the HTTP referer does not match the hostname."* A visit is derived from the `Referer` header of the request in hand. **There is no unique-visitors metric at all** — which is precisely what you would expect from a system holding no persistent identifier, and is the honest cost of the design. `pageloadId` is generated fresh per page load and never persisted.

**On the consent conclusion, stated carefully.** The trigger in EU law is storing information on, or gaining access to information stored in, a user's terminal equipment (ePrivacy Directive Article 5(3)); a banner is the mechanism for obtaining consent to that. Cloudflare documents that the beacon does neither. So the specific thing a cookie banner exists to consent to is absent. That is a materially stronger position than "we use a privacy-friendly analytics vendor," and it is the position the map wanted. It is **not** a legal opinion, and GDPR obligations around any processing of personal data are a separate question from Article 5(3) — the honest framing for a footer line is *what the site does* ("this site sets no cookies and stores nothing in your browser"), which is verifiable, rather than *what the law requires*, which is not ours to assert. Note also that the `__cf_bm` bot cookie exists on proxied zones under Bot Fight Mode or Bot Management; leave both off and it is never set.

### What the tag actually is

Two ways to install it ([RUM beacon](https://developers.cloudflare.com/speed/observatory/rum-beacon/), [FAQs](https://developers.cloudflare.com/web-analytics/faq/)):

**Manual embed** — one script tag in the Astro layout:

```html
<script
	type="module"
	src="https://static.cloudflareinsights.com/beacon.min.js"
	data-cf-beacon='{"token": "$SITE_TOKEN"}'
></script>
```

**Automatic injection** — for proxied zones, Cloudflare inserts it at the edge as HTML passes through. Requires valid HTML in the response, and is blocked by a `Cache-Control: public, no-transform` header.

The choice changes the CSP. Both directives are documented verbatim:

| | `script-src` | `connect-src` | SRI |
| --- | --- | --- | --- |
| Manual embed | `https://static.cloudflareinsights.com/beacon.min.js` | `cloudflareinsights.com` | not possible |
| Automatic injection | same | `'self'` (reports to `/cdn-cgi/rum` on your own domain) | `integrity` added automatically |

Automatic injection is the better shape on both counts: one fewer cross-origin `connect-src`, and Cloudflare *"automatically includes an `integrity` attribute in the `<script>`."* On the manual path there is no SRI at all — *"there is no current way to safely apply an `integrity` attribute because we do not support version-pinning our beacon script."* For a project whose whole pitch is fail-closed supply-chain care, an unpinned, un-hashable third-party script in the page is a thing to notice, and automatic injection removes it.

The beacon sends the fields listed in the [RUM beacon table](https://developers.cloudflare.com/speed/observatory/rum-beacon/): `pageloadId`, `referrer`, `startTime`, `memory`, `timings`/`timingV2`, `resources`, `firstPaint`, FCP, LCP, CLS, TTFB, INP, `landingPath`. Reported dimensions are country, host, path, referer, device type, browser, OS, and navigation type ([Dimensions](https://developers.cloudflare.com/web-analytics/data-metrics/dimensions/)). Query strings are dropped: *"Cloudflare Web Analytics do not log query strings to avoid collecting potentially sensitive data."*

### Three things that will surprise someone later

1. **Ad blockers block it.** *"The analytics beacon is blocked by ad-blockers (including adblockplus, Brave, DuckDuckGo extension, etc)."* Ripen's audience is self-hosters and infrastructure people, i.e. the most heavily ad-blocked population on the internet. Expect the numbers to undercount badly. That is an argument for treating the analytics as a rough signal, not for choosing a different vendor — the alternative that is not blocked is the kind that this project has already declined to ship.
2. **The free plan may exclude EEA/EU traffic by default.** From the same RUM beacon page: *"Customers have the option to enable RUM globally or to limit its application to exclude users connecting to Cloudflare data centers in the EEA/EU. […] Free customers have RUM enabled automatically, with EU traffic excluded, and can switch it off if they prefer."* That text sits in the Speed/Observatory context and Cloudflare does not say whether the same default governs a manually embedded Web Analytics beacon on a free zone. **Unresolved.** It is a dashboard setting to check on day one, not a blocker — but "our analytics silently omit Europe" is a bad thing to learn from a chart.
3. **Sampling.** *"We retain unsampled beacon data for the past 7 days, after this point data is aggregated down to around 10%."* Fine for a low-traffic site; worth knowing before reading anything into a month-old figure.

**Verdict on this bullet.** The no-cookie claim holds and is documented in Cloudflare's own words, twice over, and the counting mechanism corroborates it. Prefer automatic injection for the SRI and the tighter `connect-src`. Write the footer line as a statement about the site's behavior, not about the law. Check the EEA/EU RUM setting before trusting a number.

---

## 7. The honest alternative: GitHub Pages

The map's stated trade was "one fewer vendor, but no preview deploys and apex via A records." Both halves are correct. The costs and the credits both turn out to be slightly different from the summary.

**Apex via A records, exactly as assumed.** GitHub publishes four IPv4 and four IPv6 addresses ([Managing a custom domain](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site)):

```
185.199.108.153   2606:50c0:8000::153
185.199.109.153   2606:50c0:8001::153
185.199.110.153   2606:50c0:8002::153
185.199.111.153   2606:50c0:8003::153
```

`ALIAS`/`ANAME` to `<user>.github.io` is supported where the DNS provider offers it. Namecheap's parking DNS is not the place to run this, so "one fewer vendor" is a little optimistic — the domain still needs a DNS provider willing to serve an apex, whether that stays Namecheap or moves. **Hardcoded IPs are the real long-term cost**: they are GitHub's to change, and the day they change, a `.dev` domain does not degrade, it goes dark.

**TLS is automatic but slower and manual-ish.** "Enforce HTTPS" is a checkbox, and *"It can take up to 24 hours before this option is available."* Comparable to Cloudflare's 15-minutes-to-24-hours, but it is a step someone has to remember to take, and on a preloaded TLD forgetting it means an unreachable site rather than an insecure one.

**No preview deploys. This is the whole decision.** GitHub Pages has one live site per repository. The Actions starter workflow is explicit that deployment is skipped on PRs — *"If the workflow was triggered by a push to the default branch, use the `actions/deploy-pages` action to deploy the artifact. This step is skipped if the workflow was triggered by a pull request"* ([Configuring a publishing source](https://docs.github.com/en/pages/getting-started-with-github-pages/configuring-a-publishing-source-for-your-github-pages-site)). A PR that changes the landing page can be reviewed as a diff or built locally. There is no URL to send anyone. For a project whose entire remaining design work is a landing page whose job is to convince a skeptical evaluator, this is the expensive loss.

**It does require GitHub Actions — and that is a smaller cost than the map assumed.** Astro needs a build, so branch-based publishing is out: *"If you want to use a build process other than Jekyll or you do not want a dedicated branch to hold your compiled static files, we recommend that you write a GitHub Actions workflow to publish your site."* The workflow needs *"a minimum of `pages: write` and `id-token: write` permissions"* plus `contents: read` ([Using custom workflows](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages)), and deploys through a `github-pages` environment that can carry branch protection rules.

Compare that honestly with §2. This repo already runs four workflows, every one of them least-privilege — `ci.yaml` and `release.yaml` both declare `permissions: contents: read` at the top and escalate per job. A `site.yaml` in the same style would be a fifth file of the same shape: token minted per run, expires with the run, scoped by the workflow, never able to push a tag, and never touching `release.yaml` — which is a separate file with a separate trigger that a `pages: write` token cannot reach. **The "keep it off Actions" premise had it backwards. A scoped Actions workflow is the narrower grant; the Cloudflare App is the broader one.** Path filtering would be `on: push: paths:` in that workflow, which this repo already knows how to write.

Everything else is fine. [Limits](https://docs.github.com/en/pages/getting-started-with-github-pages/github-pages-limits): 1 GB published site, a *soft* 100 GB/month bandwidth limit, and a *soft* 10 builds/hour that *"does not apply with custom GitHub Actions workflows."* The commercial-use prohibition — no online business, no e-commerce, no SaaS — does not touch a documentation site for an MIT tool.

**What GitHub Pages actually costs:** the preview URL, and a set of hardcoded IPs to keep an eye on. **What it actually saves:** a third-party App with `contents: write` and `administration: write` on this repository, and a second dashboard to remember the login for.

---

## 8. Consequences for the spec

1. **Write "Cloudflare Workers" — Static Assets, built by Workers Builds — not "Cloudflare Pages."** Cloudflare's own Pages index and Astro's own deploy guide both say to start new projects on Workers. Nothing a static Astro site needs is missing from Workers, and Pages costs a migration later. Update the map's Hosting premise ([#98](https://github.com/frankieramirez/ripen/issues/98)) accordingly.
2. **The premise otherwise holds.** Per-PR preview URLs with a comment on the PR, `site/` as build root, a pinnable Node, apex `ripen.dev` with automatic TLS, and `site/*, docs/*` build watch paths so a Go-only push does not redeploy. All six mechanisms exist and are documented.
3. **Rewrite the security rationale.** Do not say Cloudflare keeps deploys "clear of `release.yaml`." The App holds `contents: write` on this repository; `release.yaml` triggers on `push: tags: v*`; there are no rulesets and no branch protection. Say instead: Cloudflare is chosen for preview deploys, and it costs a standing third-party App grant of `administration/contents/checks/deployments/pull_requests: write`, scoped to this one repository, which is the price of the previews.
4. **Two settings that are not defaults.** Non-production branch builds must be turned **on** (no previews without it), and build watch paths must be set (Workers otherwise builds every branch of a repo with a lot of stale branches). Both belong in the spec as configuration, not as assumptions.
5. **Pin Node in `site/.nvmrc`.** Cloudflare moves the default and has already moved it across a major between products. One line, and it removes a class of "it built in March" failure.
6. **Prefer automatic beacon injection over the manual script tag.** It keeps `connect-src` at `'self'` and gets a real SRI hash, which the manual embed cannot have at all. The no-cookie claim is verified and can be stated as a fact about the site's behavior; do not state a legal conclusion. Check whether the free plan's EEA/EU RUM exclusion applies before reading anything into the numbers.
7. **Sequence the DNS cutover early, before the site is live.** `.dev` is genuinely preloaded (confirmed against the list itself), Universal SSL takes 15 minutes to 24 hours after activation, and in that window a preloaded domain is unreachable rather than merely insecure. Doing it now costs nothing; doing it after launch costs a visible outage. `ripen.dev` has no DNSSEC `DS` record and one parking `A` record, so there is nothing to migrate and nothing blocking the nameserver change.
8. **Three things to verify at build time, none of which change the host decision.** Whether Astro can glob root `docs/*.md` from a project root of `site/` under Vite's filesystem restrictions; whether build watch paths are repository-root-relative (near-certain) or root-directory-relative; and whether Workers Builds declines fork PRs the way Pages documents that it does.
