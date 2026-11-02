#!/bin/sh
# Start the agent when it is enrolled; otherwise say how to enroll it.
command -v systemctl >/dev/null 2>&1 || exit 0
systemctl daemon-reload >/dev/null 2>&1 || true
# a copy installed earlier with "watchzit-agent install" gives way to the package
if [ -f /etc/systemd/system/watchzit-agent.service ] && grep -q /usr/local/bin/watchzit-agent /etc/systemd/system/watchzit-agent.service; then
    rm -f /etc/systemd/system/watchzit-agent.service /usr/local/bin/watchzit-agent
    systemctl daemon-reload >/dev/null 2>&1 || true
fi
systemctl enable watchzit-agent.service >/dev/null 2>&1 || true
if [ -f /var/lib/watchzit-agent/agent.json ] || [ -f /var/lib/watchzit-agent/pending-enrollment.json ]; then
    systemctl restart watchzit-agent.service >/dev/null 2>&1 || true
else
    echo "Enroll this computer with the code from the console (Agents page):"
    echo "  sudo watchzit-agent enroll --code WZ1-..."
    echo "  sudo systemctl start watchzit-agent"
fi
exit 0
