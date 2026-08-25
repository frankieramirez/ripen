import { defineCollection } from "astro:content";
import { docsSchema } from "@astrojs/starlight/schema";

import { repoDocsLoader } from "./loaders/repo-docs";

// One collection, and it is not in this directory. The docs are the root
// docs/ files, read where ADR 0004 keeps them; the loader is what makes a
// directory of frontmatter-free markdown look like a Starlight collection.
export const collections = {
  docs: defineCollection({ loader: repoDocsLoader(), schema: docsSchema() }),
};
