# This file was generated by GoReleaser. DO NOT EDIT.
# vim: set ft=nix ts=2 sw=2 sts=2 et sta
{
system ? builtins.currentSystem
, lib
, fetchurl
, installShellFiles
, stdenvNoCC
, unzip
}:
let
  shaMap = {
    x86_64-linux = "0p3xi35zw8n4nfrggsx22928n2imkygj5hq5f87vyadfx1ai5j6m";
    aarch64-linux = "00xbgw2kmxyanbri6ig6a28s188k3f3ga40nh9rvbvwvwvn7wzg7";
    x86_64-darwin = "1f9y25cn9hy0ijrgwfzsphkk67lm66vr77mqlj67as5wl5gbkk47";
    aarch64-darwin = "1f9y25cn9hy0ijrgwfzsphkk67lm66vr77mqlj67as5wl5gbkk47";
  };

  urlMap = {
    x86_64-linux = "https://github.com/defang-io/defang/releases/download/v0.5.2/defang_0.5.2_linux_amd64.tar.gz";
    aarch64-linux = "https://github.com/defang-io/defang/releases/download/v0.5.2/defang_0.5.2_linux_arm64.tar.gz";
    x86_64-darwin = "https://github.com/defang-io/defang/releases/download/v0.5.2/defang_0.5.2_macOS.zip";
    aarch64-darwin = "https://github.com/defang-io/defang/releases/download/v0.5.2/defang_0.5.2_macOS.zip";
  };
in
stdenvNoCC.mkDerivation {
  pname = "defang";
  version = "0.5.2";
  src = fetchurl {
    url = urlMap.${system};
    sha256 = shaMap.${system};
  };

  sourceRoot = ".";

  nativeBuildInputs = [ installShellFiles unzip ];

  installPhase = ''
    mkdir -p $out/bin
    cp -vr ./defang $out/bin/defang
  '';

  system = system;

  meta = {
    description = "Command-line interface for the Defang Opinionated Platform";
    homepage = "https://defang.io/";
    license = lib.licenses.mit;

    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];

    platforms = [
      "aarch64-darwin"
      "aarch64-linux"
      "x86_64-darwin"
      "x86_64-linux"
    ];
  };
}
