import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, posix, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import type { AstroIntegration } from "astro";
import sax from "sax";

import { DOCS_MAP } from "../docs-map";

/** Internal, and worth resolving: not a scheme, a protocol-relative URL, or empty. */
const EXTERNAL = /^(?:[a-z][a-z0-9+.-]*:|\/\/)/i;

interface Target {
  readonly path: string;
  readonly anchor: string;
  readonly relative?: boolean;
}

const REPO_ROOT = fileURLToPath(new URL("../../../", import.meta.url));

export function buildChecks(): AstroIntegration {
  return {
    name: "ripen-build-checks",
    hooks: {
      "astro:build:done": ({ dir, logger }) => {
        const root = fileURLToPath(dir);
        logger.info(`${docsRendered(root)} docs pages rendered with a body.`);
        logger.info(`${svgsParse(root)} SVGs parse as XML.`);
        logger.info(`${cardsShipped(root)} pages carry a link preview that is on disk.`);
        logger.info(`${indexAgrees(root)} pages in the sitemap, and no others indexable.`);
        const pages = filesIn(root, ".html").map((file) => ({
          route: routeOf(relative(root, file)),
          file,
          html: readFileSync(file, "utf-8"),
        }));

        const anchorsByRoute = new Map(
          pages.map((page) => [page.route, anchorsIn(page.html)]),
        );

        const broken: string[] = [];
        let checked = 0;

        for (const page of pages) {
          for (const href of hrefsIn(page.html)) {
            if (EXTERNAL.test(href)) continue;
            const target = resolve(href, page.route);
            if (!target) continue;

            checked += 1;
            const anchors = anchorsByRoute.get(target.path);
            if (target.relative) {
              broken.push(
                `${page.route} -> ${href} -- a relative link, left as written. ` +
                  `In a root doc that means it names nothing in the repository.`,
              );
            } else if (!anchors) {
              broken.push(`${page.route} -> ${href} -- no such page.`);
            } else if (target.anchor && !anchors.has(target.anchor)) {
              broken.push(`${page.route} -> ${href} -- no such heading on that page.`);
            }
          }
        }

        if (broken.length > 0) {
          throw new Error(
            `${broken.length} broken internal link(s):\n  ${broken.join("\n  ")}`,
          );
        }
        logger.info(`${checked} internal links, all resolving.`);
      },
    },
  };
}

function filesIn(dir: string, extension: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return filesIn(path, extension);
    return path.endsWith(extension) ? [path] : [];
  });
}

/**
 * Every SVG the build ships, put through a real XML parser rather than a
 * pattern that knows about the one fault already seen. `sax` in strict mode
 * is the parser SVGO uses, and it refuses an unclosed tag and a bare `&` as
 * readily as the `--` that broke the favicon.
 */
function svgsParse(root: string): number {
  const svgs = filesIn(root, ".svg");
  const broken = svgs.flatMap((file) => {
    const fault = xmlFault(readFileSync(file, "utf-8"));
    return fault ? [`${relative(root, file)}: ${fault}`] : [];
  });

  if (broken.length > 0) {
    throw new Error(
      `SVG is parsed as XML, and these do not parse:\n  ${broken.join("\n  ")}`,
    );
  }
  return svgs.length;
}

function xmlFault(xml: string): string | undefined {
  let fault: string | undefined;
  const parser = sax.parser(true);
  parser.onerror = (error) => {
    fault ??= error.message.split("\n")[0];
    parser.resume();
  };
  try {
    parser.write(xml).close();
  } catch (error) {
    fault ??= error instanceof Error ? error.message.split("\n")[0] : String(error);
  }
  return fault;
}

/** `docs/agents/index.html` is the route `/docs/agents/`. */
function routeOf(relativePath: string): string {
  const url = `/${relativePath.split(sep).join(posix.sep)}`;
  return url.endsWith("/index.html") ? url.slice(0, -"index.html".length) : url;
}

const HREF = /\shref="([^"]*)"/g;
const ANCHOR = /\s(?:id|name)="([^"]+)"/g;

const hrefsIn = (html: string): string[] =>
  [...html.matchAll(HREF)].map(([, href]) => decodeURI(href!));

const anchorsIn = (html: string): Set<string> =>
  new Set([...html.matchAll(ANCHOR)].map(([, id]) => id!));

