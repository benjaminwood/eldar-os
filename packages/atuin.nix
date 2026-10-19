{ stdenv, lib, fetchurl }:

# Atuin client — the static (musl) build from the GitHub release. nixpkgs
# trails upstream (18.15.2 when this was added), and clients older than 18.18
# can't read history records written by newer ones ("unknown history version
# v2"): one current client anywhere stops older ones from syncing. Keep this at
# or above the newest client on Ben's other machines, and the server
# (hosts/eldar-os/atuin.nix) at or above this.
# To bump: update `version` and `sha256` (from the release's
# atuin-x86_64-unknown-linux-musl.tar.gz.sha256).
stdenv.mkDerivation rec {
  pname = "atuin";
  version = "18.24.1";

  src = fetchurl {
    url = "https://github.com/atuinsh/atuin/releases/download/v${version}/atuin-x86_64-unknown-linux-musl.tar.gz";
    sha256 = "fe03f652d6545a9d8795b3c2abb84bcddfe831e4b7effb0c2d191a0d70e498a0";
  };

  installPhase = ''
    runHook preInstall
    install -Dm755 atuin $out/bin/atuin
    runHook postInstall
  '';

  meta = with lib; {
    description = "Shell history sync, search and backup";
    homepage = "https://atuin.sh";
    license = licenses.mit;
    platforms = [ "x86_64-linux" ];
    mainProgram = "atuin";
  };
}
