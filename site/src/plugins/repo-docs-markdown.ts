import { existsSync, statSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { defineMdastPlugin } from "satteri";

import { PAGE_BY_SOURCE, routeFor } from "../docs-map";

/*
 * The two edits the root docs need on their way to becoming site pages.
 *
 * Both exist because the files are canonical. They are written for a GitHub
 * reader and stay byte-identical for one, so anything the site needs
 * differently has to happen here, at build time, and never in the file.
 *
 * 1. The leading H1 comes out. Starlight sets the page title itself, from the
 *    frontmatter the loader injects -- which is that same H1. Left in, every
 *    page would carry its title twice.
 *
 * 2. Relative links are resolved by collection membership. A link to a
 *    published page becomes its site route; a link to anything else in the
 *    repository becomes its absolute GitHub URL. There is no hand-maintained
 *    list of either, so a doc that starts linking somewhere new is handled
 *    without anyone remembering to come here.
 *
 * A relative link that resolves to nothing in the working tree is a broken
 * link, and it is left exactly as written -- which is what fails the build,
 * one hook later. Nothing else in this site emits a relative href, so the
 * build checks treat a surviving one as a link to nowhere and refuse it.
 *
 * That indirection is deliberate. Throwing here does not fail the build:
 * Astro's glob loader catches an error thrown while rendering an entry, logs
 * it, and carries on with the entry unrendered. The rule has to be enforced
 * somewhere that can stop.
 */

const REPO_ROOT = fileURLToPath(new URL("../../../", import.meta.url));
const GITHUB = "https://github.com/frankieramirez/ripen";

/** Absolute paths of the canonical files this plugin may touch. */
const SOURCES = new Set(
  [...PAGE_BY_SOURCE.keys()].map((source) => resolve(REPO_ROOT, source)),
);

/** Anything with a scheme, a host, a root-relative path, or a bare anchor. */
const NOT_RELATIVE = /^(?:[a-z][a-z0-9+.-]*:|\/\/|\/|#)/i;

export const repoDocsMarkdown = () =>
  defineMdastPlugin({
    name: "ripen-repo-docs",

    heading(node, ctx) {
      if (!sourceDir(ctx.fileURL)) return;
      // The leading one only: a later `#` heading would be the author's, and
      // removing it would be an edit rather than a de-duplication.
      if (node.depth === 1 && ctx.indexOf(node) === 0) ctx.removeNode(node);
    },

    link(node, ctx) {
      const dir = sourceDir(ctx.fileURL);
      if (!dir || NOT_RELATIVE.test(node.url)) return;
      ctx.setProperty(node, "url", rewrite(node.url, dir));
    },

    definition(node, ctx) {
      const dir = sourceDir(ctx.fileURL);
      if (!dir || NOT_RELATIVE.test(node.url)) return;
      ctx.setProperty(node, "url", rewrite(node.url, dir));
    },
  });

/**
 * The directory a document's relative links resolve against, or undefined if
 * the document is not one of the canonical files -- site-authored Markdown is
 * left alone.
 *
 * Read off each node's own context rather than remembered in the factory's
 * closure. The loader compiles ten files at a time, so a plugin that caches
 * the first document it sees is one lifetime assumption away from resolving
 * one page's links against another page's directory, and the failure would
 * be a wrong URL rather than an error.
 */
function sourceDir(fileURL: URL | undefined): string | undefined {
  if (!fileURL) return undefined;
  const path = fileURLToPath(fileURL);
  return SOURCES.has(path) ? dirname(path) : undefined;
}

function rewrite(url: string, from: string): string {
  const [target, ...rest] = url.split("#");
  const anchor = rest.length > 0 ? `#${rest.join("#")}` : "";
  if (!target) return url;

  const absolute = resolve(from, target);
  const repoRelative = relative(REPO_ROOT, absolute);

  // Outside the repository, or naming nothing in it. Either way there is no
  // rewrite that could be right, so the link keeps the form the build checks
  // will refuse.
  if (repoRelative.startsWith("..") || !existsSync(absolute)) return url;

  const page = PAGE_BY_SOURCE.get(repoRelative);
  if (page) return `${routeFor(page)}${anchor}`;

  // `blob` redirects to `tree` for a directory rather than 404-ing, but the
  // path is known here, so the link can just be right.
  const kind = statSync(absolute).isDirectory() ? "tree" : "blob";
  return `${GITHUB}/${kind}/main/${repoRelative}${anchor}`;
}
