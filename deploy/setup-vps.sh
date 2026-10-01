#!/bin/sh
# One-time setup of an Ubuntu or Debian server for heall.rexial.in. It
# installs Caddy and the site configuration, and makes /var/www/heall
# writable by the user who will publish the site.
#
#   sudo sh setup-vps.sh <Caddyfile> [user who publishes]
#
# It stops, changing nothing, if another web server already uses ports 80 or
# 443, and it keeps a copy of any Caddy configuration it replaces.
set -eu

[ "$(id -u)" -eq 0 ] || { echo "run this with sudo" >&2; exit 1; }
caddyfile=${1:?usage: sudo sh setup-vps.sh <Caddyfile> [user]}
owner=${2:-${SUDO_USER:-root}}
[ -f "$caddyfile" ] || { echo "$caddyfile does not exist" >&2; exit 1; }

# Another web server on these ports would clash with Caddy. Leave it alone.
busy=$(ss -ltnpH 2>/dev/null | awk '$4 ~ /:(80|443)$/' | grep -v caddy || true)
if [ -n "$busy" ]; then
  echo "ports 80 or 443 are already in use by something other than Caddy:" >&2
  echo "$busy" >&2
  echo "nothing was changed. Serve /var/www/heall from that web server instead." >&2
  exit 1
fi

if ! command -v caddy >/dev/null 2>&1; then
  echo "installing Caddy"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg >/dev/null
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq
  apt-get install -y -qq caddy >/dev/null
fi

mkdir -p /var/www/heall
chown -R "$owner" /var/www/heall
if [ ! -f /var/www/heall/index.html ]; then
  echo "heall: the site has not been published yet" > /var/www/heall/index.html
  chown "$owner" /var/www/heall/index.html
fi

if [ -f /etc/caddy/Caddyfile ] && ! cmp -s "$caddyfile" /etc/caddy/Caddyfile; then
  backup="/etc/caddy/Caddyfile.before-heall.$(date +%Y%m%d-%H%M%S)"
  cp /etc/caddy/Caddyfile "$backup"
  echo "the previous Caddy configuration was kept as $backup"
fi
install -m 644 "$caddyfile" /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile >/dev/null 2>&1 || { echo "the Caddy configuration is not valid; run: caddy validate --config /etc/caddy/Caddyfile" >&2; exit 1; }

# Open the web ports if the firewall is on, and never close SSH.
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  echo "opened ports 80 and 443 in ufw"
fi

systemctl enable --now caddy >/dev/null 2>&1
systemctl reload caddy
echo "Caddy is serving /var/www/heall; files are published by: $owner"
