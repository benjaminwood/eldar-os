{ stdenv, lib, fetchurl, makeWrapper, nodejs_22 }:

# Cloudflare CLI (`cf`, https://developers.cloudflare.com/cf) — npm-only, beta.
# The published tarball ships a bundled dist/ that lazily imports a few npm
# packages. For the public-API commands the only one reached is `minisearch`
# (used by `cf cli search`, zero dependencies), so it is vendored below. The
# rest (miniflare, workerd, @cloudflare/config, codemods, vite plugin, ...) are
# only imported by `cf dev` / `cf build` / `cf deploy` / `cf migrate` for
# Workers projects, which we don't use from here. If project commands are ever
# needed, switch to buildNpmPackage with a generated package-lock.json.
#
# To bump: set `version` (and minisearch's, from `npm view cf dependencies`), then
#   nix hash convert --hash-algo sha256 --to sri $(nix-prefetch-url <tarball url>)
let
  minisearch = fetchurl {
    url = "https://registry.npmjs.org/minisearch/-/minisearch-7.2.0.tgz";
    hash = "sha256-yzuBJqPqZdazh3hylPB5Kw6kpAtw+PN2iAZqVjjgIYo=";
  };
in
stdenv.mkDerivation rec {
  pname = "cloudflare-cf-cli";
  version = "1.0.0-beta.12";

  src = fetchurl {
    url = "https://registry.npmjs.org/cf/-/cf-${version}.tgz";
    hash = "sha256-LGaN+SuptzxQq1uElbwA71HzBk3en/cLPZwCyOTLMHY=";
  };

  nativeBuildInputs = [ makeWrapper ];

  # npm tarballs unpack into ./package
  sourceRoot = "package";

  installPhase = ''
    runHook preInstall
    mkdir -p $out/libexec/cf/node_modules/minisearch
    cp -r bin dist package.json $out/libexec/cf/
    tar xzf ${minisearch} -C $out/libexec/cf/node_modules/minisearch --strip-components=1
    # bin/cf requires Node >= 22; pin it rather than trusting whatever is on PATH.
    # DO_NOT_TRACK keeps the CLI's anonymous telemetry off by default.
    makeWrapper ${nodejs_22}/bin/node $out/bin/cf \
      --add-flags $out/libexec/cf/bin/cf \
      --set-default DO_NOT_TRACK 1
    runHook postInstall
  '';

  meta = with lib; {
    description = "Cloudflare CLI — one CLI for the public Cloudflare API and Workers";
    homepage = "https://developers.cloudflare.com/cf";
    license = with licenses; [ mit asl20 ];
    platforms = [ "x86_64-linux" ];
    mainProgram = "cf";
  };
}
