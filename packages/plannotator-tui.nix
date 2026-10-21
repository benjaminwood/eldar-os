{ lib, rustPlatform, fetchFromGitHub }:

# plannotator-tui (https://github.com/plannotator/plannotator-tui), the review pane
# herdr-annotate opens, built from source for configurable keys (plannotator-tui#104,
# issue #58). Once a release includes them, herdr-annotate.nix goes back to the
# release binary and this file goes away.
rustPlatform.buildRustPackage {
  pname = "plannotator-tui";
  version = "0.9.4-keys";

  src = fetchFromGitHub {
    owner = "benjaminwood";
    repo = "plannotator-tui";
    rev = "af0c661002bc80ea2dd792dce6ec6b8426e99a1e";
    hash = "sha256-fnfqI+X4TvXRsm0APe9hPdsa/89scgV4Lu5Gzd1CPeQ=";
  };

  cargoHash = "sha256-EK3iCT+VJQ/1Sjhdt06nc1pUdo0B6tPBjbjSV9jsRKY=";

  # The workspace declares Rust 1.96; nixpkgs here has 1.95, which builds it.
  cargoBuildFlags = [ "--ignore-rust-version" "-p" "plannotator-tui" ];
  cargoTestFlags = [ "--ignore-rust-version" "-p" "plannotator-tui" ];
  # One test writes annotations under $HOME, which the sandbox doesn't have.
  preCheck = "export HOME=$TMPDIR";

  meta = {
    description = "Annotate Markdown in the terminal";
    homepage = "https://github.com/plannotator/plannotator-tui";
    license = lib.licenses.mit;
    mainProgram = "plannotator-tui";
  };
}
