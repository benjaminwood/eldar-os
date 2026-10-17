{
  description = "eldar-os: Ben's NixOS workstation and the home of Eldar";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    sops-nix = {
      url = "github:Mic92/sops-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    microvm = {
      url = "github:microvm-nix/microvm.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Ben's dotfiles (chezmoi source), shared with his other machines.
    dotfiles = {
      url = "github:benjaminwood/dotfiles/eldar-os";
      flake = false;
    };
  };

  outputs = { nixpkgs, disko, sops-nix, microvm, dotfiles, ... }: {
    nixosConfigurations.eldar-os = nixpkgs.lib.nixosSystem {
      specialArgs = { inherit dotfiles; };
      modules = [
        disko.nixosModules.disko
        sops-nix.nixosModules.sops
        ./modules/apply
        ./modules/secrets.nix
        ./modules/chezmoi.nix
        microvm.nixosModules.host
        ./modules/eldar-vm.nix
        ./hosts/eldar-os
      ];
    };

    # `nix run .#disko` uses the disko pinned in flake.lock (scripts/install.sh).
    packages.x86_64-linux.disko = disko.packages.x86_64-linux.disko;
  };
}
