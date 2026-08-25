import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, posix, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import type { AstroIntegration } from "astro";
import sax from "sax";

import { DOCS_MAP } from "../docs-map";

/*
 * The things `astro build` refuses to finish on, checked against the built
 * directory.
 *
 * It reads the built HTML rather than the sources, which is what makes it
 * trustworthy: it checks the hrefs the reader will actually click against the
 * files and the heading ids the site actually shipped. Nothing here has to
 * model how a slug is generated, how a route is formatted, or how a heading
 * becomes an anchor -- those are the build's answers, already on disk.
 *
 * The link half replaces `starlight-links-validator`, which was the spec's
 * suggestion.
 * That plugin keys its data by each page's path under `src/content/docs/`,
 * and this site's pages are the root `docs/` files read where ADR 0004 keeps
 * them, so every page resolved to a `../../` path it could not match to a
 * route and every internal link came back invalid.
 *
 * It is the enforcing half of the link rule. The mdast plugin rewrites what
 * it can prove and leaves the rest alone; this refuses an href to a route or
 * an anchor the built site does not have -- on every page, including the ones
 * written by hand.
 *
 * The third check is that every SVG in the build is well-formed XML, which
 * [#114](https://github.com/frankieramirez/ripen/issues/114) handed here. A
 * comment written in this repo's `--` style shipped a broken favicon: SVG is
 * parsed as XML, where `--` inside a comment is a syntax error, and an HTML
 * page parses an inline copy anyway -- so the component looked right while
 * the shipped icon rendered as nothing. The build said nothing either.
 *
 * The fourth is the link preview: every page has to name an `og:image`, and
 * the image it names has to be a file in this build. A card is checked by a
 * crawler somewhere else, days later, on a page nobody is watching -- there
 * is no error to see, just a bare grey link, so the build looks instead.
 *
 * The other check is that each docs page has a body. Astro's glob loader
 * catches an error thrown while rendering an entry, logs it, and stores the
 * entry unrendered -- which ships a page with its title, its sidebar and its
 * search entry, and nothing in the middle, on a build that exits 0. Nothing
 * else on the way out notices, so this does. Each page has to render as many
 * `<h2>`s as its source has `##` headings: a statement about the content,
 * rather than a size threshold somebody has to keep plausible.
 */

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
  // Strict sax reports through the handler and keeps going once resumed, so
  // the first fault is kept and the rest of the document is not walked twice.
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

// Attribute matching, not HTML parsing. The input is this build's own output,
// where every href and id is a double-quoted attribute written by Astro.
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

  // A link into the same page.
  if (!path) return anchor ? { path: from, anchor } : null;

  // Nothing in this build emits a relative href. Astro writes its own
  // root-relative, and the remark plugin rewrites every relative link a root
  // doc carries -- except the ones it could not resolve, which it leaves
  // alone precisely so they arrive here.
  const route = path.split("?")[0]!;
  if (!route.startsWith("/")) return { path: route, anchor, relative: true };

  // Assets -- the fonts, the icons, the OG image later -- are not pages, and
  // the build does not put them in a directory with an index.
  if (/\.[a-z0-9]+$/i.test(route)) return null;

  return { path: route.endsWith("/") ? route : `${route}/`, anchor };
}

/*
 * Every page names a card, and every card it names is in the build.
 *
 * The image is absolute -- a crawler resolves it against nothing -- so this
 * has to strip the site origin back off before it can look for the file. An
 * absolute URL on some other host is somebody else's asset and not ours to
 * check.
 */
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

    // `/docs` is a redirect stub -- a meta refresh and nothing else. It is
    // never the page a link resolves to, so it has no preview to carry.
    if (REDIRECT.test(html)) {
      redirects += 1;
      continue;
    }

    // De-duplicated: `og:image` and `twitter:image` name the same file, and
    // one missing card should read as one fault rather than two.
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
