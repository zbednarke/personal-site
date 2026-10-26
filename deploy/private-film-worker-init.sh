#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends ffmpeg sudo nftables ca-certificates nodejs npm
getent group magic-evidence >/dev/null || groupadd --system magic-evidence
id magic-film >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/magic-film --shell /usr/sbin/nologin magic-film
id magic-codex >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/magic-codex --shell /usr/sbin/nologin magic-codex
usermod -a -G magic-evidence magic-film
usermod -a -G magic-evidence magic-codex
chmod 0750 /var/lib/magic-film
chgrp magic-evidence /var/lib/magic-film
chmod 0700 /var/lib/magic-codex
install -d -m 2750 -o magic-film -g magic-evidence /var/lib/magic-film/jobs
install -d -m 0700 -o magic-codex -g magic-codex /var/lib/magic-codex/.codex
install -d -m 0700 /etc/magic-film
install -d -m 0755 /usr/local/libexec /opt/magic-film /opt/magic-codex
npm install --global --prefix /opt/magic-codex @openai/codex@0.164.0

cat >/usr/local/libexec/magic-codex-session <<'EOF'
#!/bin/sh
exec env -i HOME=/var/lib/magic-codex CODEX_HOME=/var/lib/magic-codex/.codex PATH=/opt/magic-codex/bin:/usr/bin:/bin LANG=C.UTF-8 /opt/magic-codex/bin/codex "$@"
EOF
chmod 0755 /usr/local/libexec/magic-codex-session
cat >/usr/local/bin/magic-film-codex <<'EOF'
#!/bin/sh
exec sudo -n -u magic-codex /usr/local/libexec/magic-codex-session "$@"
EOF
chmod 0755 /usr/local/bin/magic-film-codex
printf '%s\n' 'magic-film ALL=(magic-codex) NOPASSWD: /usr/local/libexec/magic-codex-session *' >/etc/sudoers.d/magic-film-codex
chmod 0440 /etc/sudoers.d/magic-film-codex
visudo -cf /etc/sudoers.d/magic-film-codex

# The model process cannot obtain the VM's Google identity. The Go worker
# remains able to use ADC for Cloud SQL and its private recording bucket.
codex_uid=$(id -u magic-codex)
cat >/etc/nftables.conf <<EOF
#!/usr/sbin/nft -f
flush ruleset
table inet magic_codex {
 chain output {
  type filter hook output priority 0; policy accept;
  meta skuid $codex_uid ip daddr 169.254.169.254 udp dport 53 accept
  meta skuid $codex_uid ip daddr 169.254.169.254 tcp dport 53 accept
  meta skuid $codex_uid ip6 daddr fd20:ce::254 udp dport 53 accept
  meta skuid $codex_uid ip6 daddr fd20:ce::254 tcp dport 53 accept
  meta skuid $codex_uid ip daddr 169.254.169.254 reject
  meta skuid $codex_uid ip6 daddr fd20:ce::254 reject
 }
}
EOF
systemctl enable --now nftables
systemctl restart nftables

cat >/etc/systemd/system/magic-film-worker.service <<'EOF'
[Unit]
Description=Private serialized Codex subscription film worker
After=network-online.target nftables.service
Wants=network-online.target
Requires=nftables.service
ConditionPathExists=/etc/magic-film/worker.env
ConditionPathIsExecutable=/opt/magic-film/jazz-api

[Service]
Type=simple
User=magic-film
Group=magic-evidence
EnvironmentFile=/etc/magic-film/worker.env
WorkingDirectory=/var/lib/magic-film
ExecStart=/opt/magic-film/jazz-api
Restart=on-failure
RestartSec=15
TimeoutStopSec=45
KillMode=control-group
UMask=0027
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectProc=invisible
MemoryMax=3500M
TasksMax=512
# sudo only permits the dedicated unprivileged CLI identity above.

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
touch /var/lib/magic-film/bootstrap-ready
