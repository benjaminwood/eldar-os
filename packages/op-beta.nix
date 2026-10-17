{ lib, stdenvNoCC, fetchurl, unzip }:
# The 1Password CLI's beta channel: `op environment` (1Password Environments),
# which the supervisor reads credentials with, isn't in a stable release yet.
# To bump: set `version`, then sha256sum the zip.
stdenvNoCC.mkDerivation rec {
  pname = "op";
  version = "2.42.0-beta.01";
  src = fetchurl {
    url = "https://cache.agilebits.com/dist/1P/op2/pkg/v${version}/op_linux_amd64_v${version}.zip";
    sha256 = "7ffccf20cb626478ee3cbf4480000c13069742a462eeb931baf94b3bd018e85e";
  };
  nativeBuildInputs = [ unzip ];
  unpackPhase = "unzip $src op";
  installPhase = "install -Dm755 op $out/bin/op";
  meta = {
    description = "1Password CLI (beta channel)";
    license = lib.licenses.unfree;
    platforms = [ "x86_64-linux" ];
    mainProgram = "op";
  };
}
