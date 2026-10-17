{ stdenv, lib, fetchurl }:

# Honeybadger CLI — fetched from upstream's GitHub release tarball rather
# than `go install`d. To bump the version: update `version` and replace
# `sha256` with the new hash (compute via `nix-prefetch-url $url` or
# `curl -sL $url | sha256sum`).
stdenv.mkDerivation rec {
  pname = "honeybadger-cli";
  version = "0.9.0";

  src = fetchurl {
    url = "https://github.com/honeybadger-io/cli/releases/download/v${version}/cli_Linux_x86_64.tar.gz";
    sha256 = "80925dc7aee13208e4eadb547d5ac60faee383d794dfd324b334a63697488eb5";
  };

  # Tarball extracts files directly with no wrapping subdirectory.
  sourceRoot = ".";

  installPhase = ''
    runHook preInstall
    install -Dm755 hb $out/bin/hb
    runHook postInstall
  '';

  meta = with lib; {
    description = "Honeybadger CLI";
    homepage = "https://github.com/honeybadger-io/cli";
    license = licenses.mit;
    platforms = [ "x86_64-linux" ];
    mainProgram = "hb";
  };
}
