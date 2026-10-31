# Installing eldar-os

eldar-os lives on the Samsung 970 EVO 500GB (serial `S5H7NC0MB49704F`). It is
built and run as a VM on the old Ubuntu host (on the 1TB disk), then later
booted directly on the same machine. Every step below is a script in this
repo or declarative config; nothing is done by hand except where noted.

Never use `/dev/nvmeXn1` names for these disks: they have swapped between boots
before (the Ubuntu host's LUKS mapping is called `nvme0n1p3_crypt` but lives on
`nvme1n1`). Everything here goes by serial.

## Pinned inputs

| What | Pin |
|---|---|
| nixpkgs | `nixos-26.05` @ `26f4107e47e3` (flake.lock) |
| disko | `725ea35e410a` (flake.lock) |
| Installer ISO | `nixos-minimal-26.05.11985.26f4107e47e3`, URL + sha256 in `scripts/fetch-iso` |
| CI actions | by commit SHA in `.github/workflows/ci.yml` |
| Lock generated with | `nixos/nix@sha256:ff480dbb23511aa1c10710aa068ceea5b93fed9bfce5f5379f3a6b07282cbc7b` (host has no Nix) |

The ISO is built from the same nixpkgs commit as the lock, so the install
mostly reuses what's already on the ISO. Bump them together.

## Reproduced in CI

`scripts/e2e` runs steps 2–6 below unattended against the dry-run disk image
(see next section) and then `scripts/verify`. CI (`.github/workflows/ci.yml`)
runs it on every push and PR and nightly, on a stock Ubuntu runner after
`scripts/host-setup.sh --dry-run`. If CI is green, these docs work.

## Dry run

Every step below works against a throwaway disk instead of the real one: put
`--dry-run` right after `scripts/vm` (and `DRY_RUN=1` before `scripts/deploy`).
The dry-run VM gets a sparse image exactly the size of the 970 EVO
(`~/.local/share/eldar-os-vm/dry-run/disk.raw`), its own LUKS key disk,
firmware vars, systemd unit (`eldar-os-vm-dry`), SSH port (2223) and VNC display
(:1). The physical disk is never attached. To the installer it looks identical
(same virtio serial, same size), so `install.sh` runs unchanged.

```bash
scripts/e2e                       # the whole thing, unattended
scripts/vm --dry-run reset        # discard the image and start over
```

## 1. Host setup (once, as root)

```bash
sudo bash scripts/host-setup.sh            # with the real disk
sudo bash scripts/host-setup.sh --dry-run  # without it (CI)
```

Verifies the target disk by serial and size, then installs QEMU, OVMF, 7zip
and socat, adds a udev rule giving the user access to that one disk (by
serial), and grants the user KVM. Writes nothing to any disk.

## 2. Installer ISO

```bash
scripts/fetch-iso
```

## 3. LUKS key disk

```bash
scripts/vm keygen
```

Creates `~/.local/share/eldar-os-vm/luks-key.img`: a 1 MiB image whose first 64
bytes are a random hex key (the rest is zeros). The VM sees it read-only as
`/dev/disk/by-id/virtio-lukskey`; disko formats LUKS with it and the initrd
unlocks with it.

**Back it up** — without it or Ben's passphrase the disk is unreadable. The key
is plain text, so it fits in a password manager:

```bash
head -c 64 ~/.local/share/eldar-os-vm/luks-key.img; echo          # back up
truncate -s 1M luks-key.img                                        # restore
printf %s '<the 64 hex chars>' | dd of=luks-key.img conv=notrunc
```

It also works as a passphrase typed at the bare-metal prompt.

## 4. Boot the installer and get SSH

```bash
ssh-keygen -t ed25519 -N "" -f ~/.ssh/eldar-os-vm   # once: the host-side key for the VM
scripts/vm install
scripts/vm authorize
```

`install` doesn't go through the ISO's GRUB menu (graphical, awkward to drive
headless). It extracts the kernel, initrd and the first menu entry's command
line from the ISO and has QEMU boot them directly, still through the UEFI
firmware, with `console=ttyS0` added. The installer auto-logs in as `nixos` on
that serial console; `authorize` types the public key into
`~/.ssh/authorized_keys` there and waits until `VM_USER=nixos scripts/vm ssh`
works.

## 5. Install

```bash
git ls-files | tar -cf - -T - | VM_USER=nixos scripts/vm ssh 'mkdir -p eldar-os && tar -xf - -C eldar-os'
VM_USER=nixos scripts/vm ssh -t 'sudo bash eldar-os/scripts/install.sh'
```

`install.sh` checks it's in the installer VM with the right disk attached, runs
disko (destroy → format → mount) and `nixos-install` from the flake, and leaves
a copy of the config at `/etc/eldar-os`.

**Reinstalling an existing machine:** `install.sh --keep-luks` recreates the
filesystems inside the existing LUKS container instead of wiping the disk.
Every key slot survives (no one re-enters a passphrase), and so does the
machine's identity: SSH host keys (so it stays a secrets recipient), deploy
keys and the secrets checkout. No re-enrollment needed.

