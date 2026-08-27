{
  description = "mow — minimal agentic harness";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = lib.genAttrs systems;
      version = lib.removeSuffix "\n" (builtins.readFile ./VERSION);
    in
    {
      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          mow = pkgs.buildGoModule {
            pname = "mow";
            inherit version;
            src = lib.cleanSource ./.;
            # First `nix build` will fail and print the real hash — paste it here.
            vendorHash = "sha256-1E3PywLh8UXMO9eAiIumGzoQ6MijuImOibkoFbZw9sk=";
            subPackages = [ "cmd/mow" ];
            env.CGO_ENABLED = "0";
            env.GOWORK = "off";
            ldflags = [
              "-s"
              "-w"
              "-X github.com/subosito/mow/internal/engine.Version=${version}"
            ];
            meta = {
              description = "Minimal, secure-by-default agentic harness (lean)";
              homepage = "https://github.com/subosito/mow";
              license = lib.licenses.mit;
              mainProgram = "mow";
            };
          };
          mowx = pkgs.buildGoModule {
            pname = "mowx";
            inherit version;
            src = lib.cleanSource ./.;
            vendorHash = "sha256-1E3PywLh8UXMO9eAiIumGzoQ6MijuImOibkoFbZw9sk=";
            subPackages = [ "cmd/mowx" ];
            env.CGO_ENABLED = "0";
            env.GOWORK = "off";
            ldflags = [
              "-s"
              "-w"
              "-X github.com/subosito/mow/internal/engine.Version=${version}"
            ];
            meta = {
              description = "mow with workflow packs (goal, ops, review, media, …)";
              homepage = "https://github.com/subosito/mow";
              license = lib.licenses.mit;
              mainProgram = "mowx";
            };
          };
        in
        {
          inherit mow mowx;
          default = mow;
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.mow}/bin/mow";
        };
      });

      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.just ];
          };
        });
    };
}
