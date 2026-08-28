import { existsSync, statSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { defineMdastPlugin } from "satteri";

import { PAGE_BY_SOURCE, routeFor } from "../docs-map";

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

  if (repoRelative.startsWith("..") || !existsSync(absolute)) return url;

  const page = PAGE_BY_SOURCE.get(repoRelative);
  if (page) return `${routeFor(page)}${anchor}`;

  const kind = statSync(absolute).isDirectory() ? "tree" : "blob";
  return `${GITHUB}/${kind}/main/${repoRelative}${anchor}`;
}
