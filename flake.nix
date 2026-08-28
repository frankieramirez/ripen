{
  description = "Ripen, a fail-closed image updater for self-hosted container stacks";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      perSystemPkgs = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      version = "1.0.0";

      commit = self.shortRev or self.dirtyShortRev or "none";

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

          vendorHash = "sha256-1ujAxCfXoOSTVnC0DakbVK6dDRa8zcm6LU/1AOfTRow=";

          subPackages = [ "cmd/ripen" ];

          doCheck = false;

          ldflags = [
            "-s"
            "-w"
            "-X github.com/frankieramirez/ripen/internal/version.Version=${version}"
            "-X github.com/frankieramirez/ripen/internal/version.Commit=${commit}"
            "-X github.com/frankieramirez/ripen/internal/version.Date=${stamp}"
          ];

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
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.golangci-lint
          ];
        };
      });

      formatter = perSystemPkgs (pkgs: pkgs.nixfmt-rfc-style);
    };
}
