{ stdenv, lib, fetchurl, unzip }:

# Composio CLI — the agent-oriented CLI from https://composio.dev/install.
# Not in nixpkgs; this tracks the GitHub release zip instead of running the
# upstream curl|bash installer (which drops an unmanaged tree in ~/.composio).
#
# To bump: set `version` to the release tag, then
#   nix hash file --type sha256 --sri composio-linux-x64.zip
# NOTE every CLI release so far is a PRERELEASE (`-beta.N`); there is no stable
# tag yet, so `version` must be pinned explicitly — you cannot follow "latest",
# which points at an unrelated package release with no CLI assets.
stdenv.mkDerivation rec {
  pname = "composio-cli";
  version = "0.2.33-beta.298";

  # The tag is "@composio/cli@<version>", URL-encoded in the download path.
  src = fetchurl {
    url = "https://github.com/ComposioHQ/composio/releases/download/%40composio%2Fcli%40${version}/composio-linux-x64.zip";
    hash = "sha256-w55jRs2N7jnILl5r8rv4DR9bhyItkLxb50OJFkVFkiA=";
  };

  nativeBuildInputs = [ unzip ];

  # The archive is a bundle: a 118M Bun-compiled `composio` plus sibling support
  # files (services/*.mjs, acp-adapters/, local-tools-binaries/). The binary finds
  # them via process.execPath, which resolves symlinks — so the whole tree goes in
  # libexec and $out/bin/composio is a symlink. Runtime state lives in ~/.composio,
  # so a read-only store path is fine.
  sourceRoot = "composio-linux-x64";

  installPhase = ''
    runHook preInstall

    # The bundle ships codex-acp for four platforms (~800M total). Keep only the
    # one this derivation can actually run; drops ~579M from the store path.
    rm -rf acp-adapters/codex/linux-arm64 \
           acp-adapters/codex/darwin-x64 \
           acp-adapters/codex/darwin-arm64

    mkdir -p $out/libexec/composio
    cp -r . $out/libexec/composio/
    chmod +x $out/libexec/composio/composio

    mkdir -p $out/bin
    ln -s $out/libexec/composio/composio $out/bin/composio

    runHook postInstall
  '';

  # DO NOT run autoPatchelfHook here. `composio` is a Bun single-file executable:
  # the compiled app is appended AFTER the ELF image. patchelf rewrites the headers
  # and drops that trailing payload — it silently truncated the binary from
  # 123,565,983 to 103,923,408 bytes, leaving a bare Bun runtime that answered
  # `--version` with Bun's own "1.3.10" and `whoami` with "run `bunx npm login`".
  # Verified 2026-07-26. The same applies to the vendored codex-acp binary.
  #
  # Consequence: these binaries keep their baked-in interpreter
  # (/lib64/ld-linux-x86-64.so.2) and link against the host glibc, exactly as the
  # upstream curl|bash installer leaves them. Fine in this Debian-based container;
  # it would need nix-ld or an FHS wrapper to run on NixOS proper.
  dontPatchELF = true;
  dontStrip = true;

  meta = with lib; {
    description = "Composio CLI — agent-oriented CLI for the Composio platform";
    homepage = "https://composio.dev";
    platforms = [ "x86_64-linux" ];
    mainProgram = "composio";
  };
}
