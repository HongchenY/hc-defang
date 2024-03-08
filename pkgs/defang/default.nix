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
    x86_64-linux = "137lf0g4d38xra9jl7kbhq5cidqq6q6nv4y54mn4p1d0fhhxwjk6";
    aarch64-linux = "0sr052s3rdm1zsnsf4bslqcy45kmhc8jvqgnh6xbxihlr981gx56";
    x86_64-darwin = "1b6s76da09wxcq797m7lg32l4nnqsmhna6rh8227vm2k6w4dvcpd";
    aarch64-darwin = "1b6s76da09wxcq797m7lg32l4nnqsmhna6rh8227vm2k6w4dvcpd";
  };

  urlMap = {
    x86_64-linux = "https://github.com/defang-io/defang/releases/download/v0.5.5/defang_0.5.5_linux_amd64.tar.gz";
    aarch64-linux = "https://github.com/defang-io/defang/releases/download/v0.5.5/defang_0.5.5_linux_arm64.tar.gz";
    x86_64-darwin = "https://github.com/defang-io/defang/releases/download/v0.5.5/defang_0.5.5_macOS.zip";
    aarch64-darwin = "https://github.com/defang-io/defang/releases/download/v0.5.5/defang_0.5.5_macOS.zip";
  };
in
stdenvNoCC.mkDerivation {
  pname = "defang";
  version = "0.5.5";
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
