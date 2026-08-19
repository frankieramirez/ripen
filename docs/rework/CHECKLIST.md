# Pre-public checklist

The ordered gate list from the [Public relaunch map](https://github.com/frankieramirez/ripen/issues/6). Work top to bottom; each phase gates the next. The companion [rework spec](./SPEC.md) holds the design detail.

## Phase 0 — already done

- [x] Repo renamed to `github.com/frankieramirez/ripen`; old-name redirect live; **never** create a placeholder repo at the old name — it destroys the redirect ([#19](https://github.com/frankieramirez/ripen/issues/19)).
- [x] Git history secrets audit: clean, keep history, no rewrite ([#13](https://github.com/frankieramirez/ripen/issues/13)).
- [x] Issue-tracker publication decided: the planning history (map, tickets, transcripts) goes public with the repo as-is — deliberate call, 2026-08-18.
- [x] No early name claims needed: GHCR/Docker Hub namespaces already owned; PyPI collision mitigated by naming discipline ("Ripen, a fail-closed image updater" in every registry description) ([#19](https://github.com/frankieramirez/ripen/issues/19)).

## Phase 1 — repo housekeeping (before the rework PRs start)

- [x] Merge the outstanding local branches into `main` and push. The four `context/*` branches turned out to be one stack fully contained in this spec's branch, so this was a single rebase onto `main`, not five merges. The Python rename needed no merge: it was already on `main` as the squash-merge [#20](https://github.com/frankieramirez/ripen/pull/20), and the duplicate local commit `0f572c6` dropped during the rebase. `main` is now the single integration line.
- [x] Author `docs/adr/0001-rewrite-in-go.md` (source: [#9](https://github.com/frankieramirez/ripen/issues/9)) and [`docs/adr/0002-release-shape.md`](../adr/0002-release-shape.md) (source: [#12](https://github.com/frankieramirez/ripen/issues/12)).

## Phase 2 — the Go rework

- [x] PRs 1–13 per the [spec's migration plan](./SPEC.md#migration-plan), each claiming its behavior-inventory rows. Verification was local (`go test -race ./...`, `golangci-lint run ./...`, `goreleaser check`) because GitHub Actions is disabled on the account; the workflows are in place and run on the first billing-enabled push.
- [x] Behavior inventory fully claimed (every row checked against a Go test or struck with a reason) — completed with the Event stream PR; every row names its Go test in [`SPEC.md`](./SPEC.md#behavior-inventory).
- [x] The ten explicit invariants each have a dedicated test — each one names its Go test in [`SPEC.md`](./SPEC.md#invariants-to-test-explicitly).
- [x] PR 13 delivers the docs surface: rewritten README, eight `docs/` pages (`configuration`, `portainer`, `compose`, `agents`, `proposals`, `notifications`, `architecture`, `troubleshooting`), `AGENTS.md` + `CLAUDE.md`, `ROADMAP.md` (non-goals seeded from the map's Out of scope), `CONTRIBUTING.md`, rewritten `SECURITY.md`, three YAML issue forms + `config.yml`, PR template, `CHANGELOG.md` seeded, and a rewritten `config.example.yaml` ([#14](https://github.com/frankieramirez/ripen/issues/14)). **`CODE_OF_CONDUCT.md` is the one piece not written** — it is blocked on the item below.
- [ ] Provision the Code of Conduct contact alias **before** `CODE_OF_CONDUCT.md` is written ([#14](https://github.com/frankieramirez/ripen/issues/14)).

## Phase 3 — release plumbing (still private)

- [ ] Create the public `frankieramirez/homebrew-tap` repo when GoReleaser is wired ([#12](https://github.com/frankieramirez/ripen/issues/12), [#19](https://github.com/frankieramirez/ripen/issues/19)).
- [ ] GoReleaser config: linux/amd64+arm64 `FROM scratch` images to GHCR + Docker Hub (tags `vX.Y.Z`, `X.Y.Z`, `vX.Y`, `vX`, `latest`), linux+darwin binaries, checksums, Syft SBOM, keyless cosign, `attest-build-provenance`, CHANGELOG.md as release body ([#12](https://github.com/frankieramirez/ripen/issues/12)).
- [ ] `flake.nix` in-repo builds from source ([#12](https://github.com/frankieramirez/ripen/issues/12)).
- [ ] Dry-run: `goreleaser release --snapshot` produces the full artifact set locally.

## Phase 4 — Python removal

- [ ] Behavior inventory confirmed fully claimed (gate for this PR).
- [ ] One PR removes `src/ripen/*.py`, `tests/*.py`, `pyproject.toml`, the Python Dockerfile bits, and any Python-era compose/config examples superseded by the Go docs.

## Phase 5 — live NAS cutover (final validation gate)

Full runbook in [Plan the rework migration](https://github.com/frankieramirez/ripen/issues/16). Summary order:

- [ ] Build `ripen:v1.0.0-rc` locally on the NAS (GHCR still private).
- [ ] Create `/volume2/docker/ripen/{config,data,secrets}`; **copy** (not move) the two secret files; hand-write the new `policy.yaml`.
- [ ] Stop Portainer stack 150 (do not delete yet — it is the rollback).
- [ ] Create Portainer stack `ripen` from `stacks/ripen/compose.yaml`.
- [ ] Merge the single atomic `nas-infrastructure` PR (compose, `fleet.json` incl. new stack `live:` path and the three network membership lists, `operations.md`, `secrets.md`, `recovery-order.md`).
- [ ] **48-hour Monitor soak**: Baseline seeded for every Service, ≥1 Candidate observed, zero error-level Events, healthcheck green throughout.
- [ ] Soak passed → switch to Apply mode → delete stack 150 → tar `/volume2/docker/nas-stack-updater/` (archives the old state DB) to backup and remove it.

## Phase 6 — the flip

- [ ] Enable the OpenSSF Scorecard GitHub Action (the badge is dead without it) ([#14](https://github.com/frankieramirez/ripen/issues/14)).
- [ ] Set repo description to the hero line, ~13 topics per [#14](https://github.com/frankieramirez/ripen/issues/14), optional social preview card.
- [ ] Repo settings: Discussions **off**, blank issues off (via `config.yml`), private vulnerability reporting on.
- [ ] Flip repo visibility to public. (Not gated on a real Apply Transaction — the clean soak is the gate.)
- [ ] Tag annotated `v1.0.0` on `main`; verify the release workflow publishes binaries, both registries, brew formula, signatures/SBOM/provenance.
- [ ] Verify `go install github.com/frankieramirez/ripen/cmd/ripen@v1.0.0`, `brew install frankieramirez/tap/ripen`, `nix run github:frankieramirez/ripen`, and both registry pulls.

## Phase 7 — post-flip follow-ups

- [ ] Repoint the NAS compose file at the published GHCR image (small `nas-infrastructure` follow-up).
- [ ] Open the Docker MCP Catalog listing PR into docker/mcp-registry — read-only container form only, description "Ripen, a fail-closed image updater" ([#12](https://github.com/frankieramirez/ripen/issues/12), [#17](https://github.com/frankieramirez/ripen/issues/17)).
- [ ] Post-launch (no deadline): nixpkgs submission, homebrew-core consideration ([#12](https://github.com/frankieramirez/ripen/issues/12)).
