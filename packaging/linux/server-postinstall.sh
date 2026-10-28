#!/bin/sh
set -e
if ! getent passwd watchzit >/dev/null; then
    useradd --system --home-dir /var/lib/watchzit --shell /usr/sbin/nologin watchzit 2>/dev/null \
      || useradd --system --home-dir /var/lib/watchzit --shell /sbin/nologin watchzit
fi
install -d -m 0700 -o watchzit -g watchzit /var/lib/watchzit
install -d -m 0750 -o watchzit -g watchzit /var/lib/watchzit/downloads /var/lib/watchzit/helper
install -d -m 0700 -o root -g root /var/cache/watchzit
# The agent installers of this version, offered in the console right away.
if [ -d /usr/share/watchzit/downloads ]; then
    cp -f /usr/share/watchzit/downloads/* /var/lib/watchzit/downloads/ 2>/dev/null || true
    chown -R watchzit:watchzit /var/lib/watchzit/downloads
fi
chown root:watchzit /etc/watchzit/server.env
chmod 640 /etc/watchzit/server.env
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    systemctl enable watchzit-server.service >/dev/null 2>&1 || true
    systemctl enable --now watchzit-helper.path >/dev/null 2>&1 || true
    # An upgrade by hand (dpkg -i, rpm -U) runs the new version right away.
    systemctl try-restart watchzit-server.service >/dev/null 2>&1 || true
fi
if grep -q 'CHANGE_ME' /etc/watchzit/server.env; then
    echo "WatchZit server installed. Set WATCHZIT_DB in /etc/watchzit/server.env,"
    echo "then: systemctl start watchzit-server (or use install.sh, which does all of it)."
fi
# The WatchZit appliance (not other servers): the setup menu on its
# screen, the start-up script and hardened SSH.
if [ -f /var/lib/watchzit/.appliance ]; then
    a=/usr/share/watchzit/appliance
    install -m 0755 $a/watchzit-setup /usr/local/sbin/watchzit-setup
    install -m 0755 $a/watchzit-appliance-boot /usr/local/sbin/watchzit-appliance-boot
    install -m 0644 $a/watchzit-appliance-boot.service /etc/systemd/system/watchzit-appliance-boot.service
    install -m 0644 $a/watchzit-menu.sh /etc/profile.d/watchzit-menu.sh
    install -m 0440 $a/sudoers-watchzit-setup /etc/sudoers.d/watchzit-setup
    systemctl daemon-reload 2>/dev/null || true
    systemctl enable watchzit-appliance-boot.service >/dev/null 2>&1 || true
    if [ -d /etc/ssh/sshd_config.d ]; then
        install -m 0644 $a/sshd-watchzit.conf /etc/ssh/sshd_config.d/50-watchzit.conf
        sshd -t 2>/dev/null && { systemctl reload ssh 2>/dev/null || true; } || rm -f /etc/ssh/sshd_config.d/50-watchzit.conf
    fi
    [ -f /var/lib/watchzit/.appliance-ready ] && /usr/local/sbin/watchzit-appliance-boot issue >/dev/null 2>&1 || true
fi
