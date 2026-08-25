# ripen.dev

The design document for Ripen's website. Everything here was decided on the
[ripen.dev map](https://github.com/frankieramirez/ripen/issues/98); each section
names the ticket that holds its reasoning, or the premise on the map it
inherits from where the decision was the charting session's own. Building the
site is execution against this document, not re-deciding it.

The seeded design is on a
[Claude Design canvas](https://claude.ai/code/artifact/c93329f8-d89e-4c1d-9ce7-6b82359ed339):
the landing in dark and light at 1440 and 390, plus a themed docs page in both
themes.

## Shape

Settled by the charting session's own premises, recorded in the Notes on
[the map](https://github.com/frankieramirez/ripen/issues/98) rather than in a
ticket of their own.

Two surfaces, built in that order:

- **`/`** — a fully custom landing page. This is where the design budget goes.
- **`/docs`** — Starlight, themed with the palette and type below, its layout
  otherwise not fought. Content is globbed from the root `docs/` directory,
  which stays canonical; see [ADR 0004](../docs/adr/0004-site-colocated-in-site.md).

The site lives in this directory, in this repo. There is no launch date: this
is a permanent home, designed so it could carry a launch later — which is why
the OG image and the above-the-fold pitch are specified precisely rather than
deferred.

**Audience.** The cold self-hoster and the serious evaluator, the evaluator
weighted heaviest. The docs serve the operator looking up a policy field.
Agents are not an audience: `CONTEXT.md` scopes the Agent surface to the JSON
CLI and the MCP server, and the site does not blur that line — no `llms.txt`,
no agent-targeted pages. Publishing `docs/agents.md` is not a counter-example.
It is a page *about* the machine-facing surface, written for the operator
wiring one up, and the site presents it exactly as GitHub does; what the site
declines to do is become that surface itself.

## Stack and hosting

Decided on
[Re-decide the host](https://github.com/frankieramirez/ripen/issues/106) and
[the hosting research](https://github.com/frankieramirez/ripen/issues/99).

- **Astro**, static output, no adapter. Starlight for `/docs`, custom pages
  for `/`. Pin the Node version with `.node-version` — Cloudflare has moved
  Node majors across products before, and `package.json` engines is not read.
  Pinned to major `24`, newest patch, matching how `ci.yaml` treats Go: the
  major is the thing Cloudflare moves, and a full pin only rots. Scaffolded
  on Astro 7 and Starlight 0.41 in
  [Scaffold Astro in `site/` and path-filter CI](https://github.com/frankieramirez/ripen/issues/111);
  `npm run build`, `dev`, `preview` and `check` are the scripts.
- **Cloudflare Workers Static Assets**, deployed from GitHub Actions by
  `wrangler deploy`, authenticated by a Cloudflare API token stored as a repo
  secret and scoped to Workers on this account. Not the Cloudflare GitHub App
  (standing `administration:write` + `contents:write` on a repo whose
  `release.yaml` fires on tag push), not Workers Builds, not Pages (Cloudflare
  steers new projects off it). The workflow's GitHub token needs no write
  permissions.
- **Deploy on `main` only.** No preview deploys in v1. If a shareable preview
  is ever wanted, a PR-triggered `wrangler versions upload` job adds per-deploy
  preview URLs — add it when the lack is felt.
- **DNS.** `ripen.dev` sits on a live Cloudflare zone (nameservers moved from
  Namecheap 2026-08-25, while the domain served nothing). `.dev` is
  HSTS-preloaded: HTTPS is mandatory, and a certificate gap is an outage, not
  a downgrade. Use a Workers Custom Domain on the apex so Cloudflare manages
  DNS records and certificates.
- **CI.** `ci.yaml` filters so site edits do not fire the Go matrix,
  `govulncheck`, the cross-compile, or the Nix build. The site build runs on
  PRs touching `site/**`, `docs/**`, or the published root markdown files
  (`CONTEXT.md`), so a docs edit that breaks the site shows as a visible
  failing check. Heavy assets stay out of git — the module zip ships to every
  `go install`.

  Not `on.pull_request.paths`: a workflow filtered out at the trigger never
  reports its checks, so a required check sits pending forever, where a job
  skipped by an `if:` reports success. A `changes` job computes the two
  booleans in `.github/ci/changed-halves.sh` and every job is gated on them.
  The Go half is skipped only when a pull request touches nothing outside
  `site/`; anything the script is unsure of runs everything. `docs/` does not
  skip the Go jobs, because a test asserts `ripen schema` and
  `docs/schema/v1/` are identical.

## Analytics

**Cloudflare Web Analytics**, automatic injection (not the manual script tag:
injection keeps `connect-src 'self'` and gets a real SRI hash; the manual
embed cannot have SRI at all). No cookies, no browser storage — confirmed
against primary sources on
[the hosting research](https://github.com/frankieramirez/ripen/issues/99) —
so no consent banner; a one-line privacy note in the footer instead. Known
limits, accepted: the beacon is blocked by ad blockers (Ripen's audience is
close to the worst case), and there is no unique-visitors metric.

## Landing page

Content inventory and order decided on
[Landing content inventory](https://github.com/frankieramirez/ripen/issues/101),
amended by [the canvas seeding](https://github.com/frankieramirez/ripen/issues/105).
Seven sections, top to bottom. There is deliberately no separate features
section — the Transaction walkthrough is the feature list.

1. **Hero.** "A digest ripens. You apply." at largest type. Sub-headline:
   *"Tags move. Ripen watches what `:latest` actually points at, waits for a
   new image to prove itself, and updates one service — only where you said it
   may."* The artifact is a static terminal block showing **two commands**:
   `ripen status` with the amber `"mature":true` token, then `ripen explain`
   with the blockers array — it is ripe, and it still will not act until you
   say so. (This supersedes the single-block wording in the inventory and the
   visual brief.) Designed against today's JSON envelope; `--pretty`
   ([#107](https://github.com/frankieramirez/ripen/issues/107)) now exists, so
   swapping the block is a one-line edit here. Primary CTA: the monitor-mode
   run command, the zero-risk action. GitHub is a quiet secondary.
2. **The warning.** "Ripen recreates containers. Start in monitor mode." Just
   below the fold, with the install block. It does real work: it makes monitor
   mode the obvious first step, which is what the CTA wants anyway.
3. **Install.** One primary path — the Compose service block, ~12 lines, with
   `read_only`, `cap_drop`, and `no-new-privileges` visible, because those
   lines *are* the security argument. `go install`, Nix, and the signed
   archive collapse behind a toggle. Copy buttons on install commands only.
4. **How a Transaction works.** Observe → Baseline → Ripen → Apply → Roll
   back, explained in full. The page's centre of gravity, and the answer to
   "how is this different from Watchtower" without naming Watchtower.
5. **What it will not do.** The non-goals, with the one-maintainer line among
   them, framed as scope discipline. Placed *before* the comparison on
   purpose: a reader who has just been told what Ripen refuses to do reads
   Watchtower's socket mount as a choice, not a flaw.
6. **Comparison.** Watchtower and Diun only, per
   [the comparison research](https://github.com/frankieramirez/ripen/issues/100):
   - The Watchtower column heads the live fork, `nicholas-fedor/watchtower`
     (`containrrr/watchtower` was archived 2025-12-17). The table never says
     "unmaintained".
   - The fork ships `--cooldown-delay`, so the table cannot claim the maturity
     window as unique. Ripen's row states the real difference: Ripen clocks
     its own first sighting and requires a second, Watchtower trusts the
     registry-reported build time.
   - The rows Ripen honestly loses stay in (time to first useful run,
     notification targets, platforms watched, watching images you don't run,
     non-Compose orchestrators). No red cells for rows Diun deliberately does
     not play.
   - The table dates itself and prefers architecture rows over
     missing-feature rows, because absences in a weekly-shipping fork have a
     shelf life.
   - Renovate and Dependabot get one sentence as a different category, not
     columns.
7. **Footer.** One row, small type: the privacy note, GitHub, docs, license,
   security policy, and a changelog link to GitHub Releases.

**Cut from v1:** the Web UI section. No screenshot exists and there is no
`docs/webui.md` to link to. Easy to add once that page exists.

**Render-found implementation notes** (already applied on the canvas): the
warning box stacks vertically at phone width; the two-command hero makes the
page taller than the inventory implied, so the terminal block gets generous
vertical room. The comparison table and terminal scroll inside their own
containers on mobile.

## Visual direction

Decided on [the visual direction brief](https://github.com/frankieramirez/ripen/issues/102).
One line: **well-set evidence** — a quiet, warm-dark page in the product's own
monospace voice, where color appears exactly once: when something is ripe.

**Voice.** The README and `ROADMAP.md` are the house voice. Copy is set, not
sold: no exclamation marks, no "blazingly", no signup verbs. The CTA
vocabulary is `ripen run --mode monitor`.

**Palette.** Color appears only when something is ready for you; everything
else is warm neutral. Unripe states stay neutral — no green counterpart, no
traffic lights. Dark is native, light is derived (neutral off-white, not
cream). Both themes designed from the start, system default plus a toggle,
AA contrast or better for body text in both.

| Token | Dark (native) | Light (derived) |
| --- | --- | --- |
| `ground` | `#171310` | `#FAFAF8` |
| `surface` | `#201B16` | `#FFFFFF` |
| `ink` | `#E8E3DC` | `#1C1814` |
| `muted` | `#8A8178` | `#6E675F` |
| `border` | `#332C25` | `#E2DED8` |
| `ripe` | `#E09520` | `#B36F0A` |

**Typography.** IBM Plex Mono for display, terminal, labels, nav, and buttons
— the largest type on the page is the mono. Source Serif 4 for body. Hero:
mono, medium, `clamp(2.5rem, 7vw, 4.5rem)`, tight leading. Body: serif,
~17px/1.7. Labels: mono, small, tracked out, `muted`.

**The terminal treatment.** No chrome — a bare block, 1px `border`, on
`surface`; no fake title bar or traffic lights. Neutrals-only syntax: keys
`muted`, values `ink`, amber on exactly one token, `"mature":true`. One
orchestrated load (~1s, plays once, never loops; `prefers-reduced-motion`
gets the static version). No copy button on the hero — it is evidence, not a
snippet.

**Wordmark and glyph.** Lowercase `ripen` in IBM Plex Mono. Glyph: two
circles side by side, left hollow (stroke, `muted`), right filled (`ripe`) —
observed→mature. Doubles as the favicon. No fruit illustration, ever.

**The floor (must not look like).** Gradient-mesh heroes; three-feature-card
grids with icons; purple/indigo accents; screenshots in floating browser
chrome; sparkle/rocket/lightning iconography; testimonial or logo walls;
looping animation; "Get started free" register. Also the AI-default clusters:
warm-cream + terracotta editorial, and near-black + acid-green hacker landing.

## OG image

Ingredients fixed by [the visual direction brief](https://github.com/frankieramirez/ripen/issues/102);
the composition itself was fog on the map until this spec, and graduated in
[Write the site spec and ADR 0004](https://github.com/frankieramirez/ripen/issues/104).
Done right the first time, because there is no second launch of a link
preview:

- 1200×630, static, one image for both themes: dark `ground` (`#171310`) —
  link previews render on arbitrary surfaces, and the dark card carries the
  brand regardless of the reader's theme.
- The glyph (hollow `muted` circle, filled `ripe` circle) top-left, small.
- "A digest ripens. You apply." set large in IBM Plex Mono, `ink`, left-
  aligned, the amber appearing nowhere except the glyph's filled circle.
- One quiet line beneath in `muted` mono: `ripen.dev` — no sub-headline, no
  feature list; a preview card is read in under a second.
- No terminal screenshot, no JSON: at card size it renders as noise.

Same composition, square-cropped, for any surface that wants 1:1.

## Docs pipeline

Decided on [Docs pipeline shape](https://github.com/frankieramirez/ripen/issues/103).

- **Page set.** Glob `docs/*.md` — never `docs/**` — with an explicit exclude
  list, today exactly `release-credentials.md`. Discovery is automatic —
  a new `docs/*.md` file is in the collection without anyone opting it in —
  but publishing it takes one line in the sidebar map below, and until that
  line exists the build fails rather than publishing the page unordered or
  dropping it silently. The exclude list documents what is deliberately not
  published. `CONTEXT.md` is
  added explicitly as a **Vocabulary** page. `ROADMAP.md` stays a GitHub
  link.
- **Frontmatter: none.** Root docs stay byte-identical for GitHub readers.
  Title from each page's H1; description and order in a sidebar map in
  `site/` config. A globbed page missing from that map fails the build.
- **Sidebar.** Flat, Vocabulary first, then the README documentation-table
  order: Configuration, Portainer, Compose, Agents, Proposals, Notifications,
  Architecture, Troubleshooting. No groups.
- **Links.** Canonical files keep their repo-relative links. At build time,
  mechanically by collection membership: a link targeting a published page
  rewrites to its site route; anything else rewrites to its absolute
  `github.com/frankieramirez/ripen/blob/main/` URL. No hand-maintained list.
  A link validator runs in `astro build`, so a broken internal link fails the
  build.
- **Code blocks.** Starlight's Expressive Code defaults. Fix missing
  `yaml`/`json` language tags in the source docs; no per-block customization
  in v1.

## Deferred, on purpose

Not decisions, so not ticketed: each of these is fog or deferred work carried
on [the map](https://github.com/frankieramirez/ripen/issues/98), and none of
it blocks the build.

- **Docs versioning** — irrelevant until there is a v1.1 to version against.
- **The Web UI section and `docs/webui.md`** — the landing section returns
  once the doc and a screenshot exist.
- **Preview deploys** — see hosting above.
- **The hero terminal block** — still the JSON envelope; `--pretty` shipped
  in [#107](https://github.com/frankieramirez/ripen/issues/107), so swapping
  the block is a one-line edit when the canvas is next touched.
