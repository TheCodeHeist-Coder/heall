#!/bin/sh
# Installs heall: one self-contained program, with nothing else to set up.
#
#   curl -fsSL https://heall.rexial.in/install | sh
#
# Environment:
#   HEALL_VERSION      a release tag such as v0.1.0 (default: the latest)
#   HEALL_INSTALL_DIR  where to put the program (default: ~/.local/bin)
#   HEALL_BASE_URL     download from here instead of GitHub Releases
set -eu

repo="TheCodeHeist-Coder/heall"
dir=${HEALL_INSTALL_DIR:-"$HOME/.local/bin"}

say() { printf '%s\n' "$*"; }
die() { printf 'heall install: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system $(uname -s); heall runs on Linux and macOS (on Windows, use WSL)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported processor $(uname -m)" ;;
esac
name="heall_${os}_${arch}.tar.gz"

if [ -n "${HEALL_BASE_URL:-}" ]; then
  base=$HEALL_BASE_URL
elif [ -n "${HEALL_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$HEALL_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
fi

if command -v curl >/dev/null 2>&1; then
  once() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  once() { wget -q "$1" -O "$2"; }
else
  die "curl or wget is needed to download heall"
fi

# A name lookup or a connection can fail for a moment; try a few times
# before giving up.
fetch() {
  for attempt in 1 2 3; do
    if once "$1" "$2" 2>"$tmp/error"; then return 0; fi
    [ "$attempt" -lt 3 ] && sleep 2
  done
  cat "$tmp/error" >&2
  return 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "downloading $name"
fetch "$base/$name" "$tmp/$name" || die "could not download $base/$name"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download the checksums"

# The download must match the published checksum before anything is run.
want=$(awk -v f="$name" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "no checksum published for $name"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$name" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$name" | awk '{ print $1 }')
else
  die "sha256sum or shasum is needed to verify the download"
fi
[ "$want" = "$got" ] || die "the download does not match its checksum; not installing"

tar -xzf "$tmp/$name" -C "$tmp" heall 2>/dev/null || tar -xzf "$tmp/$name" -C "$tmp" ./heall
mkdir -p "$dir"
mv "$tmp/heall" "$dir/heall"
chmod +x "$dir/heall"
say "installed heall $("$dir/heall" --version 2>/dev/null | awk '{ print $NF }') to $dir/heall"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) say ""; say "$dir is not on your PATH. Add this to your shell profile:"; say "  export PATH=\"$dir:\$PATH\"" ;;
esac

say ""
say "heall also needs git, Docker and Python 3.10 or newer, and a Groq API key."
say "In the repository you want to heal, run:"
say "  heall init"
