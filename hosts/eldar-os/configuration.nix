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

  programs.zsh.enable = true;
  users.users.ben = {
    isNormalUser = true;
    uid = 1000;
    extraGroups = [ "wheel" "networkmanager" ];
    shell = pkgs.zsh;
    openssh.authorizedKeys.keys = [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH4OsaYdZiX9bacnB07cA359XLr6k/tXoQEK6Ri8W5OT ben@ben-desktop -> eldar-os"
    ];
  };

  # BOOTSTRAP ONLY — remove before cutover. Lets Claude apply configs over SSH
  # (scripts/deploy) before ben has a password and before the PR → apply loop
  # exists.
  security.sudo.wheelNeedsPassword = false;

  environment.systemPackages = with pkgs; [ git vim curl rsync btrfs-progs cryptsetup ];

  zramSwap.enable = true;
  services.fstrim.enable = true;

  system.stateVersion = "26.05";
}
