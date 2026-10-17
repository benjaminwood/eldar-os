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
  boot.kernelParams = [ "console=ttyS0,115200n8" "console=tty0" ];

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
