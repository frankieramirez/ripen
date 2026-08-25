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
  `npm run build`, `dev`, `preview` and `check` are the scripts. `check` was
  declared there but its dependency was not installed, so it errored on first
  use; `@astrojs/check` and `typescript` are devDependencies now.

  Starlight was registered in
  [Wire the docs pipeline](https://github.com/frankieramirez/ripen/issues/118),
  which also moved `astro.config.mjs` to `astro.config.ts`: the config imports
  the sidebar map, because the sidebar and the docs collection have to agree
  about which pages exist and the way to guarantee that is to build both from
  one list.

  Routes today: `/`, the landing; `/design`, the specimen page that renders the
  design system and computes its own contrast table; the nine docs pages under
  `/docs/`; and `/docs` itself, a redirect to the first sidebar entry so the
  bare path is not a 404. `/design` is linked from nowhere and is where the
  review checkpoint can look at the system without the landing page competing
  for attention.

  Registering Starlight also brought three things nobody specified, all of them
  defaults and all of them now live: a themed **404 page** for the whole site,
  a **sitemap** (`@astrojs/sitemap`, which Starlight registers itself, listing
  `/`, `/design/` and the nine docs routes), and **Pagefind search**. Pagefind
  indexes only what Starlight marks as a page body, so the index is the nine
  docs pages and not the landing or `/design`. Whether each of them stays, and
  what `robots.txt` should say about `/design` and `_deploy.txt`, is the small
  print still carried as fog on
  [the map](https://github.com/frankieramirez/ripen/issues/109).
- **Cloudflare Workers Static Assets**, deployed from GitHub Actions by
  `wrangler deploy`, authenticated by a Cloudflare API token stored as a repo
  secret and scoped to Workers on this account. Not the Cloudflare GitHub App
  (standing `administration:write` + `contents:write` on a repo whose
  `release.yaml` fires on tag push), not Workers Builds, not Pages (Cloudflare
  steers new projects off it). The workflow's GitHub token needs no write
  permissions.

  The Worker is `ripen-site`, assets-only: `site/wrangler.jsonc` declares no
  `main`, so nothing runs in the request path and the build directory is what
  the edge serves. It also declares `workers_dev` rather than leaving it to
  default, because Cloudflare re-asserts routing state on every deploy — a
  route toggled in the dashboard and left out of the config comes back on the
  next merge. `wrangler` is a devDependency, so the version that deploys is
  the one in `package-lock.json` and not whatever `npx` resolves that morning.

  Two secrets, not one. `CLOUDFLARE_ACCOUNT_ID` is needed because the token
  carries a single permission row and cannot enumerate accounts, so `wrangler`
  has no way to discover which account to deploy to.
- **Deploy on `main` only.** The `deploy` job in `ci.yaml` runs on push to
  `main`, gated on the `site` build passing, so a broken build never reaches
  the edge and the two failures stay distinguishable. It stamps the commit
  sha into `dist/_deploy.txt` after the build, then reads that path back off
  the served URL until it matches *and* the root returns 200 — both in the
  same pass, retried, because `wrangler` returns when the upload is accepted
  and the edge can then serve two paths out of step for several seconds. A
  200 alone would not be a proof: this Worker has served *something* since it
  was provisioned. Serialised by a `concurrency`
  group, queueing rather than cancelling, so two quick merges cannot leave the
  live site older than `main`. No preview deploys in v1. If a shareable
  preview is ever wanted, a PR-triggered `wrangler versions upload` job adds
  per-deploy preview URLs — add it when the lack is felt.
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
   may."* The artifact is a static terminal block showing **two commands** —
   it is ripe, and it still will not act until you say so. (This supersedes
   the single-block wording in the inventory and the visual brief.) Primary
   CTA: the monitor-mode run command, the zero-risk action. GitHub is a quiet
   secondary.

   The two commands are `ripen candidates --pretty` and
   `ripen explain media --pretty`, and **the block is real output**, produced
   by building the binary, seeding a state database with a baseline and two
   sightings of a second digest, and running the two commands against it.
   Nothing is trimmed, re-indented or invented; the digests are full 64-character
   digests, because that is what Ripen prints. Two departures from the earlier
   wording, both settled on
   [Landing: hero, warning, install](https://github.com/frankieramirez/ripen/issues/115):

   - **`--pretty`, not the JSON envelope.** `ripen status` prints its envelope
     as a **single unindented line**. The pretty-printed JSON in the earlier
     draft was therefore never something Ripen prints — reproducing it honestly
     needs `| jq`, which puts a second tool in the story the hero is telling.
     `--pretty` ([#107](https://github.com/frankieramirez/ripen/issues/107))
     shipped, and this is the swap that was anticipated.
   - **`candidates`, not `status`.** `ripen status --pretty` is 31 lines for a
     single service, most of it effective policy and version metadata, and its
     candidate block repeats what `explain` shows immediately below.
     `candidates` answers exactly the question the headline asks, in 8 lines,
     and carries the amber `mature: true`. Trimming `status` to fit was the
     other option and was rejected: on a page whose argument is evidence, an
     abbreviated payload is a mocked-up one.
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
   [the comparison research](https://github.com/frankieramirez/ripen/issues/100).
   **The rows, their wording, the footnotes and the dateline are approved in
   [Approve the comparison table rows](https://github.com/frankieramirez/ripen/issues/117)
   and are built from there without interpretation.** The constraints that
   produced them, which still bind any later edit:
   - The Watchtower column heads the live fork, `nicholas-fedor/watchtower`
     (`containrrr/watchtower` was archived 2025-12-17). The table never says
     "unmaintained".
   - The fork ships `--cooldown-delay`, so the table cannot claim the maturity
     window as unique. Ripen's row states the real difference: Ripen clocks
     its own first sighting and requires a second, Watchtower trusts the
     registry-reported build time.
   - The rows Ripen honestly loses stay in. **Departure, decided on #117:**
     the five losses are carried by four rows, not five — `non-Compose
     orchestrators` folded into `platforms watched`, because they are the same
     fact stated twice. No fact was dropped. No red cells for rows Diun
     deliberately does not play; no marks of any kind, since amber means ripe
     and only ripe, so there is no green and no red to render a tick in.
   - The table dates itself and prefers architecture rows over
     missing-feature rows, because absences in a weekly-shipping fork have a
     shelf life.
   - Renovate and Dependabot get one sentence as a different category, not
     columns.
   - **Three silences, on purpose.** The word "unmaintained" appears nowhere,
     about anything. The table never counts maintainers, because Ripen and
     Diun both have one and section 5 already says so about Ripen. "Diun's
     docs mount the socket read-write" is stated only as the documentation
     gap it is, never as a security claim.
   - Cells are one short clause; the load-bearing qualifiers live in eight
     numbered footnotes under the table, so nothing accurate is lost and
     nothing competes for the same eye.
7. **Footer.** One row, small type: the privacy note, GitHub, docs, license,
   security policy, and a changelog link to GitHub Releases.

**Cut from v1:** the Web UI section. No screenshot exists and there is no
`docs/webui.md` to link to. Easy to add once that page exists.

**Render-found implementation notes** (already applied on the canvas): the
warning box stacks vertically at phone width; the two-command hero makes the
page taller than the inventory implied, so the terminal block gets generous
vertical room. The comparison table and terminal scroll inside their own
containers on mobile.

Found in the browser while building sections 1–3, and applied:

- **The primary CTA sits above the terminal, not below it.** The real block
  runs 32 lines, which puts anything after it well past the fold at 1440×900.
  The zero-risk command is the one element that cannot be down there. The
  inventory fixes what the hero contains, not the order it is stacked in.
- **The warning carries no amber.** Amber means ripe and means only that; a
  warning painted in the page's one accent would teach the reader the wrong
  thing about every amber token they meet afterwards. It takes its weight from
  a `muted` left rule and the type instead.
- **The hero terminal reveals in five beats over about a second, switched on
  by a script**, not by the stylesheet alone. The beats start visible; only a
  browser that has run that line hides them in order to animate them in.
  Content that is invisible until an animation fires is content a failed
  animation deletes.
- **Copy buttons sit in the caption row, above the block, never floating over
  it.** A wide snippet scrolls sideways inside its own container, and an
  overlaid button eats the end of whatever line the reader has scrolled to.

Found in the browser while building sections 4–7, and applied:

- **The scrolling comparison table has to say that it scrolls.** Below about
  55rem the three columns no longer fit and the container scrolls, which is
  what the inventory asked for — but a phone reader sees a Ripen-only table
  whose rows have unexplained gaps under them, because the row heights are set
  by cells that are still off-screen. It reads as a broken table rather than a
  scrollable one. A muted `Scroll the table sideways →` line above it, shown
  only under 55rem, is the whole fix.
- **The step numbers in section 4 sit with their names, not out in the
  gutter.** Hung on the far side of the sequence rule they lost the name they
  belonged to and picked up the rule instead, so `01` read as a stray mark. The
  rule alone carries the sequence; the numbers are set at `--step-small` beside
  the step name.
- **Section 6 is headed `Ripen, Watchtower, Diun`.** The inventory names the
  section "Comparison", which is a label for a spec and not a heading for a
  reader. Naming all three is also the fair version: the page is not headed by
  its own verdict.
- **The footer links to `/docs/`**, which does not exist until
  [Wire the docs pipeline](https://github.com/frankieramirez/ripen/issues/118)
  lands. It is a dead link on the workers.dev URL until then, and it is the
  right final state, so it ships now rather than being pointed at GitHub and
  changed back.

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
| `muted` | `#948B82` | `#6E675F` |
| `border` | `#332C25` | `#E2DED8` |
| `ripe` | `#E09520` | `#9F630A` |

Two of these moved when
[Design system: tokens, themes, and type](https://github.com/frankieramirez/ripen/issues/113)
measured them. Dark `muted` was `#8A8178`, which is 4.47 against `surface` —
under AA for the small tracked-out labels it exists to set. Light `ripe` was
`#B36F0A`, which is 3.87 against `ground`, and `ripe`'s one job is the amber
token in the terminal block, which is small text. Both were failing exactly
where the token is actually used, so both were darkened or lightened to the
nearest value that clears 4.5 with margin. The visible cost is that light
`ripe` reads closer to ochre than to amber; the dark theme, which is the
native one, is unchanged.

**Measured contrast**, computed from the values above and rendered on
`/design` at build time. AA is 4.5 for normal text, AAA is 7. Nothing on the
site claims the 3.0 large-text allowance.

| Pair | Dark | Light |
| --- | --- | --- |
| `ink` on `ground` | 14.47 (AAA) | 16.88 (AAA) |
| `ink` on `surface` | 13.38 (AAA) | 17.65 (AAA) |
| `muted` on `ground` | 5.52 (AA) | 5.33 (AA) |
| `muted` on `surface` | 5.10 (AA) | 5.57 (AA) |
| `ripe` on `ground` | 7.47 (AAA) | 4.71 (AA) |
| `ripe` on `surface` | 6.90 (AA) | 4.93 (AA) |

The `/design` page computes this table from the token values rather than
repeating them, so a palette edit that breaks a ratio shows up there rather
than quietly making this document wrong.

**Typography.** IBM Plex Mono for display, terminal, labels, nav, and buttons
— the largest type on the page is the mono. Source Serif 4 for body. Hero:
mono, medium, `clamp(1.75rem, 8.5vw, 4.5rem)`, tight leading. Body: serif,
~17px/1.7. Labels: mono, small, tracked out, `muted`.

The hero clamp was `clamp(2.5rem, 7vw, 4.5rem)` until the headline was set in
a browser. Every character in a monospace is 0.6em wide, so "A digest ripens."
at the old 2.5rem floor is 384px — against the 342px a 390px phone leaves
between the frame's gutters. It did not overflow the heading, it overflowed
the document, and took the theme toggle and the whole page off the right edge
with it. A proportional face would have absorbed this; a monospace display
face cannot, which makes the floor a real constraint rather than a taste
setting.

Self-hosted from `site/public/fonts/`, four files, **89.0 KB total**:

| File | Size | |
| --- | --- | --- |
| `ibm-plex-mono-400-latin.woff2` | 9.8 KB | static |
| `ibm-plex-mono-500-latin.woff2` | 9.8 KB | static |
| `source-serif-4-latin.woff2` | 49.7 KB | variable, `wght` 200–900 |
| `source-serif-4-italic-latin.woff2` | 19.7 KB | static 400 |

Both families are OFL 1.1; the licences are committed beside them.

Three decisions inside that number. **Subsetted by unicode range (latin), not
by observed glyphs** — the docs pipeline globs arbitrary markdown, so a subset
fitted to today's copy would start dropping characters the first time someone
writes a word it had not seen. **The upright serif is the variable file**,
which covers body and bold out of one download; asking Google for 400 and 600
separately returns the same file twice. **The italic is the static 400 cut**,
not the variable italic, which costs 51 KB against 20 KB — the extra 31 KB
buys correct weights for bold italic, which prose barely uses and which
`site/` ships to every `go install`. Bold italic is synthesised.

The tracked tree went from 1.08 MB to 1.23 MB, of which 89 KB is the fonts.
That is comfortable, so build-time fetching stays off the table.

**The terminal treatment.** No chrome — a bare block, 1px `border`, on
`surface`; no fake title bar or traffic lights. Neutrals-only syntax: keys
`muted`, values `ink`, amber on exactly one token, `"mature":true`. One
orchestrated load (~1s, plays once, never loops; `prefers-reduced-motion`
gets the static version). No copy button on the hero — it is evidence, not a
snippet.

**Wordmark and glyph.** Lowercase `ripen` in IBM Plex Mono. Glyph: two
circles side by side, left hollow (stroke, `muted`), right filled (`ripe`) —
observed→mature. Doubles as the favicon. No fruit illustration, ever.

Drawn on a 32-unit grid: the hollow circle is `r=6` under a 2-unit stroke and
the filled one is a bare `r=7`, so both come out 14 units across — a stroke
straddles its path, and matching the *outer* diameters is what makes the pair
look like one size. Centres 17 apart. In the wordmark the glyph is set to 62%
of the font size, which lands it on the mono's x-height; aligning to cap
height leaves the circles floating above a word with no ascenders. The word
is live text, not outlines — the mono is already preloaded, so setting it
costs nothing and keeps it selectable. The OG card (#119) is the one place
that has to draw it instead.

The icons are `favicon.svg` (light and dark cuts in one file, via
`prefers-color-scheme`), a 32px PNG fallback in the light cut for anything
that will not take an SVG icon, and a 180px `apple-touch-icon.png` that is
opaque on `ground`, because iOS composites a transparent icon onto whatever
it likes. 3.5 KB for all three.

**At 16px the glyph holds** — the hollow circle keeps a visible hole and the
pair does not collapse into two dots, in either cut. The icon does carry a
2.5-unit stroke against the inline glyph's 2: at 16px the whole box is 16px,
so a 2-unit stroke is a 1px hairline and the observed circle goes faint
beside a solid disc. 3 units starts rounding the ring into a square. That is
an optical adjustment at icon size, not a redraw. What a square icon cannot
fix is that a mark this wide fills the width and leaves the top and bottom
thirds empty, so it reads a size smaller than a square icon would.

One trap, learned the hard way: SVG is parsed as XML, where `--` inside a
comment is a syntax error that makes the whole file fail to render. An HTML
page will parse it anyway, so an inline copy looks fine while the shipped
icon is broken. Test icon files as files.

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

Same composition, laid out square, for any surface that wants 1:1.

Built in [OG image and meta tags](https://github.com/frankieramirez/ripen/issues/119).

- **Generated, then committed**, by `npm run og` — `site/scripts/og.mjs`. Not
  a build step: `astro build` runs in CI on a runner with no browser, and a
  card that regenerates on every deploy is a binary that changes when nothing
  about it did. The renderer is headless Chrome over the DevTools protocol,
  driven by Node's own global `WebSocket` with no dependency, and it is
  deliberately the same engine that renders the site — the card is set in the
  real subsetted `woff2` with the browser's own hinting, not by a second text
  shaper that would set the same string slightly differently.

  The script reads the six dark values out of `tokens.css` and the two circles
  out of `Glyph.astro` rather than repeating either. One geometry: the card
  cannot end up carrying a near-miss of the site's mark, because it is not
  drawing a second one.

- **The break is the landing's own** — "A digest ripens." / "You apply." The
  card and the hero are the same sentence, and a preview that re-breaks it
  reads as a different one. Sixteen characters at 104px in a face that is
  0.6em wide is 998px, against 1040px between the gutters.

- **The 1:1 is laid out, not cropped.** A centre crop of the wide card cuts a
  998px headline down to 630px of room and takes the verb off the end of it.
  Same gutter, same sizes, same order, same alignment; the one thing that
  changes is that the glyph is not pinned to the top edge, because held there
  against a square it leaves 700px of nothing in the middle and the card reads
  as a mistake. One centred stack instead of two edges.

- **`ripen.dev` is set at 38 card-pixels, not the 30 it was drawn at.** Found
  by looking at it at the size it is read: a Slack unfurl is about 360 CSS px
  wide, where 30 card-pixels is 9, which is past quiet and into unreadable.
  38 is the smallest that survives that scale.

- **File sizes.** 34.5 KB wide, 38.2 KB square; tracked tree 1.35 → 1.42 MB,
  which is the same comfortable as the fonts were. X crops
  `summary_large_image` to 2:1, which takes 15px off the top and bottom of a
  1200×630; nothing on the card is within 80px of an edge.

- **One `og:image`, and it is the wide one.** Declaring both would let a
  crawler choose, and some choose by area — which would send the square to a
  wide card and letterbox it. `og-square.png` is shipped for hand-use, not
  advertised in the head.

### Meta tags

- The landing writes the whole set in `BaseLayout.astro`: `og:type`,
  `og:site_name`, `og:title`, `og:description`, `og:url`, `og:locale`, the
  image with its type, dimensions and alt, `twitter:card` and
  `twitter:image`. Starlight already writes everything but the image from each
  page's own frontmatter, so the docs get the image tags and nothing else,
  through `head` in `astro.config.ts`.

- **No `twitter:title` or `twitter:description`.** X reads the `og:` pair when
  the `twitter:` one is absent, and a second copy of the same two strings is a
  second place for them to fall out of step.

- **No `theme-color`.** It can only answer `prefers-color-scheme`, and this
  site has a toggle that outranks the system — so the tag would be right in
  two of the three theme states and wrong in the third, which is worse than
  colouring nothing.

- The image URL is absolute against `site`, so on the workers.dev deploy it
  names `ripen.dev`, which is still dark. That is the same choice the
  canonical link already makes and it is the right one: the preview a reader
  shares has to name the site, not the address it was built for. The
  consequence is that no third-party card debugger can render this until the
  apex is attached — the card was checked here instead, against the real
  image at 200, 360, 400 and 504 CSS px, on a light and a dark surface, and
  with X's 2:1 crop applied.

## Docs pipeline

Decided on [Docs pipeline shape](https://github.com/frankieramirez/ripen/issues/103),
built in [Wire the docs pipeline](https://github.com/frankieramirez/ripen/issues/118).

- **Page set.** Glob `docs/*.md` — never `docs/**` — with an explicit exclude
  list, today exactly `release-credentials.md`. Discovery is automatic —
  a new `docs/*.md` file is in the collection without anyone opting it in —
  but publishing it takes one line in the sidebar map below, and until that
  line exists the build fails rather than publishing the page unordered or
  dropping it silently. The exclude list documents what is deliberately not
  published. `CONTEXT.md` is
  added explicitly as a **Vocabulary** page. `ROADMAP.md` stays a GitHub
  link.

  The sidebar map is `site/src/docs-map.ts`, one entry per page: the canonical
  file, the route, the label, the description, and a title override where the
  H1 is wrong for a site page — which is only Vocabulary, whose H1 is the
  product name. It is enforced in both directions. A globbed file missing from
  the map fails the build; so does a map entry whose file has been renamed or
  deleted, which would otherwise leave a sidebar link to a 404.

- **Frontmatter: none.** Root docs stay byte-identical for GitHub readers.
  Title from each page's H1; description and order in a sidebar map in
  `site/` config. A globbed page missing from that map fails the build.

  The collection is Astro's own glob loader pointed at the repository root,
  wrapped to supply the frontmatter the files do not carry. The wrap
  intercepts one call — `parseData`, which the glob loader makes between
  reading a file's frontmatter and validating it against the schema — and
  leaves parsing, rendering and watching to the stock loader. The leading H1
  is stripped on the way to the page, because Starlight sets the title itself
  from that same heading and the page would otherwise carry it twice.

- **Sidebar.** Flat, Vocabulary first, then the README documentation-table
  order: Configuration, Portainer, Compose, Agents, Proposals, Notifications,
  Architecture, Troubleshooting. No groups.
- **Links.** Canonical files keep their repo-relative links. At build time,
  mechanically by collection membership: a link targeting a published page
  rewrites to its site route; anything else rewrites to its absolute
  `github.com/frankieramirez/ripen/blob/main/` URL — `tree/main/` where the
  target is a directory, which is what `docs/schema/v1` is. No
  hand-maintained list.

  **A broken link fails the build, and not through the plugin the spec named.**
  `starlight-links-validator` keys its data by each page's path under
  `src/content/docs/`, and these pages are the root `docs/` files read where
  ADR 0004 keeps them, so every page resolved to a `../../` path it could not
  match to a route and every internal link came back invalid. In its place
  `site/src/plugins/build-checks.ts` reads the built directory at
  `astro:build:done` and checks every internal href — on every page, including
  the ones written by hand — against the routes and heading ids the build
  actually shipped.

  The two halves fit together deliberately. The Markdown plugin rewrites only
  what it can prove and **leaves an unresolvable relative link exactly as
  written**, because nothing else in this build emits a relative href, so a
  surviving one is a link to nowhere and the check refuses it. It does not
  throw: Astro's glob loader catches an error thrown while rendering an entry,
  logs it, and stores the entry unrendered — a red line in the output and a
  build that exits 0. The rule has to be enforced somewhere that can stop.

  The same hook checks three more things. That each docs page rendered a body,
  for the same reason: a page can ship with its title, its sidebar and its
  search entry and nothing in the middle, on a green build. Each page has to
  render as many `<h2>`s as its source has `##` headings. And that every SVG
  in the build parses as XML, through `sax` in strict mode — the parser SVGO
  uses. A comment containing `--` makes the whole file fail to render, which
  is how [the wordmark ticket](https://github.com/frankieramirez/ripen/issues/114)
  shipped a broken favicon that looked fine inline and rendered as nothing as
  a file; a real parser refuses an unclosed tag and a bare `&` on the same
  terms. And that every page names an `og:image` and that the file it names is
  in the build — a card is checked by a crawler on somebody else's machine,
  days later, and a wrong path shows up as a bare grey link with no error
  anywhere for anyone to see. Astro's redirect stubs are skipped: a page whose
  whole body is a meta refresh is never what a link resolves to.

  `ci.yaml` runs `npm run check` beside `npm run build`. `astro check` covers
  the `.astro` files and the TypeScript beside them — the sidebar map, the
  loader and the two build plugins — none of which `astro build` type checks.

- **Code blocks.** Expressive Code, with two global settings changed and no
  per-block customization. Both changes are the browser arguing with the
  "defaults" instruction, and the visual direction winning:

  **No frame.** Shell blocks otherwise render inside a fake terminal window
  with three traffic lights — which the terminal treatment rules out by name,
  and which would put the same command in two voices on two pages.

  **Neutrals-only syntax**, in `site/src/styles/code-theme.ts`. The defaults
  are GitHub's, which set commands in blue and flags in purple, and
  purple/indigo accents are on the list of things this site must not look
  like. The theme says the one thing worth saying and it is what the landing's
  terminal block says by hand: keys `muted`, everything else `ink`. Two themes
  rather than one, since those are different colours in each, named `dark` and
  `light` so Expressive Code's selectors line up with the attribute the toggle
  writes, and paired with its dark-mode media query so the third state — no
  attribute, follow the system — resolves the way the rest of the palette
  does.

  There were no missing `yaml`/`json` language tags to fix. All thirty-seven
  fenced blocks across the nine pages already carry one; the single untagged
  block is the architecture diagram, which is ASCII art and correctly has no
  language.

- **Theming.** Starlight's own properties are redefined in terms of the six
  tokens and two faces, unlayered so they beat `@layer starlight.base` without
  an `!important`. Nothing is duplicated per theme: every value is a
  `var(--token)`, so the three-state cascade in `tokens.css` decides both
  themes at once.

  Three components are overridden, and only one of them is cosmetic.
  `SiteTitle` is the wordmark. `ThemeProvider` and `ThemeSelect` are the
  toggle: Starlight stores its preference under `starlight-theme` and writes
  an explicit `dark` or `light` on every load, where this site stores
  `ripen-theme` and treats the attribute's absence as "follow the system", so
  leaving both in place would mean the docs and the landing disagreeing about
  the reader's choice one navigation apart.

  Two of Starlight's layout measurements moved, and this is the line between
  theming and forking that the ticket asked about — it falls on the theming
  side, because both are variables Starlight publishes for the purpose.
  `--sl-content-width` goes 45rem → 52rem and `--sl-sidebar-width` 18.75rem →
  15rem: `configuration.md` is a three-column field table and `agents.md` has
  six more, and at the stock width the third column wrapped to one word a line
  and scrolled out of sight. The sidebar has the room to give — nine flat
  labels, the longest of them "Troubleshooting".

- **Markdown processor.** Sätteri, Astro 7's default, named in the config only
  to hang the one plugin off it. `markdown.remarkPlugins` also works and is
  deprecated, and it quietly swaps the whole processor for unified — a
  different Markdown implementation, adopted by accident.

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
