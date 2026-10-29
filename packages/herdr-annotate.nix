{ stdenv, lib, fetchFromGitHub, fetchurl, autoPatchelfHook }:

# herdr-annotate: a herdr plugin to annotate terminal text and review agent
# replies (https://github.com/plannotator/herdr-annotate). `herdr plugin
# install` clones the repo and downloads two prebuilt binaries into it at
# install time; this builds the same plugin directory in the store, pinned,
# for `herdr plugin link`. The plugin keeps its state in herdr's per-plugin
# state dir, so a read-only root is fine.
# To bump: update `rev`, then the version from the repo's herdr-annotate.version,
# and the hashes (nix store prefetch-file; cross-check the release's SHA256SUMS).
let
  annotateVersion = "0.2.0"; # herdr-annotate.version
  tuiVersion = "0.9.9"; # plannotator-tui.version
  target = "x86_64-unknown-linux-gnu";
in
stdenv.mkDerivation {
  pname = "herdr-annotate";
  version = "0.9.1"; # herdr-plugin.toml

  src = fetchFromGitHub {
    owner = "plannotator";
    repo = "herdr-annotate";
    rev = "c5e565e9bdbb52d152ad15169f38ed8bae31b610";
    hash = "sha256-qh6vQ4e4ehIuY8bIV6H0uScO6AQySCLDYwHkI24csFQ=";
  };

  annotate = fetchurl {
    url = "https://github.com/plannotator/herdr-annotate/releases/download/rust-lite-v${annotateVersion}/herdr-annotate-${target}";
    hash = "sha256-o+9OcnhLuoz3nlgtYCHKNPDWAPFw2jCVvMYKh7G8uUI=";
  };

  tui = fetchurl {
    url = "https://github.com/plannotator/plannotator-tui/releases/download/v${tuiVersion}/plannotator-tui-${target}";
    hash = "sha256-BtQD4HQrWq8Wo0G6AQT4uFTx8K4TO3D2PRGICxSNThg=";
  };

  nativeBuildInputs = [ autoPatchelfHook ];
  buildInputs = [ stdenv.cc.cc.lib ];
  dontBuild = true;

  # The manifest's [[build]] steps are what downloads the binaries; link
  # doesn't run them, and they'd find them already here.
  installPhase = ''
    runHook preInstall
    mkdir -p $out
    cp -r herdr-plugin.toml scripts LICENSE $out/
    install -Dm755 $annotate $out/bin/herdr-annotate.exe
    install -Dm755 $tui $out/bin/plannotator-tui.exe
    echo ${annotateVersion} > $out/bin/herdr-annotate.version
    printf %s ${tuiVersion} > $out/bin/plannotator-tui.version
    runHook postInstall
  '';

  meta = {
    description = "Herdr plugin: annotate terminal text and review agent replies";
    homepage = "https://github.com/plannotator/herdr-annotate";
    license = lib.licenses.mit;
    platforms = [ "x86_64-linux" ];
  };
}
