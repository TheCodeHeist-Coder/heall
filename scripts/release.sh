#!/usr/bin/env bash
# Builds the release archives in dist/: one self-contained heall binary per
# platform, with the agent and the dashboard inside it, plus checksums.
#
#   scripts/release.sh [version]     # version defaults to `git describe`
#
# Publish them with:
#   gh release create v0.1.0 dist/*.tar.gz dist/checksums.txt --title v0.1.0 --generate-notes
#   (cd npm && npm publish)
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
[ -f web/out/index.html ] || { echo "the dashboard is not built; run: make web" >&2; exit 1; }
make --no-print-directory embed

rm -rf dist
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="heall_${os}_${arch}"
  stage=$(mktemp -d)
  (cd cli && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X heall/embedded.Version=$version" -o "$stage/heall" .)
  cp README.md "$stage/"
  [ -f LICENSE ] && cp LICENSE "$stage/"
  tar -C "$stage" -czf "dist/$name.tar.gz" .
  rm -rf "$stage"
  echo "built dist/$name.tar.gz"
done
(cd dist && sha256sum ./*.tar.gz | sed 's# \./# #' > checksums.txt)

# The npm package downloads the release with its own version number, so the
# two must agree.
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*)
    node -e 'const f="npm/package.json", p=require("./"+f); p.version=process.argv[1].slice(1); require("fs").writeFileSync(f, JSON.stringify(p, null, 2)+"\n")' "$version"
    echo "npm/package.json set to ${version#v}"
    ;;
esac
echo "version $version; checksums in dist/checksums.txt"
