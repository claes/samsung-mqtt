{ pkgs ? import <nixpkgs> {}}:

pkgs.mkShell {
  packages = [ pkgs.go pkgs.gopls pkgs.go-outline pkgs.gotools pkgs.godef pkgs.delve pkgs.mqttui];
}
