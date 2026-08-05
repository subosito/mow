{ pkgs, lib, ... }:

let
  # shell-use drives a real PTY and reports per-cell char/fg/bg, which is how
  # the mowi TUI smoke tests assert on what a terminal actually renders rather
  # than on ANSI substrings. Not in nixpkgs yet (released 2026-08), so pin the
  # upstream release by hash.
  shellUseVersion = "0.0.1-beta.6";

  shellUsePlatforms = {
    "x86_64-linux" = {
      target = "x86_64-unknown-linux-musl";
      sha256 = "12knniy379036bi4wrxzr54da68hh5vz7gwydpmy2fiw8ahwgimh";
    };
    "aarch64-linux" = {
      target = "aarch64-unknown-linux-musl";
      sha256 = "04qzas9hxgwxrkhm3f25vbz9sals3m3gb2867i4a6hxx4jk6mylb";
    };
    "x86_64-darwin" = {
      target = "x86_64-apple-darwin";
      sha256 = "1axyw2qxfiaaipwj48h600j4yhxqpw6142p077mk6w845g0cm0di";
    };
    "aarch64-darwin" = {
      target = "aarch64-apple-darwin";
      sha256 = "0i4nddj6rvvrx07h45f3hj757x2k49958bxyf427an4fvmy5r28l";
    };
  };

  plat = shellUsePlatforms.${pkgs.stdenv.hostPlatform.system} or null;

  shell-use = pkgs.stdenvNoCC.mkDerivation {
    pname = "shell-use";
    version = shellUseVersion;
    src = pkgs.fetchurl {
      url = "https://github.com/microsoft/shell-use/releases/download/v${shellUseVersion}/shell-use-${plat.target}.tar.gz";
      inherit (plat) sha256;
    };
    sourceRoot = ".";
    nativeBuildInputs = lib.optionals pkgs.stdenv.isLinux [ pkgs.autoPatchelfHook ];
    installPhase = ''
      runHook preInstall
      install -Dm755 shell-use $out/bin/shell-use
      runHook postInstall
    '';
    meta = {
      description = "Drive and assert on terminal apps (mowi TUI smoke tests)";
      homepage = "https://github.com/microsoft/shell-use";
      platforms = builtins.attrNames shellUsePlatforms;
    };
  };
in
{
  # Go comes from nixpkgs; devenv sets GOTOOLCHAIN=local. Keep go.mod floor in sync
  # with the locked toolchain (same posture as sibling Go repos).
  languages.go.enable = true;

  # shell-use is only added where a prebuilt release exists; the shell still
  # works everywhere else, and `just smoke-tui` reports it as missing.
  packages = [ pkgs.just pkgs.openssl ] ++ lib.optional (plat != null) shell-use;

  # just build → bin/mow; put it first so `mow` resolves after a local build.
  enterShell = ''
    export PATH="$DEVENV_ROOT/bin:$PATH"
  '';
}
