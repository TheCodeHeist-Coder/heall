#!/usr/bin/env bash
# Publishes the heall site to the VPS: the install script and the dashboard.
#
#   scripts/deploy-site.sh user@server            # publish the site
#   scripts/deploy-site.sh user@server --setup    # first time: also install Caddy
#
# The server needs the DNS record heall.rexial.in pointing at it before
# Caddy can get its certificate. --setup asks for the sudo password there.
set -euo pipefail
cd "$(dirname "$0")/.."

host=${1:?usage: scripts/deploy-site.sh user@server [--setup]}
[ -f web/out/index.html ] || { echo "the dashboard is not built; run: make web" >&2; exit 1; }

site=$(mktemp -d)
trap 'rm -rf "$site"' EXIT
cp -R web/out/. "$site/"
cp install.sh "$site/install.sh"

if [ "${2:-}" = "--setup" ]; then
  scp -q deploy/setup-vps.sh deploy/Caddyfile "$host:/tmp/"
  ssh -t "$host" 'sudo sh /tmp/setup-vps.sh /tmp/Caddyfile "$USER" && rm -f /tmp/setup-vps.sh /tmp/Caddyfile'
fi

# Unpacked beside the live site and swapped in, so a visitor never gets half
# of the old site and half of the new.
tar -C "$site" -cz . | ssh "$host" '
  set -e
  rm -rf /var/www/heall/.new && mkdir -p /var/www/heall/.new
  tar -xz -C /var/www/heall/.new
  cd /var/www/heall
  find . -mindepth 1 -maxdepth 1 ! -name .new -exec rm -rf {} +
  mv .new/* . && rmdir .new
'
echo "published to $host:/var/www/heall"
echo "check: curl -fsSL https://heall.rexial.in/install | head -3"
