# The Nix channel from ADR 0003: `nix run github:frankieramirez/ripen` builds
# Ripen from source. Everything the release ldflags stamp in is derived from
# the flake's own source info, with one exception recorded below.
{
  # `nix flake show` and `nix search` are catalog surfaces, so this and
  # `meta.description` both carry the wording SPEC.md fixes for them.
  description = "Ripen, a fail-closed image updater for self-hosted container stacks";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # The release matrix is linux and darwin on both arches (ADR 0003), so
      # the flake covers exactly those four and claims nothing else.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      perSystemPkgs = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # The one value a source build cannot derive. Flakes expose a revision
      # and a timestamp but never the tag they were fetched by, so this
      # constant is what `ripen version` reports and it has to move with the
      # annotated tag. The release workflow refuses a tag that disagrees with
      # it, the same way it refuses a tag with no changelog section.
      version = "0.1.0-rc.1";

      # Both fallbacks are the values internal/version already ships, so a
      # source with no git metadata reports exactly what a plain `go build`
      # would. `self.shortRev` is absent on a dirty tree and
      # `self.dirtyShortRev` is absent on a clean one; a `path:` source -- an
      # unpacked tarball, a vendored checkout -- has neither, and no
      # `lastModifiedDate` either. Falling back beats failing evaluation on a
      # build that would otherwise have worked.
      commit = self.shortRev or self.dirtyShortRev or "none";

      # `self.lastModifiedDate` is "YYYYMMDDhhmmss". GoReleaser stamps RFC 3339,
      # so match it rather than teaching operators two formats.
      stamp =
        let
          rfc3339 =
            d:
            let
              field = start: len: builtins.substring start len d;
            in
            "${field 0 4}-${field 4 2}-${field 6 2}T${field 8 2}:${field 10 2}:${field 12 2}Z";
        in
        if self ? lastModifiedDate then rfc3339 (builtins.toString self.lastModifiedDate) else "unknown";
    in
    {
      packages = perSystemPkgs (pkgs: rec {
        ripen = pkgs.buildGoModule {
          pname = "ripen";
          inherit version;

          src = self;

          # No in-repo `vendor/`: a vendor directory would silently switch
          # every other tool in the repo to `-mod=vendor`, and modernc.org/libc
          # alone would add tens of thousands of files to the tree. A stale
          # hash fails loudly and prints the one that would have worked.
          vendorHash = "sha256-1ujAxCfXoOSTVnC0DakbVK6dDRa8zcm6LU/1AOfTRow=";

          subPackages = [ "cmd/ripen" ];

          # buildGoModule derives its test scope from `subPackages`, so leaving
          # checks on would run `go test ./cmd/ripen` -- a main package with no
          # tests -- and read like the tree had been tested. CI runs
          # `go test -race ./...`; an install path does not need to.
          doCheck = false;

          ldflags = [
            "-s"
            "-w"
            "-X github.com/frankieramirez/ripen/internal/version.Version=${version}"
            "-X github.com/frankieramirez/ripen/internal/version.Commit=${commit}"
            "-X github.com/frankieramirez/ripen/internal/version.Date=${stamp}"
          ];

          # Matches the release matrix, which builds with CGO off so the
          # archives are static.
          env.CGO_ENABLED = 0;

          meta = {
            description = "Ripen, a fail-closed image updater for self-hosted container stacks";
            homepage = "https://github.com/frankieramirez/ripen";
            license = pkgs.lib.licenses.mit;
            mainProgram = "ripen";
          };
        };

        default = ripen;
      });

      devShells = perSystemPkgs (pkgs: {
        # The three commands AGENTS.md requires to be clean before a change.
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.golangci-lint
          ];
        };
      });

      # So `nix fmt` keeps this file in the style it was written in.
      formatter = perSystemPkgs (pkgs: pkgs.nixfmt-rfc-style);
    };
}
