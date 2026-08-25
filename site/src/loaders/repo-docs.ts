import { readFileSync } from "node:fs";
import { glob, type Loader, type LoaderContext } from "astro/loaders";

import { DOCS_MAP, PAGE_BY_ID, PAGE_BY_SOURCE } from "../docs-map";

/*
 * The docs collection is the root `docs/` directory, read where it lives.
 *
 * ADR 0004 keeps those files canonical and byte-identical for GitHub readers,
 * which rules out both a copy under `site/` and frontmatter added to the
 * originals. So the collection is Astro's own glob loader pointed at the
 * repository root, wrapped to supply the frontmatter the files do not carry.
 *
 * One trap when working on any of this: rendered pages are cached in
 * `node_modules/.astro`, keyed by each file's digest. A change to the loader
 * or to a Markdown plugin does not re-render a doc that has not itself
 * changed, so a local build can keep serving the old HTML. `rm -rf
 * node_modules/.astro` before believing a result. CI installs from scratch
 * and is always cold.
 *
 * The wrapper is thin on purpose. Everything about parsing, rendering and
 * watching stays the stock loader's job -- the wrap only intercepts
 * `parseData`, which is the one call the glob loader makes between reading a
 * file's frontmatter and validating it against the schema. That is where the
 * title, the description and the sidebar order get injected, and where a file
 * nobody has published fails the build.
 */

/** Where the glob runs, relative to `site/`. */
const REPO_ROOT = "../";

/**
 * Globbed but deliberately unpublished. An exclusion is a decision, so it is
 * written down here rather than being an absence in the sidebar map: today
 * exactly `release-credentials.md`, which is maintainer-facing, omitted from
 * the README's documentation table, and links into `rework/` and `adr/`.
 */
const EXCLUDED = ["docs/release-credentials.md"];

const PATTERN = [
  "docs/*.md",
  // Never `docs/**`: that would publish `rework/SPEC.md`, the ADRs, and the
  // generated `docs/schema/v1/` that invariant 8 asserts against `ripen schema`.
  "CONTEXT.md",
  ...EXCLUDED.map((path) => `!${path}`),
];

/** The page's H1, which is the title unless the sidebar map overrides it. */
function headingOf(filePath: string): string {
  const heading = readFileSync(filePath, "utf-8").match(/^#[^\S\n]+(.+?)\s*$/m);
  if (!heading?.[1]) {
    throw new Error(
      `${filePath} has no H1. The docs pipeline takes each page's title from its heading, ` +
        `so a page without one cannot be published.`,
    );
  }
  return heading[1];
}

function unpublished(entry: string): Error {
  return new Error(
    `${entry} is in docs/ but not in the sidebar map. Add it to site/src/docs-map.ts ` +
      `to publish it, or to the exclude list in site/src/loaders/repo-docs.ts to keep it ` +
      `off the site.`,
  );
}

export function repoDocsLoader(): Loader {
  const inner = glob({
    base: REPO_ROOT,
    pattern: PATTERN,
    generateId: ({ entry }) => {
      const page = PAGE_BY_SOURCE.get(entry);
      if (!page) throw unpublished(entry);
      return page.id;
    },
  });

  return {
    name: "ripen-repo-docs",
    async load(context: LoaderContext) {
      await inner.load({
        ...context,
        parseData: ({ id, data, filePath }) => {
          const page = PAGE_BY_ID.get(id);
          // Unreachable through `generateId`, which has already refused an
          // unmapped file. Kept because `parseData` is what validates, and a
          // silent `undefined` here would ship a page with no title.
          if (!page) throw unpublished(id);

          return context.parseData({
            id,
            filePath,
            data: {
              ...data,
              title: page.title ?? headingOf(filePath!),
              description: page.description,
              // The canonical file is the thing to edit, so the edit link goes
              // to it on GitHub rather than to a path under `site/`.
              editUrl: `https://github.com/frankieramirez/ripen/edit/main/${page.source}`,
            },
          });
        },
      });

      // The other direction. A map entry whose file has been renamed or
      // deleted would otherwise leave a sidebar link to a 404, which is the
      // failure mode the map exists to prevent.
      const loaded = new Set(context.store.keys());
      const missing = DOCS_MAP.filter((page) => !loaded.has(page.id));
      if (missing.length > 0) {
        throw new Error(
          `the sidebar map lists ${missing.map((page) => page.source).join(", ")}, ` +
            `which the glob did not find. Remove the entry from site/src/docs-map.ts, ` +
            `or restore the file.`,
        );
      }
    },
  };
}
