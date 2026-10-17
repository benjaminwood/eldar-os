{ stdenv, lib, fetchurl }:

# Herdr — terminal workspace manager for AI coding agents (Rust).
# Fetched from the GitHub release as a static-pie binary. nixpkgs nixos-25.05
# has no herdr (unstable lags behind upstream), so this tracks releases directly.
# Herdr detects /nix/store installs and disables its own `herdr update`.
# To bump: update `version` and `sha256` (nix-prefetch-url the binary URL);
# cross-check against the sha256 in https://herdr.dev/latest.json.
stdenv.mkDerivation rec {
  pname = "herdr";
  version = "0.9.3";

  src = fetchurl {
    url = "https://github.com/herdrdev/herdr/releases/download/v${version}/herdr-linux-x86_64";
    sha256 = "19yvyj3l0gqrisknzx3cy3313nfgl7g5chrlhic4iyn2y5jxra0q";
  };

  # Bare binary, no archive.
  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 $src $out/bin/herdr
    runHook postInstall
  '';

  meta = with lib; {
    description = "Terminal workspace manager for AI coding agents";
    homepage = "https://herdr.dev";
    license = licenses.asl20;
    platforms = [ "x86_64-linux" ];
    mainProgram = "herdr";
  };
}
