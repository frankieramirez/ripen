# Release credentials

The release pipeline holds exactly one long-lived secret: `DOCKERHUB_TOKEN`.
Everything else it needs is minted for the run and expires with it — GHCR uses
the workflow's own `GITHUB_TOKEN`, and cosign signing and build provenance are
keyless through GitHub OIDC ([ADR 0003](adr/0003-distribution-channels-at-v1.md)).

This page is for whoever holds that token. It is not needed to build or run
Ripen.

## What `.github/workflows/release.yaml` reads

| Secret | Read by | Kind |
| --- | --- | --- |
| `DOCKERHUB_USERNAME` | the Docker Hub login step | Docker Hub account name. Not sensitive, but the workflow reads it from `secrets.`, so it has to be set as one. |
| `DOCKERHUB_TOKEN` | the Docker Hub login step | Docker Hub personal access token. The only long-lived credential in the pipeline. |
| `GITHUB_TOKEN` | GHCR login, the Release, provenance | Minted per run by Actions. Nothing to provision. |

## Provisioning

`.github/release/provision-dockerhub.sh` is the procedure. It walks you through
creating `docker.io/<account>/ripen` **private**, generating the token, proving
the token can push and that the repository really is private, and writing both
secrets. It refuses to push into a public repository, because a push to a
repository that does not exist creates one, and Docker Hub creates repositories
public — which would put the image and its description into public search while
the GitHub repo is still private.

```bash
.github/release/provision-dockerhub.sh
```

Two things it does that are worth knowing before you start. It pushes a
throwaway `provision-check` tag into the repository to prove the token can
write, and asks you to delete that tag by hand, because a Read & Write token
cannot. And it runs `docker logout` at the end, which ends whatever Docker Hub
session the machine already had.

It was run on 2026-08-19. `docker.io/frankieramirez/ripen` exists, private, with
the description below; the token is Read & Write with a 365-day expiry; both
secrets are set. Until a rotation, there is nothing here left to do.

The Docker Hub repository's description is `Ripen, a fail-closed image updater`,
never a bare `ripen`. That is the naming discipline from
[#19](https://github.com/frankieramirez/ripen/issues/19): a bare name collides
with an unrelated PyPI package and gets lost in Docker Hub search.

The repository flips to public at the same moment the GitHub repo does, in
[Phase 6](rework/CHECKLIST.md#phase-6--the-flip). Not before.

## Where the token lives

In one place: the `DOCKERHUB_TOKEN` Actions secret on
`github.com/frankieramirez/ripen`.

Not in a password manager, not in a `.env`, and not in the local Docker
credential store. GitHub secrets are write-only, so nobody can read it back out, including the
person who set it. That is fine: a token nobody can read is not a token anybody
has to recover. If it is ever lost or in doubt, it is replaced, and replacing it
is a five-minute job. A second copy would only be a second thing to leak.

## What the token can actually do

It can write to **every** repository the Docker Hub account owns, not just
`ripen`.

[#51](https://github.com/frankieramirez/ripen/issues/51) asked for a token
"scoped to read/write for this repository only — not account-wide, not delete",
and only half of that is purchasable. Scoping a token to one repository is a
Docker Pro/Team feature, so on a Personal plan account-wide is the floor. The
permission *level* is still a choice, so this token is **Read & Write**, without
Delete.

That is not taken on trust. The wizard pushes a `provision-check` tag and then
tries to remove it through the API; on the 2026-08-19 run the delete was
refused, which is the proof that the token cannot delete. The tag came off by
hand.

What holds the blast radius down instead of scope:

- It expires. 365 days, forcing a yearly rotation.
- One consumer. Nothing else on any machine uses this token, and the wizard
  logs Docker out afterwards so it is not left in the local credential store.
- What it can reach is a mirror, not the record. GHCR, the GitHub Release, the
  signed checksums and the provenance attestation all live elsewhere, so a
  stolen token can publish a bad Docker Hub tag but cannot forge a release or
  make a good one disappear.
- Docker Hub OIDC would remove the token entirely, but it needs an organization
  on a paid plan or the Docker Sponsored Open Source program, and DSOS needs a
  public repo. Worth revisiting after the flip.

## Rotation

Yearly, before the expiry. Docker Hub's
[token page](https://app.docker.com/settings/personal-access-tokens) shows the
exact expiry date, and that is the only record of it — there is deliberately no
second copy to fall out of date.

Re-run the wizard. It spots the existing secrets and says so, asks whether the
repository is already there (it is — answer yes, and it skips creating one), and
overwrites the secret with the new token:

```bash
.github/release/provision-dockerhub.sh
```

The order is: generate the new token, prove it pushes, overwrite the secret,
**then** delete the old one. Never delete first by choice. The old value cannot
be read back out of GitHub, so a rotation that fails after the old token is gone
leaves the pipeline with nothing.

Docker Hub may refuse to issue a second token while the first exists — the limit
is a "fair use" policy rather than a documented number, and a Personal account
has historically been held to one. If it refuses, delete the old token first and
accept the gap. The only thing the gap breaks is a `v*` tag pushed during it, and
that fails at login before anything is published.

## Revocation

If the token is suspected of being exposed, delete it at
[app.docker.com/settings/personal-access-tokens](https://app.docker.com/settings/personal-access-tokens)
before anything else, then rotate. Deleting it does not break anything that is
running; it breaks the next `v*` tag, which is the point.

## When it expires or is revoked

Nothing fails until someone pushes a `v*` tag. Then the Docker Hub login step
fails, and it fails before GoReleaser is invoked — so no binaries, no Release,
no GHCR images, nothing half-published. Fix the token and push the tag again.

The cost of that shape is that an expired token is invisible until a release
tries to happen. There is no monitor for it, and adding one would mean another
credential to watch the credential.