/** An href, as a route to look up plus the heading it wants, or null to skip. */
function resolve(href: string, from: string): Target | null {
  const [path, ...rest] = href.split("#");
  const anchor = rest.join("#");

  if (!path) return anchor ? { path: from, anchor } : null;

  const route = path.split("?")[0]!;
  if (!route.startsWith("/")) return { path: route, anchor, relative: true };

  if (/\.[a-z0-9]+$/i.test(route)) return null;

  return { path: route.endsWith("/") ? route : `${route}/`, anchor };
}

const CARD = /<meta\s[^>]*?(?:property|name)="(?:og|twitter):image"[^>]*?content="([^"]+)"/g;
const SITE = "https://ripen.dev";
const REDIRECT = /<meta\s[^>]*http-equiv="refresh"/i;

function cardsShipped(root: string): number {
  const faults: string[] = [];
  const pages = filesIn(root, ".html");
  let redirects = 0;

  for (const file of pages) {
    const route = routeOf(relative(root, file));
    const html = readFileSync(file, "utf-8");

    if (REDIRECT.test(html)) {
      redirects += 1;
      continue;
    }

    const cards = new Set([...html.matchAll(CARD)].map(([, url]) => url!));

    if (cards.size === 0) {
      faults.push(`${route} -- no og:image, so it shares as a bare grey link.`);
      continue;
    }
    for (const url of cards) {
      if (!url.startsWith(`${SITE}/`)) {
        faults.push(`${route} -> ${url} -- not on ${SITE}, so this build cannot vouch for it.`);
      } else if (read(join(root, ...url.slice(SITE.length + 1).split("/"))) === undefined) {
        faults.push(`${route} -> ${url} -- named as the card, but not in the build.`);
      }
    }
  }

  if (faults.length > 0) {
    throw new Error(`${faults.length} link preview fault(s):\n  ${faults.join("\n  ")}`);
  }
  return pages.length - redirects;
}

const NOINDEX = /<meta\s[^>]*name="robots"[^>]*content="[^"]*noindex/i;
const LOC = /<loc>([^<]+)<\/loc>/g;

function indexAgrees(root: string): number {
  const listed = new Set(
    filesIn(root, ".xml")
      .filter((file) => /sitemap-\d+\.xml$/.test(file))
      .flatMap((file) => [...readFileSync(file, "utf-8").matchAll(LOC)])
      .map(([, loc]) => new URL(loc!).pathname),
  );

  const faults: string[] = [];

  for (const file of filesIn(root, ".html")) {
    const html = readFileSync(file, "utf-8");
    if (REDIRECT.test(html)) continue;

    const route = routeOf(relative(root, file));
    const page = route.endsWith(".html")
      ? `${route.slice(0, -".html".length)}/`
      : route;

    if (NOINDEX.test(html)) {
      if (listed.has(page)) {
        faults.push(
          `${page} -- noindex, and in the sitemap. The sitemap invites the ` +
            `crawl that the tag then tells the crawler to forget.`,
        );
      }
    } else if (!listed.has(page)) {
      faults.push(
        `${page} -- indexable, and not in the sitemap. Either it wants ` +
          `noindex, or a filter is dropping more than it was written to.`,
      );
    }
  }

  if (faults.length > 0) {
    throw new Error(`${faults.length} indexing disagreement(s):\n  ${faults.join("\n  ")}`);
  }
  return listed.size;
}

/** Every page in the sidebar map is on disk, with the headings its source has. */
function docsRendered(root: string): number {
  const thin = DOCS_MAP.filter((page) => {
    const html = read(join(root, ...page.id.split("/"), "index.html"));
    if (html === undefined) return true;
    const source = read(join(REPO_ROOT, page.source)) ?? "";
    return count(html, /<h2[\s>]/g) < count(source, /^## /gm);
  });

  if (thin.length > 0) {
    throw new Error(
      `${thin.map((page) => page.source).join(", ")} built without the headings ` +
        `the source has. The page rendered empty, or did not render at all -- ` +
        `check the Markdown plugins.`,
    );
  }
  return DOCS_MAP.length;
}

const read = (path: string): string | undefined => {
  try {
    return readFileSync(path, "utf-8");
  } catch {
    return undefined;
  }
};

const count = (text: string, pattern: RegExp): number => [...text.matchAll(pattern)].length;
