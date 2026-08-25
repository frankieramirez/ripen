# The site lives in `site/`; root `docs/` stays canonical

Status: accepted.

ripen.dev is built from a `site/` directory in this repository. The
documentation it publishes is the root `docs/` directory, which remains the
canonical copy: the site's build globs `docs/*.md` (plus `CONTEXT.md`) at
build time, and the markdown files are not moved, mirrored, or annotated with
frontmatter. What GitHub readers see and what the site publishes are the same
bytes.

Full detail: [ripen.dev map](https://github.com/frankieramirez/ripen/issues/98)
and [Docs pipeline shape](https://github.com/frankieramirez/ripen/issues/103).
The site's own design document is [`site/README.md`](../../site/README.md).

This earns an ADR because it will surprise a future reader — `site/` reaches
*outside itself* for content, which no framework's default project layout
does — and because it becomes painful to reverse once ripen.dev URLs exist:
moving the docs later breaks either the site's routes or every repo-relative
link on GitHub.

The glob is `docs/*.md`, deliberately not `docs/**`. The subdirectories are
not documentation for operators: `rework/` is the implementation spec,
`adr/` is this series, `schema/v1/` is generated and asserted byte-identical
to `ripen schema` output. An explicit exclude list (today exactly
`release-credentials.md`, which is maintainer-facing) handles top-level
exceptions. Discovery of a new `docs/*.md` file is automatic, but publishing
it takes one line in the site's sidebar map, and the build fails until that
line exists; the exclude list documents what is deliberately never published.

Because the canonical files carry no frontmatter and keep their repo-relative
links, the site build does the adaptation: titles come from each page's H1,
ordering from a sidebar map in `site/` config (a globbed page missing from
the map fails the build), and links rewrite mechanically by collection
membership — a link to a published page becomes a site route, anything else
becomes an absolute GitHub URL. All of that machinery lives in `site/` and
none of it leaks into `docs/`.

## Considered options

- **Move `docs/` into `site/` and make the site canonical.** Rejected. The
  docs are read on GitHub today, linked from the README's documentation
  table, and referenced by relative links from `AGENTS.md`, `CONTEXT.md`, and
  each other. Moving them breaks every existing link and deep-links held by
  users, and subordinates the operator documentation to a website that is,
  by the map's own scoping, the less essential surface.
- **Mirror `docs/` into `site/` with a sync script.** Rejected. Two copies
  and a script that must run is exactly the drift this project refuses
  elsewhere — the schema invariant exists because generated copies lie when
  the sync step is forgotten. Globbing at build time has one copy and
  nothing to forget.
- **A second repository for the site.** Rejected. It cannot glob the docs at
  all without vendoring or submodules, needs its own credentials and CI, and
  splits changes that belong together — a PR that adds a config field should
  update `docs/configuration.md` and have the site build check it in the same
  change.

## Consequences

- A docs edit can break the site build (a new page missing from the sidebar
  map, a broken relative link). `ci.yaml` runs the site build on PRs touching
  `docs/**`, the published root markdown files, or `site/**`, so the failure
  is a visible check on the PR that caused it — while site-only edits do not
  fire the Go matrix, and Go-only edits do not build the site.
- The site's page set follows `docs/` without a vendoring or sync step, but
  adding a doc is a two-file change: the file itself, and its line in the
  sidebar map. Publishing decisions are expressed in one exclude list and one
  sidebar map, both in `site/`.
- Heavy site assets stay out of `docs/` and out of git where possible: the
  module zip for `go install github.com/frankieramirez/ripen/...` packs the
  repository, and the site must not bloat it.
- Anyone reshaping `docs/` (renaming a file, adding a subdirectory) is
  changing site routes and must treat it as such.
