{ pkgs, ... }:
{
  networking.hostName = "eldar-os";
  time.timeZone = "America/Los_Angeles";
  i18n.defaultLocale = "en_US.UTF-8";

  boot.loader.systemd-boot.enable = true;
  boot.loader.systemd-boot.configurationLimit = 20;
  # No NVRAM boot entries: from the VM they'd land in QEMU's virtual NVRAM, not
  # the motherboard's. systemd-boot also installs \EFI\BOOT\BOOTX64.EFI, which
  # the real firmware boots from the disk without an entry.
  boot.loader.efi.canTouchEfiVariables = false;

  # Kernel messages on serial too, so the VM can be debugged headless. tty0 is
  # last, so it stays /dev/console: on bare metal the LUKS prompt must show on
  # screen, not on a serial port nobody is watching.
  #
  # no-kvmapf: while eldar-os runs as a VM, a host that swaps out its memory
  # sends async page faults, and one landing in kernel code panics the guest
  # ("Host injected async #PF in kernel mode"). Without them the guest just
  # waits for the page. No effect on bare metal.
  boot.kernelParams = [ "console=ttyS0,115200n8" "console=tty0" "no-kvmapf" ];

  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    trusted-users = [ "root" "@wheel" ];
  };

  networking.networkmanager.enable = true;

  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
      PermitRootLogin = "no";
    };
  };

  programs.zsh = {
    enable = true;
    # Users' zsh setup (oh-my-zsh: completion, prompt) comes from their
    # dotfiles; don't run NixOS's versions first.
    enableGlobalCompInit = false;
    promptInit = "";
  };
  users.users.ben = {
    isNormalUser = true;
    uid = 1000;
    extraGroups = [ "wheel" "networkmanager" ];
    shell = pkgs.zsh;
    openssh.authorizedKeys.keys = [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH4OsaYdZiX9bacnB07cA359XLr6k/tXoQEK6Ri8W5OT ben@ben-desktop -> eldar-os"
      # Ben's Mac, over the tailnet (hosts/eldar-os/tailscale.nix)
      "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQDki3du8ajCxzI1jSBoMus37OrT/TT4Lj119/jmg88bdWZrBTJQ5eqC8Qs8Cr00HRjn6aCDEA5Rn4Sjx7pV/koaR7XpRGeirecMx8l2qMFNT7mZcoVAD6VWY3c9oLIswpFU4iq06JrQTNlfKTWixlXWdMyl9Y+mKnM598ZCBqIp5ovo9OQSOmJRWttwiYdP2BfZqEyvmqcxogb7SJHTfPhqPFJ8jRiytj3AqwuybN42B8okPyPlEJ7m1GYBfj/my+NaHqCwY6oKEkbADmkjn/oa99KVOVu8830jHrDKPlw/95GQDhluYrWEoAVFV127BPc6xePwqwq3NJa9ksXQ0/sbHzkdJzkQXiKRb0fzyGWdb0H/2D/kc1xdfmlKFZi4vXUwlpArwbCbnUFpwIu4P20YIfNVM/J8Gl+5eMiR5yPFfi+KoBxx94LeS+W+DTdXKtdVH6WVI7I726LetzIZyWVfWsWbX2QlxScH9z1PKU6VvYbzK91T7UGqUmi3vdM025VdpECKjxFhQ5m5I/ZF4aOh/P2WibseP5gjg/uUg0CRRZbDIsaE5vrsXwbViy2sjSuA9Q/r7Sbc5xO8d7OcyF8PwmH3YCTpTI58zChuphm1rk0sbzsvvrSyg8x6wIuwy+dLUHyNIyPVwgIgBPzw/QdW8esZ/D56KcuysdiM23xbYw== ben@mac"
      # Ben's iPad, the same way
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBI6jjV42ZcQxH6KrDrW7G667xOKk6jxjnsQn2rzmD6D ben@ipad"
    ];
  };

  # Remove before cutover. While the machine is only reached as ben over SSH
  # (the admin session), passwordless sudo costs little and keeps admin work
  # quick. Once ben runs a desktop, a browser and arbitrary tools, it would turn
  # any compromise of those into root. Agents are separate users either way.
  security.sudo.wheelNeedsPassword = false;

  # scripts/try: ben may *activate* (not make the boot default) a config from
  # his scratch dir without a password: the admin's fast inner loop. Root-
  # equivalent for ben, who is the admin anyway; agents run as other users and
  # only get changes applied through signed main (docs/decisions/0005).
  security.sudo.extraRules = [{
    users = [ "ben" ];
    commands = [{
      command = "/run/current-system/sw/bin/nixos-rebuild test --flake /home/ben/.cache/eldar-os-try\\#eldar-os";
      options = [ "NOPASSWD" ];
    } {
      # same, with a local dotfiles checkout in place of the pinned one
      command = "/run/current-system/sw/bin/nixos-rebuild test --flake /home/ben/.cache/eldar-os-try\\#eldar-os --override-input dotfiles /home/ben/.cache/eldar-os-try-dotfiles";
      options = [ "NOPASSWD" ];
    }];
  }];

  environment.systemPackages = with pkgs; [ git vim curl rsync xfsprogs cryptsetup ];

  zramSwap.enable = true;
  services.fstrim.enable = true;

  system.stateVersion = "26.05";
}
