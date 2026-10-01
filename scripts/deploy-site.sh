#!/usr/bin/env bash
# Publishes the heall site to the VPS: the install script and the dashboard.
#
#   scripts/deploy-site.sh user@server                    # publish the site
#   scripts/deploy-site.sh user@server --setup            # first time, on a server with no web server:
#                                                         # install Caddy
#   scripts/deploy-site.sh user@server --behind NETWORK   # first time, on a server where a proxy in
#                                                         # Docker already owns ports 80 and 443:
#                                                         # start a container on that proxy's network
#
# The DNS record heall.rexial.in must point at the server before a
# certificate can be issued. The first-time options ask for the sudo
# password there unless you log in as root.
set -euo pipefail
cd "$(dirname "$0")/.."

host=${1:?usage: scripts/deploy-site.sh user@server [--setup | --behind NETWORK]}
[ -f web/out/index.html ] || { echo "the dashboard is not built; run: make web" >&2; exit 1; }

site=$(mktemp -d)
trap 'rm -rf "$site"' EXIT
cp -R web/out/. "$site/"
cp install.sh "$site/install.sh"

# On the server, sudo is used only when not already root.
case "${2:-}" in
  --setup)
    scp -q deploy/setup-vps.sh deploy/Caddyfile "$host:/tmp/"
    ssh -t "$host" 'if [ "$(id -u)" -eq 0 ]; then s=; else s=sudo; fi; $s sh /tmp/setup-vps.sh /tmp/Caddyfile "$USER"; rm -f /tmp/setup-vps.sh /tmp/Caddyfile'
    ;;
  --behind)
    network=${3:?give the Docker network of the proxy: --behind NETWORK}
    scp -q deploy/setup-behind-proxy.sh deploy/Caddyfile "$host:/tmp/"
    ssh -t "$host" "if [ \"\$(id -u)\" -eq 0 ]; then s=; else s=sudo; fi; \$s sh /tmp/setup-behind-proxy.sh /tmp/Caddyfile '$network' \"\$USER\"; rm -f /tmp/setup-behind-proxy.sh /tmp/Caddyfile"
    ;;
  "") ;;
  *) echo "unknown option ${2}" >&2; exit 1 ;;
esac

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
