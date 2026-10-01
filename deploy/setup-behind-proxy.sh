#!/bin/sh
# One-time setup for a server where a reverse proxy in Docker (such as Nginx
# Proxy Manager) already owns ports 80 and 443. It starts one small
# container that serves /var/www/heall on the proxy's Docker network, with
# no ports of its own. The existing proxy and its sites are not touched: you
# add heall to the proxy yourself, as one more host.
#
#   sudo sh setup-behind-proxy.sh <Caddyfile> <docker network of the proxy> [user who publishes]
set -eu

[ "$(id -u)" -eq 0 ] || { echo "run this as root or with sudo" >&2; exit 1; }
caddyfile=${1:?usage: sudo sh setup-behind-proxy.sh <Caddyfile> <docker network> [user]}
network=${2:?give the Docker network the proxy is on}
owner=${3:-${SUDO_USER:-root}}
name=heall-site

[ -f "$caddyfile" ] || { echo "$caddyfile does not exist" >&2; exit 1; }
docker network inspect "$network" >/dev/null 2>&1 || { echo "there is no Docker network called $network" >&2; exit 1; }

mkdir -p /var/www/heall /etc/heall
chown -R "$owner" /var/www/heall
install -m 644 "$caddyfile" /etc/heall/Caddyfile
if [ ! -f /var/www/heall/index.html ]; then
  echo "heall: the site has not been published yet" > /var/www/heall/index.html
  chown "$owner" /var/www/heall/index.html
fi

# Only heall's own container is ever replaced.
docker rm -f "$name" >/dev/null 2>&1 || true
docker run --detach --name "$name" --restart unless-stopped \
  --network "$network" \
  --env HEALL_SITE=:80 \
  --volume /etc/heall/Caddyfile:/etc/caddy/Caddyfile:ro \
  --volume /var/www/heall:/var/www/heall:ro \
  --read-only --tmpfs /tmp --volume heall-caddy-data:/data --volume heall-caddy-config:/config \
  --cap-drop ALL --cap-add NET_BIND_SERVICE \
  caddy:2 >/dev/null

sleep 2
if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != "true" ]; then
  echo "the $name container did not stay up:" >&2
  docker logs --tail 20 "$name" >&2
  exit 1
fi
echo "$name is running on the $network network (files published by: $owner)"
echo
echo "Now add it to the proxy. In Nginx Proxy Manager: Hosts > Proxy Hosts > Add Proxy Host"
echo "  Domain names:      heall.rexial.in"
echo "  Scheme:            http"
echo "  Forward hostname:  $name"
echo "  Forward port:      80"
echo "  SSL tab:           request a new certificate, and turn on Force SSL"
