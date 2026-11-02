#!/bin/sh
# Stop the agent when the package is removed (not on an upgrade); its
# enrollment in /var/lib/watchzit-agent stays.
case "$1" in
    remove|purge|0)
        if command -v systemctl >/dev/null 2>&1; then
            systemctl disable --now watchzit-agent.service >/dev/null 2>&1 || true
        fi
        ;;
esac
exit 0
