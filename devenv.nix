{ pkgs, lib, ... }:

let
in
{
  # Go comes from nixpkgs; devenv sets GOTOOLCHAIN=local. Keep go.mod floor in sync
  # with the locked toolchain (same posture as sibling Go repos).
  languages.go.enable = true;
  # nixpkgs still ships 1.26.8; 1.26.9 carries the standard-library security
  # fixes govulncheck requires. Drop this override once nixpkgs has it.
  languages.go.package = pkgs.go.overrideAttrs (old: rec {
    version = "1.26.9";
    src = pkgs.fetchurl {
      url = "https://go.dev/dl/go${version}.src.tar.gz";
      hash = "sha256-lzXX3Ntls10/pXfwQGRzfAO4nPGitx5uaf4vPG+f1Mo=";
    };
  });

  packages = [ pkgs.just pkgs.openssl pkgs.ripgrep pkgs.fd ];

  # just build → bin/mow stamped from VERSION; put it first so `mow` resolves.
  enterShell = ''
    export PATH="$DEVENV_ROOT/bin:$PATH"
  '';
}
