import { readFileSync } from "node:fs";
import { glob, type Loader, type LoaderContext } from "astro/loaders";

import { DOCS_MAP, PAGE_BY_ID, PAGE_BY_SOURCE } from "../docs-map";

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
          if (!page) throw unpublished(id);

          return context.parseData({
            id,
            filePath,
            data: {
              ...data,
              title: page.title ?? headingOf(filePath!),
              description: page.description,
              editUrl: `https://github.com/frankieramirez/ripen/edit/main/${page.source}`,
            },
          });
        },
      });

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
