# Eldar's VM on this machine (modules/eldar-vm.nix).
{
  # While eldar-os itself runs as a VM (8 CPUs, 12 GB), Eldar gets a slice.
  # At cutover to bare metal (Ryzen 9 3900X: 24 threads, 64 GB): vcpu = 24,
  # memMiB = 57344 (all but ~8 GB for the host, supervisor and desktop).
  eldar-os.eldarVm = {
    vcpu = 6;
    memMiB = 6144;
  };
}
