{ stdenv, lib, fetchurl }:

# RWX Cloud CLI — fetched directly from the GitHub release as a single
# static binary. To bump: update `version` and `sha256`.
stdenv.mkDerivation rec {
  pname = "rwx-cli";
  version = "3.27.0";

  src = fetchurl {
    url = "https://github.com/rwx-cloud/rwx/releases/download/v${version}/rwx-linux-x86_64";
    sha256 = "03300562e6b2cd196d35fc5d408fc91f364278f8e36c710ae7021bd0fa3504da";
  };

  # The asset is the bare binary, not an archive.
  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    install -Dm755 $src $out/bin/rwx
    runHook postInstall
  '';

  meta = with lib; {
    description = "RWX Cloud CLI";
    homepage = "https://github.com/rwx-cloud/rwx";
    platforms = [ "x86_64-linux" ];
    mainProgram = "rwx";
  };
}
