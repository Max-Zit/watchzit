# WatchZit — notes

Each run of the WatchZit routine starts from this file: decisions, status, next steps, open questions. Keep it current.

## Product (Stefan's request, 2026-10-27)

A self-hosted web application for monitoring networks and devices: a mix of **MikroTik's The Dude** (network map, discovery, device status, open a device from the map), **Zabbix** (metrics, triggers, alerts, templates) and **Grafana** (graphs, dashboards).

- Metrics from an **agent** (own, Windows + Linux), **SNMP** (v1/v2c/v3), **databases**, and everything else that makes sense (ICMP, TCP ports, HTTP(S), DNS, TLS certificates, MikroTik RouterOS API, FortiGate REST API, …).
- Network **and** devices: routers, switches, firewalls (MikroTik, FortiGate), servers, PCs, printers, UPS.
- **Map**: clicking a device opens its graphs with metrics.
- **Open devices**: in the web (device web UI) or with programs on the user's computer — PuTTY/SSH, RDP, WinBox, Telnet, VNC, … — default list of common tools, plus **custom applications with the path to the program** on the user's computer. (Browsers cannot start local programs directly: plan a protocol handler / small helper installed on the user's PC, or downloadable .rdp/.ssh launch files — propose and confirm with Stefan.)
- Look and behaviour **like BackupZit and ControlZit**: first check how they are built (console layout, sidebar, cards, light/dark theme with the moon button, 14 languages, user guide in the console, roles, 2FA, audit log, sessions, CSRF via Origin, login rate limits, signed self-update, update channels, appliance images, Linux packages, MSI agent, embedded PostgreSQL for development) and reuse what is good; where something could be better, propose it to Stefan.
- **License:** GNU AGPLv3, like BackupZit (free, open source; paid support by MaxZit).
- **Logo:** variant A, an eye with a pulse line (chosen by Stefan 2026-10-27). Files: `brand/` here; full package `C:\Users\Mili\Ai\maxzit-brand\watchzit-logo\` (+ `watchzit-logo.zip`); generator `C:\Users\Mili\Ai\maxzit-brand\tools\brandgen` (`brandgen Inter-SemiBold.ttf OUT watchzit a`).

## Repository and release flow

- GitHub: **https://github.com/Max-Zit/watchzit** (public). Branches: `develop` (all work) and `main` (released versions only).
- Work on `develop` and push there freely. **Merge to `main` and publish a release only when Stefan says so** ("objavi u main"). Same candidate flow as BackupZit (pre-release on a `testing` channel, promoted when approved) once releases start.
- Commits only as stefanzeljkic, never any "Co-Authored-By: Claude" or AI attribution; stage explicit paths, never `git add -A`.

## Status

- 2026-10-27: repository created (license, README, security policy, contributing, code of conduct, issue/PR templates, Dependabot, Scorecard workflow, logo). No code yet.
- 2026-10-27 (first routine run): read BackupZit and ControlZit (ControlZit is the newer, cleaner base: pgx, coder/websocket, html/template, JSON i18n, numbered SQL migrations, separate console/agent listeners, mTLS agent channel, update/helper/netacl/tlsutil packages, appliance scripts). Wrote **docs/ARCHITECTURE.md** (proposal: components, collectors, PostgreSQL metric store with own compressed hourly blocks + 5 min/1 h rollups, ~15 GB for 500 devices x 50 metrics x 1 min with default retention, maps/discovery, triggers/dependencies, launcher with signed watchzit:// links, packaging, dependencies, feature order 0.1-0.9). Sent to Stefan; **waiting for his ok** before writing code.

## Next steps

1. Wait for Stefan's answers (below). Do not write much code before.
2. After the ok: version 0.1 per docs/ARCHITECTURE.md section 10 (module github.com/max-zit/watchzit, cmd/watchzit-server, internal/{store,web,metrics,poll,...}); add ci.yml + security.yml with the first Go code.

## Open questions (asked 2026-10-27)

- Architecture and the PostgreSQL block store ok? Default retention raw 30 d / 5 min 90 d / 1 h 2 years ok?
- Launcher (variant B, per-user MSI, signed watchzit:// links, local confirmation per program path) ok, plus variant A without install?
- Feature order 0.1-0.9 ok, or agent earlier?
- Ports 8443 console / 8444 agent (same as ControlZit) ok?
- Real devices: his MikroTik router (read-only SNMP v3 / API user)? FortiGate-VM needs a Fortinet account/licence.
- Libraries gosnmp (BSD-2), go-sql-driver/mysql (MPL-2.0), go-mssqldb (BSD-3), govmomi (Apache-2.0) ok?
