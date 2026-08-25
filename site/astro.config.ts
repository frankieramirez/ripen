// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import sitemap from "@astrojs/sitemap";
import { satteri } from "@astrojs/markdown-satteri";

import { DOCS_MAP, routeFor } from "./src/docs-map";
import { NOINDEX_ROUTES } from "./src/noindex";
import { buildChecks } from "./src/plugins/build-checks";
import { codeThemes } from "./src/styles/code-theme";
import { repoDocsMarkdown } from "./src/plugins/repo-docs-markdown";

// Kept identical to BaseLayout.astro's, by hand: the two surfaces are wired
// by different mechanisms and there is nothing either of them can import that
// an .astro frontmatter and a config object both reach.
const OG_ALT =
  "A digest ripens. You apply. — ripen.dev, set in white monospace on " +
  "near-black beside Ripen's two-circle mark.";

// Static output, no adapter: the build is a directory of files that Cloudflare
// Workers Static Assets serves. An adapter would put a server in the request
// path that nothing here needs.
//
// TypeScript rather than .mjs since #118, because the config imports the
// sidebar map -- the sidebar and the docs collection have to agree about
// which pages exist, and the way to guarantee that is for both to be built
// from the same list.
export default defineConfig({
  site: "https://ripen.dev",
  output: "static",

  // `/docs` is not a page: the pages are `/docs/<name>`, ordered by the
  // sidebar map. Someone who types the bare path gets the first of them
  // rather than a 404.
  redirects: {
    "/docs": routeFor(DOCS_MAP[0]!),
  },

  markdown: {
    // Sätteri is Astro's own pipeline and the default; it is named here only
    // to hang one plugin off it. `markdown.remarkPlugins` would have worked
    // and is deprecated, and it quietly swaps the whole processor for
    // unified -- a different Markdown implementation, adopted by accident.
    //
    // The plugin applies to the root docs only: it checks the file it is
    // given and returns immediately for anything authored under site/.
    processor: satteri({ mdastPlugins: [repoDocsMarkdown] }),
  },

  integrations: [
    starlight({
      title: "Ripen",
      // The site's 404 is src/pages/404.astro, for the landing and the docs
      // alike. Without this Starlight injects its own at the same pattern,
      // ours wins on route precedence, and every build logs the collision as
      // a warning -- a permanent warning being the thing that teaches people
      // to stop reading them.
      disable404Route: true,
      description:
        "Ripen watches what :latest actually points at, waits for a new image to prove itself, and updates one service -- only where you said it may.",
      // The landing page owns `/`. Starlight's routes all live under the
      // `docs/` prefix their collection ids carry, so the two do not overlap.
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/frankieramirez/ripen",
        },
      ],
      // Flat, in the README's documentation-table order with Vocabulary
      // prepended. Built from the map rather than repeated here, so a page
      // cannot be in the collection and missing from the sidebar.
      sidebar: DOCS_MAP.map((page) => ({
        label: page.label,
        link: routeFor(page),
      })),
      components: {
        SiteTitle: "./src/components/starlight/SiteTitle.astro",
        ThemeProvider: "./src/components/starlight/ThemeProvider.astro",
        ThemeSelect: "./src/components/starlight/ThemeSelect.astro",
      },
      // tokens.css and fonts.css are the design system; starlight.css is the
      // adapter that dresses Starlight's own properties in it. base.css is
      // deliberately absent: its element rules are the landing page's layout.
      customCss: [
        "./src/styles/tokens.css",
        "./src/styles/fonts.css",
        "./src/styles/starlight.css",
      ],
      // Expressive Code's own defaults, with one global default changed: no
      // frame. Shell blocks otherwise render inside a fake terminal window
      // with three traffic lights, which the visual direction rules out by
      // name -- the landing's terminal is a bare block with one hairline, and
      // a docs page drawing a window around the same command would be the
      // same content in two voices. This is a default, not per-block markup;
      // no block in docs/ asks for a frame.
      expressiveCode: {
        themes: codeThemes,
        defaultProps: { frame: "none" },
        styleOverrides: {
          borderColor: "var(--border)",
          borderRadius: "0",
          codeBackground: "var(--surface)",
          frames: { shadowColor: "transparent" },
        },
      },
      favicon: "/favicon.svg",
      head: [
        {
          tag: "link",
          attrs: { rel: "icon", href: "/favicon-32.png", sizes: "32x32", type: "image/png" },
        },
        { tag: "link", attrs: { rel: "apple-touch-icon", href: "/apple-touch-icon.png" } },
        // The two faces that set the first paint that matters, same as the
        // landing. The italic and the 500 weight are fetched normally.
        {
          tag: "link",
          attrs: {
            rel: "preload",
            href: "/fonts/ibm-plex-mono-400-latin.woff2",
            as: "font",
            type: "font/woff2",
            crossorigin: true,
          },
        },
        {
          tag: "link",
          attrs: {
            rel: "preload",
            href: "/fonts/source-serif-4-latin.woff2",
            as: "font",
            type: "font/woff2",
            crossorigin: true,
          },
        },
        /*
         * The link preview. Starlight already writes the `og:` pair, the url,
         * the locale, the site name and `twitter:card` from each page's own
         * frontmatter -- what it has no opinion about is the image, because
         * there is no per-page one to have an opinion about. One card for the
         * whole site, the same one the landing carries; see BaseLayout.astro
         * for why there is no `twitter:title` and no `theme-color`.
         *
         * Absolute, spelled out rather than built from `site`, because
         * Starlight's head entries are plain attribute objects evaluated at
         * config time with no page context to resolve a URL against.
         */
        {
          tag: "meta",
          attrs: { property: "og:image", content: "https://ripen.dev/og.png" },
        },
        { tag: "meta", attrs: { property: "og:image:type", content: "image/png" } },
        { tag: "meta", attrs: { property: "og:image:width", content: "1200" } },
        { tag: "meta", attrs: { property: "og:image:height", content: "630" } },
        { tag: "meta", attrs: { property: "og:image:alt", content: OG_ALT } },
        {
          tag: "meta",
          attrs: { name: "twitter:image", content: "https://ripen.dev/og.png" },
        },
        { tag: "meta", attrs: { name: "twitter:image:alt", content: OG_ALT } },
      ],
    }),
    /*
     * Ours, so it can take a filter. Starlight registers `@astrojs/sitemap`
     * itself with no way to configure it -- but only if the integration is
     * not already in the list, so declaring it here replaces Starlight's
     * rather than adding a second one. That also promotes the package from a
     * transitive dependency to a direct one, which is what importing it makes
     * it anyway.
     */
    sitemap({
      filter: (page) => !NOINDEX_ROUTES.includes(new URL(page).pathname),
    }),
    // Last, so it runs against the finished directory.
    buildChecks(),
  ],
});
