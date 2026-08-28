import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const SITE = fileURLToPath(new URL("..", import.meta.url));

const CHROME =
  process.env.CHROME ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

const CARD = {
  pad: 80,
  glyph: 36,
  headline: 104,
  domain: 38,
};

const HEADLINE = ["A digest ripens.", "You apply."];
const DOMAIN = "ripen.dev";

const SIZES = [
  { file: "og.png", width: 1200, height: 630, layout: "split" },
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

function glyphFrom(component) {
  const viewBox = component.match(/viewBox="([^"]+)"/)?.[1];
  const circles = [...component.matchAll(/<circle\s([^>]*?)\s*\/?>/g)].map(([, attrs]) => attrs);
  if (!viewBox || circles.length !== 2) throw new Error("Glyph.astro: cannot read the geometry");
  return { viewBox, circles: circles.map((attrs) => `<circle ${attrs} />`) };
}

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

  await call("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await call("Page.enable");
  await call("Page.navigate", { url: `data:text/html;base64,${Buffer.from(html).toString("base64")}` });

  await call("Runtime.enable");
  await call("Runtime.evaluate", { expression: "document.fonts.ready", awaitPromise: true });

  const { data } = await call("Page.captureScreenshot", { format: "png", optimizeForSpeed: false });
  await send(chrome, "Target.closeTarget", { targetId });
  return Buffer.from(data, "base64");
}

await main();
