# Body of eldar-os-apply (see default.nix). Pass --force to re-apply the
# current revision.
SRC=$STATE_DIR/src
SECRETS=$STATE_DIR/secrets
KEYS=$STATE_DIR/deploy-keys

log() { echo "eldar-os-apply: $*"; }

# A stalled connection must fail fast, not hang the apply: bounded connect,
# keepalives, an overall timeout, and one retry.
fetch() { # dir url key
  export GIT_SSH_COMMAND="ssh -i $3 -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=2"
  [ -d "$1/.git" ] || timeout 60 git clone -q --no-checkout "$2" "$1"
  timeout 30 git -C "$1" fetch -q origin main || { log "fetch of $2 failed, retrying"; timeout 30 git -C "$1" fetch -q origin main; }
  unset GIT_SSH_COMMAND
}

fetch "$SRC" "$REPO" "$KEYS/staging"
fetch "$SECRETS" "$SECRETS_REPO" "$KEYS/secrets"
rev=$(git -C "$SRC" rev-parse origin/main)
secrets_rev=$(git -C "$SECRETS" rev-parse origin/main)

if [ "${1:-}" != --force ] && [ "$(cat "$STATE_DIR/applied" 2>/dev/null)" = "$rev $secrets_rev" ]; then
  log "up to date at ${rev:0:12}"
  exit 0
fi

# Only a tip signed by an allowed key gets applied.
GNUPGHOME=$(mktemp -d)
export GNUPGHOME
trap 'rm -rf "$GNUPGHOME"' EXIT
gpg --quiet --import "$GPG_KEYS" 2>/dev/null
if ! git -C "$SRC" -c gpg.ssh.allowedSignersFile="$ALLOWED_SIGNERS" verify-commit "$rev"; then
  log "REFUSING ${rev:0:12}: not signed by an allowed key"
  exit 1
fi
log "${rev:0:12} is signed by an allowed key: $(git -C "$SRC" log -1 --format=%s "$rev")"

git -C "$SRC" checkout -q --force --detach "$rev"
git -C "$SECRETS" checkout -q --force --detach "$secrets_rev"

log "building"
new=$(nix --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths \
  "$SRC#nixosConfigurations.$FLAKE_ATTR.config.system.build.toplevel")
prev=$(readlink -f /nix/var/nix/profiles/system)

# If anything below dies before confirming health, reactivate the previous
# system. Boot default isn't touched until health is confirmed, so a reboot
# also lands on the previous system.
systemctl stop eldar-os-rollback.timer 2>/dev/null || true
systemd-run --quiet --collect --unit=eldar-os-rollback --on-active="$ROLLBACK_AFTER" \
  "$prev/bin/switch-to-configuration" test

rollback() {
  log "ROLLING BACK to $prev: $*"
  systemctl stop eldar-os-rollback.timer 2>/dev/null || true
  "$prev/bin/switch-to-configuration" test || true
  exit 1
}

failed_before=$(systemctl list-units --state=failed --plain --no-legend | cut -d' ' -f1 | sort)
log "activating $new"
"$new/bin/switch-to-configuration" test || rollback "activation reported errors"

# Let the jobs activation queued finish (ignoring our own start job), then check.
# shellcheck disable=SC2016  # expanded by the inner bash
timeout 120 bash -c 'while systemctl list-jobs --no-legend | grep -qv " eldar-os-apply.service "; do sleep 0.2; done' || true
failed_after=$(systemctl list-units --state=failed --plain --no-legend | cut -d' ' -f1 | sort)
new_failures=$(comm -13 <(echo "$failed_before") <(echo "$failed_after") | grep . || true)
[ -z "$new_failures" ] || rollback "units failed: $(echo "$new_failures" | tr '\n' ' ')"
"$HEALTH_CHECK" || rollback "health check failed"

systemctl stop eldar-os-rollback.timer 2>/dev/null || true
nix-env -p /nix/var/nix/profiles/system --set "$new"
"$new/bin/switch-to-configuration" boot
echo "$rev $secrets_rev" > "$STATE_DIR/applied"
log "applied ${rev:0:12} (secrets ${secrets_rev:0:12})"
