# Ripen

A fail-closed image updater for Portainer and Compose. A digest ripens. You apply. This glossary is the ubiquitous language for that product.

## Language

**Ripen**:
The product. Orchestrator-neutral name. Launch talks to Portainer and to a Compose runtime (Docker Compose or Podman Compose).
_Avoid_: NAS Stack Updater, nas-stack-updater, Daymark, Plimsoll

**Transaction**:
The fail-closed unit that observes a Service, optionally changes that one Service, verifies every configured sibling, and either accepts the new digest or rolls back and opens the Circuit breaker.
_Avoid_: job, update cycle, deployment

**Service**:
One named Compose service inside a Stack. A Transaction mutates at most one.
_Avoid_: container, app, unit

**Stack**:
A Compose application the updater is authorized to see, through Portainer or a Compose runtime.
_Avoid_: application

**Compose runtime**:
Docker Compose or Podman Compose as the control plane. Stacks are declared as compose-file paths in policy. Distinct from Portainer.
_Avoid_: Docker socket backend, engine adapter, Quadlet

**Privileged socket**:
The host engine's full API, usually `/var/run/docker.sock`. Out of scope. A rootless user socket is a narrower, opt-in connection, not this.
_Avoid_: docker.sock, Watchtower-style access

**Candidate**:
A remote image digest observed for a Service that is not yet the accepted Baseline.
_Avoid_: available update, pending version

**Baseline**:
The last accepted, health-proven digest for a Service.
_Avoid_: current image, running tag

**Circuit breaker**:
A human-reset halt that opens after a failed Transaction. While open, Apply mode cannot mutate.
_Avoid_: lock, pause, alarm

**Proposal**:
A Git digest-pin pull request the updater creates and never merges. Merge and deploy stay outside the updater.
_Avoid_: auto-PR, GitOps sync, update PR

**Monitor mode**:
Observation-only operation. Records Baselines and Candidates. Never mutates a Stack.
_Avoid_: dry-run, preview

**Apply mode**:
The extra-gated mode that may mutate one Service or open a Proposal.
_Avoid_: auto-update, unattended mode

**Agent surface**:
The machine-facing interface — versioned JSON CLI plus MCP server — an agent uses to observe and prepare. It is not a path to Apply mode or Circuit breaker clearing. Scopes agents _operating_ Ripen. Agents working on Ripen's own codebase are a different audience served by different files; that scaffolding is not the Agent surface.
_Avoid_: AI integration, copilot, chatbot, AGENTS.md

**Notifier**:
An outbound page to a human about an observed event. Distinct from stderr JSON logs.
_Avoid_: alert, log
