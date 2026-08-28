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

const OG_ALT =
  "A digest ripens. You apply. — ripen.dev, set in white monospace on " +
  "near-black beside Ripen's two-circle mark.";

export default defineConfig({
  site: "https://ripen.dev",
  output: "static",

  redirects: {
    "/docs": routeFor(DOCS_MAP[0]!),
  },

  markdown: {
    processor: satteri({ mdastPlugins: [repoDocsMarkdown] }),
  },

  integrations: [
    starlight({
      title: "Ripen",
      disable404Route: true,
      description:
        "Ripen watches what :latest actually points at, waits for a new image to prove itself, and updates one service -- only where you said it may.",
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/frankieramirez/ripen",
        },
      ],
      sidebar: DOCS_MAP.map((page) => ({
        label: page.label,
        link: routeFor(page),
      })),
      components: {
        SiteTitle: "./src/components/starlight/SiteTitle.astro",
        ThemeProvider: "./src/components/starlight/ThemeProvider.astro",
        ThemeSelect: "./src/components/starlight/ThemeSelect.astro",
      },
      customCss: [
        "./src/styles/tokens.css",
        "./src/styles/fonts.css",
        "./src/styles/starlight.css",
      ],
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
    sitemap({
      filter: (page) => !NOINDEX_ROUTES.includes(new URL(page).pathname),
    }),
    buildChecks(),
  ],
});
