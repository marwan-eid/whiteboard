#!/bin/bash
# First boot of the demo VM: installs Docker, fetches the repo, writes .env,
# and starts deploy/compose.prod.yaml. Terraform renders this file with
# templatefile: it fills in the dollar-brace names it knows, and doubled
# dollars reach the shell as single ones.
set -euxo pipefail
exec > >(tee -a /var/log/whiteboard-setup.log) 2>&1

# Small machines (the 1 GB Micro VM) get swap, so a burst does not get the
# database killed.
if [ "${swap_gb}" -gt 0 ] && [ ! -f /swapfile ]; then
  fallocate -l ${swap_gb}G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo "/swapfile none swap sw 0 0" >> /etc/fstab
fi

# Oracle's Ubuntu images reject everything but SSH in iptables, on top of the
# VCN security list. Let HTTP, HTTPS and HTTP/3 in, and keep it across reboots.
iptables -I INPUT -p tcp -m state --state NEW -m multiport --dports 80,443 -j ACCEPT
iptables -I INPUT -p udp --dport 443 -j ACCEPT
netfilter-persistent save

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y docker.io docker-compose-v2 git curl
systemctl enable --now docker
usermod -aG docker ubuntu

git clone "${repo_url}" /opt/whiteboard
chown -R ubuntu:ubuntu /opt/whiteboard
cd /opt/whiteboard

ip=$(curl -fsS https://ifconfig.me)
site="${site_address}"
if [ -z "$site" ]; then
  site="$${ip//./-}.sslip.io" # a hostname for this IP, with no account anywhere
fi

# DuckDNS: keep the name pointed at this VM (its IP can change on rebuild).
if [ -n "${duckdns_token}" ] && [ "$${site%.duckdns.org}" != "$site" ]; then
  name="$${site%.duckdns.org}"
  cat > /etc/cron.d/duckdns <<CRON
*/5 * * * * root curl -fsS "https://www.duckdns.org/update?domains=$name&token=${duckdns_token}&ip=" >/dev/null
CRON
  curl -fsS "https://www.duckdns.org/update?domains=$name&token=${duckdns_token}&ip="
fi

umask 077
cat > .env <<ENV
SITE_ADDRESS=$site
SECRET=${secret}
POSTGRES_PASSWORD=${postgres_password}
GRAFANA_ADMIN_PASSWORD=${grafana_password}
IMAGE_TAG=${image_tag}
BACKUP_UPLOAD_URL=${backup_url}
BACKUP_AT=03:00
ENV
chown ubuntu:ubuntu .env

compose="docker compose ${compose_files}"
$compose pull ${services}
$compose up -d ${services}
# Remember how this machine runs the stack, for deploy/update.sh.
cat > /etc/whiteboard.conf <<CONF
COMPOSE_FILES="${compose_files}"
SERVICES="${services}"
CONF
echo "whiteboard: up at https://$site"
