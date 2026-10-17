{ lib, buildGoModule, go_1_27 }:
# The supervisor (server, TUI and web UI in one binary): ../supervisor.
(buildGoModule.override { go = go_1_27; }) {
  pname = "supervisor";
  version = "0.1.0";
  src = lib.cleanSource ../supervisor;
  vendorHash = "sha256-O0kJx0qwRR/CrmTY9rDOtk51yeUtmC7oC6MyNSymPiA=";
  subPackages = [ "cmd/supervisor" ];
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" ];
  # Run every package's tests (the server's parity tests), not just the
  # built command's.
  checkPhase = ''
    runHook preCheck
    go test ./...
    runHook postCheck
  '';
  meta.mainProgram = "supervisor";
}
