// @ts-check
import { defineConfig } from "astro/config";

// Static output, no adapter: the build is a directory of files that Cloudflare
// Workers Static Assets serves. An adapter would put a server in the request
// path that nothing here needs.
//
// Starlight is installed but deliberately not registered yet. It owns /docs,
// and wiring it to the root docs/ collection is its own ticket (#118); an
// integration pointed at no content would only fail confusingly in between.
export default defineConfig({
  site: "https://ripen.dev",
  output: "static",
  integrations: [],
});
