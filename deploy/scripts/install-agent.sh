#!/usr/bin/env bash
# Offline installer for air-gapped hosts. The coordinator publishes the
# recommended bootstrap/update script at GET /install.sh; this script expects
# the binary, configuration and certificates to be copied to the host already.
#
# Install the Kurwall agent on a Linux host. Run as root from the repository
# root after building the binary (or use deploy/scripts/build-agent-static.sh).
set -euo pipefail

BINARY=${1:-build/kurwall-agent}
CONFIG=${2:-deploy/agent.yaml}
UNIT=${3:-deploy/systemd/kurwall-agent.service}

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

systemctl stop kurwall-agent.service 2>/dev/null || true

install -D -m 0755 "$BINARY" /usr/local/bin/kurwall-agent
install -d -m 0750 /etc/kurwall /etc/kurwall/tls
install -d -m 0750 /var/lib/kurwall
if [ ! -f /etc/kurwall/agent.yaml ]; then
  install -m 0640 "$CONFIG" /etc/kurwall/agent.yaml
else
  echo "keeping existing /etc/kurwall/agent.yaml"
fi
install -m 0644 "$UNIT" /etc/systemd/system/kurwall-agent.service

install -D -m 0644 deploy/sysctl/99-kurwall.conf /etc/sysctl.d/99-kurwall.conf
sysctl --system >/dev/null

modprobe nf_conntrack
modprobe nf_conntrack_netlink
modprobe nfnetlink_queue
modprobe nfnetlink_log

systemctl daemon-reload
systemctl enable --now kurwall-agent.service
systemctl --no-pager status kurwall-agent.service | head -20