## 6. Boot and verify

```bash
scripts/vm stop           # the installer (a clean shutdown, so writes are flushed)
scripts/vm start
scripts/verify
```

`verify` checks the hostname, that the system is running with no failed units,
XFS on LUKS with reflinks, the fallback bootloader, secrets decrypting, and that
LUKS was unlocked by the key disk.

## 7. Ben's LUKS passphrase (Ben, interactively)

The disk only has the VM key until this runs. Needed before booting on bare metal.

```bash
scripts/vm ssh -t 'sudo cryptsetup luksAddKey /dev/disk/by-partlabel/disk-main-luks --key-file /dev/disk/by-id/virtio-lukskey --keyfile-size 64'
```

To see the bare-metal prompt without leaving the VM: `NO_KEY=1 scripts/vm start`
and look at VNC (`127.0.0.1:5900`).

## 8. Enroll: the machine applies its own config from here on

```bash
scripts/host-tools        # pinned sops + age on the admin host
scripts/enroll
```

The machine generates read-only deploy keys for the staging and secrets repos
(their public halves are registered via `gh`), and its age recipient (from its
SSH host key) must be in the secrets repo's `.sops.yaml`; `enroll` says so if
it isn't. It then clones the secrets repo on the machine and installs the
apply service with one last direct switch. See decisions 0004 and 0005.

The admin host needs an age key at `~/.config/sops/age/keys.txt`
(`age-keygen -o`, backed up in 1Password) and a commit-signing key whose
public half is in `trust/allowed_signers`.

## 9. GitHub: Eldar's App, the ruleset and webhooks

