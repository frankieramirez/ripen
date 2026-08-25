/*
 * Draws the OG card and writes it to public/. Run it with `npm run og`.
 *
 * Committed output, committed generator. The card is a static asset because
 * a link preview is fetched by a crawler that will not run a build, and it is
 * generated rather than hand-drawn because every value in it belongs to the
 * design system: change a token and this is how the card follows.
 *
 * Not a build step. `astro build` runs in CI on a runner with no browser, and
 * a card that regenerates on every deploy is a binary that changes when
 * nothing about it did. It is rendered here, looked at, and committed.
 *
 * The renderer is headless Chrome over the DevTools protocol, driven by
 * Node's own global WebSocket -- no dependency, and the same engine that
 * renders the site, which is the point: the card is set in the real
 * subsetted woff2 with the browser's own hinting, not by a second text
 * shaper that would set the same string slightly differently.
 */

import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SITE = fileURLToPath(new URL("..", import.meta.url));

// Named rather than searched for. This is run by hand on a machine that has a
// browser, and a script that guesses at four install paths would be four ways
// to render the card in something other than what serves the site.
const CHROME =
  process.env.CHROME ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

/*
 * The composition, from site/README.md. Sizes are card pixels, and the card
 * is 1200 wide, so they are not the page's rem scale -- a preview is read at
 * about 500 CSS px in a timeline and nothing on it can be page-sized.
 */
const CARD = {
  pad: 80,
  glyph: 36, // tall; the box is 32:14, so ~82 wide
  headline: 104,
  /*
   * 38, not the 30 it was drawn at. A card is read at about 360 CSS px in a
   * Slack unfurl, where 30 card-pixels is 9 -- past quiet and into
   * unreadable. This is the smallest size that survives that scale.
   */
  domain: 38,
};

/*
 * "A digest ripens." / "You apply." -- the landing's own break, because the
 * card and the hero are the same sentence and a preview that re-breaks it
 * reads as a different one. Sixteen characters at 104px in a font that is
 * 0.6em wide is 998px, against 1040px of room between the gutters.
 */
const HEADLINE = ["A digest ripens.", "You apply."];
const DOMAIN = "ripen.dev";

const SIZES = [
  // Glyph pinned to the top edge, the sentence sitting on the bottom one. A
  // 1200x630 card is short enough that the two still read as one thing.
  { file: "og.png", width: 1200, height: 630, layout: "split" },
  /*
   * The 1:1 surfaces -- Mastodon, some chat clients. Laid out square rather
   * than centre-cropped out of the wide card, which would cut a 998px
   * headline down to 630px of room and take the verb off the end of it.
   *
   * The one thing that changes is that the glyph is not pinned to the top:
   * held there against a square, it leaves 700px of nothing in the middle and
   * the card reads as a mistake. Same gutter, same sizes, same order, same
   * alignment -- one centred stack instead of two edges.
   */
  { file: "og-square.png", width: 1200, height: 1200, layout: "stack" },
];

async function main() {
  const tokens = darkTokens();
  const glyph = glyphFrom(read("src/components/Glyph.astro"));
  const font = fontData("public/fonts/ibm-plex-mono-500-latin.woff2");
  const regular = fontData("public/fonts/ibm-plex-mono-400-latin.woff2");

  const chrome = await launch();
  try {
    for (const size of SIZES) {
      const html = card({ ...size, tokens, glyph, font, regular });
      const png = await shoot(chrome, html, size);
      writeFileSync(join(SITE, "public", size.file), png);
      console.log(`${size.file}  ${size.width}x${size.height}  ${(png.length / 1024).toFixed(1)} KB`);
    }
  } finally {
    chrome.kill();
  }
}

const read = (path) => readFileSync(join(SITE, path), "utf-8");

/*
 * The six dark values, read out of the design system rather than repeated.
 * Dark is the native theme and the only one the card has -- a preview renders
 * on somebody else's surface, so it carries the brand rather than answering a
 * media query it will never be asked.
 */
function darkTokens() {
  const root = read("src/styles/tokens.css").match(/:root\s*\{([\s\S]*?)\n\}/);
  if (!root) throw new Error("tokens.css: no :root block");
  return Object.fromEntries(
    [...root[1].matchAll(/--([a-z-]+):\s*(#[0-9a-f]{3,8});/gi)].map(([, name, value]) => [
      name,
      value,
    ]),
  );
}

/*
 * The glyph's geometry, lifted out of the component that draws it on the
 * page. One geometry: the card cannot end up with a mark that is a near-miss
 * of the site's, because it is not drawing a second one.
 */
function glyphFrom(component) {
  const viewBox = component.match(/viewBox="([^"]+)"/)?.[1];
  const circles = [...component.matchAll(/<circle\s([^>]*?)\s*\/?>/g)].map(([, attrs]) => attrs);
  if (!viewBox || circles.length !== 2) throw new Error("Glyph.astro: cannot read the geometry");
  return { viewBox, circles: circles.map((attrs) => `<circle ${attrs} />`) };
}

