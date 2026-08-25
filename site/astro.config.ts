// @ts-check
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import { satteri } from "@astrojs/markdown-satteri";

import { DOCS_MAP, routeFor } from "./src/docs-map";
import { buildChecks } from "./src/plugins/build-checks";
import { codeThemes } from "./src/styles/code-theme";
import { repoDocsMarkdown } from "./src/plugins/repo-docs-markdown";

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
      ],
    }),
    // Last, so it runs against the finished directory.
    buildChecks(),
  ],
});
