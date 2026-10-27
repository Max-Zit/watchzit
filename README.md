<p align="center"><img src="brand/watchzit-logo-on-light.svg" alt="WatchZit" height="64"></p>

# WatchZit

**Free, open-source, self-hosted network and infrastructure monitoring** — a network map like The Dude, metrics and alerts like Zabbix, dashboards like Grafana, in one web console that is easy to install.

> **Status:** in development. There is no release yet; follow the repository or [Discussions](https://github.com/Max-Zit/watchzit/discussions) for news.

## Planned

- **Network map:** discover devices (ping, SNMP, ARP/LLDP neighbors), draw them on maps with links and live status; click a device to see its graphs.
- **Metrics from everywhere:** SNMP v1/v2c/v3 (routers, switches, firewalls, printers, UPS), a small agent for Windows and Linux (CPU, memory, disks, services, processes, event logs), ICMP/TCP/HTTP(S)/DNS/certificate checks, databases (PostgreSQL, MySQL/MariaDB, SQL Server), MikroTik RouterOS and FortiGate APIs.
- **Graphs and dashboards:** time series with long-term retention, dashboards per site or customer, top lists.
- **Alerts:** thresholds and dependencies (a router down hides the alerts behind it), maintenance windows, email, chat and webhooks.
- **Open a device:** in the browser (web interface) or with the programs on your computer — SSH/PuTTY, RDP, WinBox, Telnet, VNC, your own tools — from the map.
- **Self-hosted and secure:** one server (Linux package or appliance image), PostgreSQL, HTTPS, two-factor sign-in, roles, audit log, signed updates; 14 console languages.

WatchZit is made by [MaxZit](https://maxzit.com), like [BackupZit](https://github.com/Max-Zit/backupzit) and [ControlZit](https://github.com/Max-Zit/controlzit).

## License

GNU Affero General Public License v3.0 — see [LICENSE](LICENSE). Paid support will be offered by MaxZit.
