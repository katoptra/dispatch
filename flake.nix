{
  description = "katoptra-dispatch: starts the katoptra mirrors' workflows on UTC slots";

  # The same release that the host's flake builds its hosts from. Thus, the host's lock
  # can follow it.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      lib = nixpkgs.lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAll = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: {
        default = pkgs.buildGoModule {
          pname = "katoptra-dispatch";
          version = "0.1.0";
          src = lib.fileset.toSource {
            root = ./.;
            fileset = lib.fileset.unions [
              ./go.mod
              (lib.fileset.fileFilter (f: f.hasExt "go") ./.)
            ];
          };
          vendorHash = null; # only the standard library
          # go test operates in the checkPhase of buildGoModule. go vet operates first. Thus,
          # an error from go vet or go test stops the build on the host.
          preCheck = "go vet ./...";
          # without this step, the name is `dispatch`, the last part of the module path
          postInstall = "mv $out/bin/dispatch $out/bin/katoptra-dispatch";
          meta.mainProgram = "katoptra-dispatch";
        };
      });

      nixosModules.default = import ./module.nix self;

      # The package (with its tests), and the two units of the module, made from a small
      # system. Nix makes the units, but it does not boot a VM. ponytail: a nixosTest boots
      # the timer in a VM. If this check does not find a unit error, add a nixosTest.
      checks = forAll (
        pkgs:
        let
          system = pkgs.stdenv.hostPlatform.system;
          host = lib.nixosSystem {
            modules = [
              self.nixosModules.default
              {
                nixpkgs.hostPlatform = system;
                boot.loader.grub.enable = false;
                fileSystems."/" = {
                  device = "none";
                  fsType = "tmpfs";
                };
                system.stateVersion = "26.05";
                services.katoptra-dispatch = {
                  enable = true;
                  appIdFile = "/run/secrets/app-id";
                  privateKeyFile = "/run/secrets/private-key";
                  healthcheckUrlFile = "/run/secrets/healthcheck-url";
                };
              }
            ];
          };
          units = host.config.systemd.units;
        in
        {
          package = self.packages.${system}.default;
          # the two units. This check stops with an error if ExecStart is not an
          # executable.
          module = pkgs.runCommand "katoptra-dispatch-units" { } ''
            service=${units."katoptra-dispatch.service".unit}/katoptra-dispatch.service
            exe=$(sed -n 's/^ExecStart=//p' "$service")
            test -x "$exe" || { echo "ExecStart $exe is not an executable" >&2; exit 1; }
            install -Dm644 -t $out "$service" ${units."katoptra-dispatch.timer".unit}/katoptra-dispatch.timer
          '';
        }
      );

      formatter = forAll (pkgs: pkgs.nixfmt-tree);
    };
}
