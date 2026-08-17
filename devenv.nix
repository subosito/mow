{ pkgs, lib, ... }:

let
in
{
  # Go comes from nixpkgs; devenv sets GOTOOLCHAIN=local. Keep go.mod floor in sync
  # with the locked toolchain (same posture as sibling Go repos).
  languages.go.enable = true;

  packages = [ pkgs.just pkgs.openssl ];

  # just build → bin/mow stamped from VERSION; put it first so `mow` resolves.
  enterShell = ''
    export PATH="$DEVENV_ROOT/bin:$PATH"
  '';
}
