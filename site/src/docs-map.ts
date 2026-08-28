export interface DocPage {
  /** The canonical file, relative to the repository root. */
  readonly source: string;
  /** Route id in the docs collection. The site route is `/${id}/`. */
  readonly id: string;
  /** Sidebar label. */
  readonly label: string;
  /** Meta description. There is no frontmatter to carry one. */
  readonly description: string;
  /** Overrides the page's H1 as the title. Only Vocabulary needs it. */
  readonly title?: string;
}

export const DOCS_MAP: readonly DocPage[] = [
  {
    source: "CONTEXT.md",
    id: "docs/vocabulary",
    label: "Vocabulary",
    title: "Vocabulary",
    description:
      "The ubiquitous language: what Ripen means by Transaction, Service, Baseline, Candidate, Proposal, and the rest.",
  },
  {
    source: "docs/configuration.md",
    id: "docs/configuration",
    label: "Configuration",
    description:
      "Every field of the policy file. Validation is fail-closed: a typo stops the process rather than disabling a safety rule.",
  },
  {
    source: "docs/portainer.md",
    id: "docs/portainer",
    label: "Portainer",
    description:
      "Driving Portainer through its HTTP API as a dedicated, limited user. No Docker socket, and no stack the policy does not name.",
  },
  {
    source: "docs/compose.md",
    id: "docs/compose",
    label: "Compose",
    description:
      "Driving Docker Compose and Podman Compose through the engine's own compose command. No daemon client and no socket mount.",
  },
  {
    source: "docs/agents.md",
    id: "docs/agents",
    label: "Agents",
    description:
      "The machine-facing surface: the CLI and one MCP server, the same guards behind both, and the write tools that are absent rather than refused.",
  },
  {
    source: "docs/proposals.md",
    id: "docs/proposals",
    label: "Proposals",
    description:
      "What Ripen does with a stack deployed from Git: it opens a pull request that pins the digest, and stops.",
  },
  {
    source: "docs/notifications.md",
    id: "docs/notifications",
    label: "Notifications",
    description:
      "One Event stream and two places it can go. The stderr stream is always on; the webhook Notifier is optional and filtered.",
  },
  {
    source: "docs/architecture.md",
    id: "docs/architecture",
    label: "Architecture",
    description:
      "How the binary is put together: the Transaction at the centre, the ports around it, and the adapters behind them.",
  },
  {
    source: "docs/troubleshooting.md",
    id: "docs/troubleshooting",
    label: "Troubleshooting",
    description:
      "Reading a refusal and knowing what to change. Start with status, explain, and audit.",
  },
];

/** Published pages by canonical source path. Used by the link rewriter. */
export const PAGE_BY_SOURCE = new Map(DOCS_MAP.map((page) => [page.source, page]));

/** Published pages by route id. Used by the loader. */
export const PAGE_BY_ID = new Map(DOCS_MAP.map((page) => [page.id, page]));

/** The site route for a published page, with the trailing slash Astro builds. */
export const routeFor = (page: DocPage): string => `/${page.id}/`;