Eldar works on GitHub as its own GitHub App, which can open PRs but not
merge (decision 0011). GitHub only creates Apps in the browser, signed in as
the repo owner (Settings → Developer settings → GitHub Apps → New, i.e.
https://github.com/settings/apps/new):

| Setting | Value |
|---|---|
| Name | `eldar-os-agent-<owner>` (globally unique) |
| Homepage URL | the public repo |
| Webhook | off (uncheck Active) |
| OAuth during installation, Device Flow | off |
| Repository permissions | Contents: read and write; Pull requests: read and write; Actions, Checks, Commit statuses: read-only; Metadata: read-only (required). Nothing else: in particular not Workflows or Administration |
| Organization / account permissions, events | none |
| Where it can be installed | only on this account |

Then:

1. **Generate a private key** (it downloads a `.pem`). Put it in the secrets
   repo without displaying it, `jq -Rs . < key.pem | sops set --value-stdin
   eldar-os.yaml '["github-app-key"]'`, back it up to 1Password as a
   Document, and delete the download.
2. **Install App** → only select repositories → the staging repo. The
   installation ID is the number at the end of the page's URL.
3. Add to the sops secret `supervisor-env`: `SUPERVISOR_GITHUB_APP_ID`,
   `SUPERVISOR_GITHUB_INSTALLATION_ID` and `SUPERVISOR_GITHUB_REPOS` (the
   staging repo's name). Commit the secrets repo; the next apply picks it up.
4. Generate the apply webhook secret into sops (`openssl rand -hex 32 | tr -d
   '\n' | jq -Rs . | sops set --value-stdin eldar-os.yaml
   '["apply-webhook-secret"]'`) and back it up to 1Password.
5. `scripts/github-setup` applies the rest, idempotently: the ruleset on
   `main` (only admins update it), rebase-merge off, and both repos' push
   webhooks to the machine (decision 0012). `scripts/github-setup --check`
   reports drift without changing anything.

## 10. Tailnet nodes

The host, the supervisor's web UI and the apply webhook receiver each join
the tailnet as their own node. In the tailnet policy (download it first;
change only these entries):

- owners for `tag:eldar-os`, `tag:eldar-proxy` and `tag:eldar-os-hook`;
- `hosts` aliases for Ben's Mac and iPad, and two rules: the Mac and the
  iPad → `tag:eldar-os:22,8123` (Ben SSHes in, and the VM is a jump from
  there; 8123 is Home Assistant, `hosts/eldar-os/home-assistant.nix`) and
  `tag:eldar-os` → the Mac's port 22 (the supervisor's Mac commands), both TCP
  only;
- the `funnel` node attribute for `tag:eldar-os-hook` (public HTTPS; no ACL
  rules, so no tailnet access);
- `tests` that pin these paths, so a later edit can't quietly widen them.

Eldar's VM can't use the host's connection: the host firewall rejects VM
traffic to the tailnet range, and a health check enforces it. Then give each
node a single-use auth key for its tag:

| Node | Tag | Auth key goes in |
|---|---|---|
| host (`tailscaled`) | `tag:eldar-os` | `/var/lib/tailscale/authkey` |
| web UI (`supervisor-web`) | `tag:eldar-proxy` | `/var/lib/supervisor-web/authkey` |
| webhooks (`eldar-os-apply-hook`) | `tag:eldar-os-hook` | `/var/lib/eldar-os-hook/authkey` |
| the VM's Chrome stream (`tailnet-proxy-chrome`) | `tag:eldar-chrome` | `/var/lib/tailnet-proxy-chrome/authkey` (owner `tailnet-proxy`) |

Restart the service (for the host, apply); it registers and deletes the key.

## 11. Home Assistant's state (once, from the previous OS)

`hosts/eldar-os/home-assistant.nix` runs Home Assistant against
`/var/lib/homeassistant`, which starts out empty. Copy the old install's config
directory there before its first start, or it starts a fresh setup. On this
machine it came from the previous Ubuntu install's Docker container, still
readable at `/mnt/ubuntu` (`hosts/eldar-os/ubuntu-disk.nix`):

```bash
sudo systemctl stop docker-homeassistant   # if it's already running
sudo rsync -aHAX /mnt/ubuntu/home/ben/.config/homeassistant/ /var/lib/homeassistant/
sudo chmod 700 /var/lib/homeassistant
sudo systemctl start docker-homeassistant
```

Keep the image tag at the version that wrote the state (`.HA_VERSION` in it)
for the first start, then upgrade by changing the tag.

## 12. Atuin's database (once, from the previous OS)

`hosts/eldar-os/atuin.nix` runs Atuin's sync server against Postgres data in
`/var/lib/atuin/postgres` and reads its database URL from `/var/lib/atuin/env`.
On this machine both came from the previous Ubuntu install's Docker Compose
project (a `postgres:14` volume, and the database credentials in its `.env`).
Same Postgres major version, so the data directory is copied as is:

```bash
sudo systemctl stop docker-atuin docker-atuin-postgres   # if already running
sudo rsync -aHAX --numeric-ids /mnt/ubuntu/var/lib/docker/volumes/atuin_database/_data/ /var/lib/atuin/postgres/
sudo sh -c 'set -a; . /mnt/ubuntu/home/ben/projects/atuinsh/atuin/.env; umask 077
  printf "ATUIN_DB_URI=postgres://%s:%s@127.0.0.1:5432/atuin\n" "$ATUIN_DB_USERNAME" "$ATUIN_DB_PASSWORD" > /var/lib/atuin/env'
sudo systemctl start docker-atuin-postgres docker-atuin
curl -s http://127.0.0.1:8888/    # version and total_history
```

Its tailnet node (`atuin`) needs a single-use auth key for its tag in
`/var/lib/tailnet-proxy-atuin/authkey`, as in step 10.

Before changing either image, dump the database (the data directory itself
can't be downgraded once a newer Atuin migrates it):

```bash
U=$(sudo sh -c '. /var/lib/atuin/env; echo "$ATUIN_DB_URI"' | sed -E 's#^postgres://([^:]+):.*#\1#')
sudo docker exec atuin-postgres pg_dump -U "$U" -Fc atuin | sudo tee /var/lib/atuin/atuin-$(date +%F).dump >/dev/null
```

## Changing the system

```bash
scripts/try               # activate the working tree now, uncommitted (~7 s)
git commit                # signed
scripts/apply             # push; the machine verifies, applies, health-checks (~11 s)
```

## Booting on bare metal

The bootloader is at the firmware's fallback path (`\EFI\BOOT\BOOTX64.EFI`) on
the 500GB disk's own ESP; no NVRAM entry is written. Pick the 970 EVO 500GB
from the firmware boot menu. There's no key disk there, so after 5 s the initrd
asks for Ben's passphrase.

## The data disk

The 1TB drive held the previous Ubuntu install, which steps 11 and 12 read
from (it was mounted read-only at `/mnt/ubuntu` then). Once everything worth
keeping was copied off, it became the data disk (`hosts/eldar-os/data-disk.nix`):
one LUKS2 partition around XFS, mounted at `/data`, unlocked at boot by the
sops secret `data-disk-key`. Formatted once, by serial, from the running
system (the key is generated into RAM, never printed):

```bash
T=/dev/disk/by-id/nvme-Samsung_SSD_970_EVO_1TB_S5H9NG0N204821J
K=/run/user/$UID/datadisk/key
install -d -m 700 ${K%/*}; (umask 077; tr -dc A-Za-z0-9 </dev/urandom | head -c 64 >$K)
sudo wipefs -a $T-part*; sudo wipefs -a $T; sudo blkdiscard -f $T
sudo sgdisk --zap-all $T; sudo sgdisk -n1:0:0 -t1:8309 -c1:eldar-data $T
sudo cryptsetup luksFormat --batch-mode --type luks2 --label eldar-data --key-file $K $T-part1
sudo cryptsetup open --allow-discards --key-file $K $T-part1 data
sudo mkfs.xfs -L eldar-data /dev/mapper/data
# in eldar-os-secrets: store it, then back it up to the admin's 1Password vault
jq -Rs . <$K | sops set --value-stdin eldar-os.yaml '["data-disk-key"]'
```

In the VM the drive is absent and the mount is skipped.
