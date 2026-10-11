#!/bin/sh
# Run as root after uploading binary + bot.env + panel.env to a private staging dir.
set -eu
stage=${1:?private staging directory required}
test -f "$stage/relationshipbot"
test -f "$stage/bot.env"
test -f "$stage/panel.env"
getent group relationshipdata >/dev/null || groupadd --system relationshipdata
for account in relationshipbot relationshippanel; do
 getent passwd "$account" >/dev/null || useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin "$account"
 usermod -a -G relationshipdata "$account"
done
install -d -o root -g root -m 0755 /opt/relationshipbot
install -d -o root -g root -m 0750 /etc/relationshipbot
install -d -o root -g relationshipdata -m 2770 /var/lib/relationshipbot /var/lib/relationshipbot/data /var/lib/relationshipbot/backups
# SQLite inherits WAL/SHM permissions from its database. Pre-create the shared
# database with group write before either service can win the first-open race.
if ! test -f /var/lib/relationshipbot/data/release.sqlite; then
 install -o relationshipbot -g relationshipdata -m 0660 /dev/null /var/lib/relationshipbot/data/release.sqlite
fi
if test -f /opt/relationshipbot/relationshipbot; then cp -p /opt/relationshipbot/relationshipbot /opt/relationshipbot/relationshipbot.previous; fi
install -o root -g root -m 0755 "$stage/relationshipbot" /opt/relationshipbot/relationshipbot.next
mv /opt/relationshipbot/relationshipbot.next /opt/relationshipbot/relationshipbot
install -o root -g relationshipbot -m 0640 "$stage/bot.env" /etc/relationshipbot/bot.env
install -o root -g relationshippanel -m 0640 "$stage/panel.env" /etc/relationshipbot/panel.env
for app in bot panel; do
 user=relationshipbot
 command=serve
 memory=256M
 if test "$app" = panel; then user=relationshippanel; command=panel; memory=128M; fi
 cat > "/etc/systemd/system/relationshipbot-$app.service" <<EOF
[Unit]
Description=Relationship bot $app
After=network-online.target
Wants=network-online.target
[Service]
User=$user
Group=$user
SupplementaryGroups=relationshipdata
UMask=0007
WorkingDirectory=/var/lib/relationshipbot
EnvironmentFile=/etc/relationshipbot/$app.env
Environment=GOMEMLIMIT=64MiB
ExecStart=/opt/relationshipbot/relationshipbot $command
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/relationshipbot
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
MemoryMax=$memory
[Install]
WantedBy=multi-user.target
EOF
done
cat > /etc/systemd/system/relationshipbot-backup.service <<'EOF'
[Unit]
Description=Verified relationship database backup
After=relationshipbot-bot.service
[Service]
Type=oneshot
User=relationshippanel
Group=relationshippanel
SupplementaryGroups=relationshipdata
UMask=0007
WorkingDirectory=/var/lib/relationshipbot
EnvironmentFile=/etc/relationshipbot/panel.env
Environment=GOMEMLIMIT=64MiB
ExecStart=/opt/relationshipbot/relationshipbot backup
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/relationshipbot
MemoryMax=128M
TimeoutStartSec=180
EOF
cat > /etc/systemd/system/relationshipbot-backup.timer <<'EOF'
[Unit]
Description=Daily relationship backup
[Timer]
OnCalendar=*-*-* 03:10:00 UTC
Persistent=true
RandomizedDelaySec=120
[Install]
WantedBy=timers.target
EOF
systemd-analyze verify /etc/systemd/system/relationshipbot-*.service /etc/systemd/system/relationshipbot-backup.timer
systemctl daemon-reload
systemctl enable --now relationshipbot-bot.service relationshipbot-panel.service relationshipbot-backup.timer
systemctl restart relationshipbot-bot.service relationshipbot-panel.service
systemctl start relationshipbot-backup.service
systemctl is-active relationshipbot-bot.service relationshipbot-panel.service relationshipbot-backup.timer
