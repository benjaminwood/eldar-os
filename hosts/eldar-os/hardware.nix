# Hand-written (not nixos-generate-config): this system has to boot both as a
# QEMU VM on the old Ubuntu host (virtio disk + net) and on the real machine:
# Ryzen 9 3900X, NVMe, RTL8125 + I211 NICs, Radeon RX 5600/5700 (Navi 10).
{
  nixpkgs.hostPlatform = "x86_64-linux";

  boot.initrd.availableKernelModules = [
    # bare metal
    "nvme" "xhci_pci" "ahci" "usbhid" "usb_storage" "sd_mod"
    # QEMU
    "virtio_pci" "virtio_blk" "virtio_scsi" "virtio_net"
  ];
  # kvm-amd isn't listed in boot.kernelModules: udev loads it from the CPU's
  # modalias on AMD, and forcing it fails (and degrades boot) on Intel, which
  # is what CI runners have.

  hardware.enableRedistributableFirmware = true;
  hardware.cpu.amd.updateMicrocode = true;
}
