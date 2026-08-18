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
A human-reset halt that opens after a failed Transaction. While open, Ripen takes no outbound action: Apply mode cannot mutate and no Proposal is opened. Monitor-mode observation and every read continue.
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

**Web UI**:
The optional, read-only, off-by-default browser view of what Ripen already knows. Embedded in the binary, served by the daemon. Never a path to Apply mode, Circuit breaker clearing, or policy edits.
_Avoid_: dashboard, operator UI, frontend

**Actor**:
Which surface initiated a write — the human CLI, the daemon, or the Agent surface. Recorded on the attempt and on the Event. Always determined by the surface that ran the code; a caller can never declare its own.
_Avoid_: user, caller, origin

**Event**:
One record of something Ripen observed or did. Emitted once, to a single stream that every sink reads. The stream always records every Event; who gets paged is a sink's decision, not the Event's.
_Avoid_: message, notification, log line

**Notifier**:
The configured outbound sink that pages a human with a subset of Events. Off unless configured. Distinct from the Event stream, which always records everything and pages no one.
_Avoid_: alert, log, stderr JSON

**Event envelope**:
The versioned wrapper around one Event on its way to a sink. Versioned independently of the Response envelope, because Events and reads change at different rates.
_Avoid_: envelope, payload

**Response envelope**:
The versioned wrapper around every Agent surface answer, identical whether it came from the CLI or from MCP. Versioned independently of the Event envelope.
_Avoid_: envelope, output, response
