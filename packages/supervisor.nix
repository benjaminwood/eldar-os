{ lib, buildGoModule, go_1_27 }:
# The supervisor (server, TUI and web UI in one binary) and apply-hook
# (webhooks that start an apply): ../supervisor.
(buildGoModule.override { go = go_1_27; }) {
  pname = "supervisor";
  version = "0.1.0";
  src = lib.cleanSource ../supervisor;
  vendorHash = "sha256-O0kJx0qwRR/CrmTY9rDOtk51yeUtmC7oC6MyNSymPiA=";
  subPackages = [ "cmd/supervisor" "cmd/apply-hook" "cmd/tailnet-proxy" ];
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" ];
  # No tests in the build: they add about a minute to every rebuild of the
  # machine. CI runs them all (`scripts/go test ./...`); locally, run the ones
  # for what changed.
  doCheck = false;
  meta.mainProgram = "supervisor";
}