/*
 * The face goes in as a data URI. Chrome is loading the page from a data URL
 * with no origin, so a `/fonts/...` href resolves to nothing and the headline
 * would silently come out in the fallback -- which is a monospace too, so
 * nothing would look obviously wrong.
 */
function fontData(path) {
  return `data:font/woff2;base64,${readFileSync(join(SITE, path)).toString("base64")}`;
}

function card({ width, height, layout, tokens, glyph, font, regular }) {
  const glyphWidth = CARD.glyph * (32 / 14);
  return `<!doctype html>
<meta charset="utf-8">
<style>
  @font-face {
    font-family: "IBM Plex Mono"; font-style: normal; font-weight: 500;
    src: url("${font}") format("woff2");
  }
  @font-face {
    font-family: "IBM Plex Mono"; font-style: normal; font-weight: 400;
    src: url("${regular}") format("woff2");
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    width: ${width}px; height: ${height}px;
    background: ${tokens.ground};
    color: ${tokens.ink};
    font-family: "IBM Plex Mono", monospace;
    display: flex; flex-direction: column;
    justify-content: ${layout === "split" ? "space-between" : "center"};
    gap: ${layout === "split" ? 0 : CARD.pad}px;
    padding: ${CARD.pad}px;
    -webkit-font-smoothing: antialiased;
  }
  h1 {
    font-weight: 500;
    font-size: ${CARD.headline}px;
    line-height: 1.05;
    letter-spacing: -0.02em;
  }
  p {
    font-weight: 400;
    font-size: ${CARD.domain}px;
    color: ${tokens.muted};
    margin-top: ${CARD.domain}px;
  }
  .hollow { fill: none; stroke: ${tokens.muted}; stroke-width: 2; }
  .filled { fill: ${tokens.ripe}; }
</style>
<svg viewBox="${glyph.viewBox}" width="${glyphWidth}" height="${CARD.glyph}">
  ${glyph.circles.join("\n  ")}
</svg>
<div>
  <h1>${HEADLINE.join("<br>")}</h1>
  <p>${DOMAIN}</p>
</div>`;
}

async function launch() {
  if (!existsSync(CHROME)) {
    throw new Error(`no browser at ${CHROME} -- set CHROME to one.`);
  }

  // A throwaway profile, so this never touches the browser the human is
  // using and never inherits an extension that would paint on the card.
  const profile = mkdtempSync(join(tmpdir(), "ripen-og-"));
  const chrome = spawn(CHROME, [
    "--headless=new",
    "--remote-debugging-port=0",
    "--hide-scrollbars",
    "--no-first-run",
    `--user-data-dir=${profile}`,
    "about:blank",
  ]);
  chrome.on("exit", () => rmSync(profile, { recursive: true, force: true }));

  // The port is written to stderr on startup, and it is the only way to learn
  // it when the port was asked for as 0.
  const endpoint = await new Promise((resolve, reject) => {
    let buffer = "";
    chrome.stderr.on("data", (chunk) => {
      buffer += chunk;
      const found = buffer.match(/ws:\/\/[^\s]+/);
      if (found) resolve(found[0]);
    });
    chrome.on("exit", (code) => reject(new Error(`chrome exited ${code}`)));
  });

  chrome.socket = new WebSocket(endpoint);
  chrome.pending = new Map();
  chrome.nextId = 1;
  chrome.socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    const waiting = chrome.pending.get(message.id);
    if (!waiting) return;
    chrome.pending.delete(message.id);
    message.error ? waiting.reject(new Error(message.error.message)) : waiting.resolve(message.result);
  });
  await new Promise((resolve) => chrome.socket.addEventListener("open", resolve, { once: true }));
  return chrome;
}

function send(chrome, method, params = {}, sessionId) {
  const id = chrome.nextId++;
  return new Promise((resolve, reject) => {
    chrome.pending.set(id, { resolve, reject });
    chrome.socket.send(JSON.stringify({ id, method, params, sessionId }));
  });
}

async function shoot(chrome, html, { width, height }) {
  const { targetId } = await send(chrome, "Target.createTarget", { url: "about:blank" });
  const { sessionId } = await send(chrome, "Target.attachToTarget", { targetId, flatten: true });
  const call = (method, params) => send(chrome, method, params, sessionId);

  /*
   * The window is set explicitly rather than trusted: headless Chrome floors
   * its window at 500 CSS px on macOS, which is well under the card, and the
   * square one is taller than most screens. The override is the only size the
   * page ever sees.
   */
  await call("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await call("Page.enable");
  await call("Page.navigate", { url: `data:text/html;base64,${Buffer.from(html).toString("base64")}` });

  // The face is a data URI, so there is no network to wait on -- but the
  // layout still has to happen with it, and a screenshot taken before the
  // font is applied comes back set in the fallback.
  await call("Runtime.enable");
  await call("Runtime.evaluate", { expression: "document.fonts.ready", awaitPromise: true });

  const { data } = await call("Page.captureScreenshot", { format: "png", optimizeForSpeed: false });
  await send(chrome, "Target.closeTarget", { targetId });
  return Buffer.from(data, "base64");
}

await main();
