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

## Decisions

- 2026-10-27: Stefan approved docs/ARCHITECTURE.md and the version order; answers: default retention ok (raw 30 d / 5 min 90 d / 1 h 2 years), **agent before maps** (0.4 agent, 0.5 maps + discovery), ports 8443 console / 8444 agent ok, no real MikroTik/FortiGate (only a TP-Link router) — **a virtual MikroTik CHR on Proxmox is fine** (VMID 98xx), libraries: "I want an authentic product, decide yourself" → own SNMP v1/v2c/v3 (no gosnmp), vSphere via REST (no govmomi), own metric store/charts/ICMP; MySQL/MSSQL driver vs own minimal protocol decided at 0.7.
- Code base: started from ControlZit's (Stefan's own, same license) auth/store/web/netacl/tlsutil/logfile/testdb packages, adapted for WatchZit (no customers; roles admin/operator/viewer; console only, the agent listener comes with 0.4). Everything monitoring-specific is new.

## Status

- 2026-10-27: repository created; architecture proposal approved (see Decisions).
- **2026-10-27: version 0.1 built on `develop` (not released).**
  - Server `cmd/watchzit-server`: console HTTPS (default :8443, self-signed console cert in data-dir/tls), allow-list (private networks by default, `--reset-allowlist`), `--dev-embedded-db`, `--dev-http` (loopback only), `--dev-no-2fa` (only with the embedded DB), `--unblock-ip`, log file data-dir/logs/server.log.
  - Sign-in like ControlZit: setup token, argon2id, mandatory TOTP + recovery codes, sessions (default 12 h / 60 min idle), brute-force blocks, CSRF via Origin/Sec-Fetch-Site, CSP without inline scripts/styles, hash-chained audit log; roles admin / operator / viewer.
  - Devices (name, address, kind, group, web URL, notes, paused), groups, checks ICMP (internal/probe: Linux unprivileged ICMP socket with raw fallback, Windows IcmpSendEcho) and TCP; poller (internal/poller) with jitter, worker pool, batch saves; device status up/warning/down/unknown/paused from its checks.
  - Metric store internal/tsdb: samples_hot partitioned by hour (COPY), hourly packing into own compressed blocks (delta-of-delta time + XOR floats, plus a scaled-integer mode for decimal values: an hour of ping times ≈ 100 B), 5-minute (per day) and 1-hour (per month) rollups min/max/avg/count, retention without deleting data not rolled up yet, late samples merged, queries pick the resolution and compute the not-yet-rolled-up part on the fly. Settings → Metric data (retention, size).
  - Console: overview (counts + problems), devices (search/group/status filters), device page (facts, graphs 1h…1y, checks with run now / settings dialog / delete, add check), groups, users, account, audit, settings (network, security, metric data). Own canvas charts (static/chart.js): avg line, min–max band, gaps, tooltip, drag to zoom, double-click back, server time zone, light/dark redraw, refresh every minute.
  - 14 languages complete (tools/i18n prune/missing/merge; TestCatalogsComplete).
  - Tests: tsdb (round trip incl. fuzz, compactness, 3-day lifecycle, retention, late samples), probe (loopback ping, TEST-NET no answer, TCP open/refused), poller (real TCP + ping into the DB), web e2e (setup token, TOTP + replay, brute force, CSP/CSRF/allow-list, devices/checks/series JSON, viewer forbidden, delete removes data, audit chain, every page in every language). go vet, staticcheck, gosec, govulncheck clean (Windows and Linux).
  - CI: .github/workflows/ci.yml (gofmt, vet, tests on Linux; Windows build) and security.yml (build, vet, staticcheck, gosec, govulncheck, gitleaks) on main and develop. THIRD_PARTY_LICENSES.md via tools/licenses (13 modules, MIT/BSD/public domain).
  - Real test: dev server on Stefan's PC (`watchzit-dev` in C:\Users\Mili\Ai\.claude\launch.json: --dev-http 127.0.0.1:8098, console 127.0.0.1:8463, embedded PG port 54339, data in watchzit/.devdata, dev admin password in .devdata/dev-admin.txt, --dev-no-2fa) polling lab hosts pve .106, bz-storage .102, db-test .130 (5432 up, 3306 closed from outside → partly down), esxi .100, gateway .254, and 192.0.2.10 (down). Graphs checked in the preview at 1400 px / 800 px / 375 px, light and dark.
  - Known: stopping the dev server leaves the embedded PostgreSQL running (stop it with .devdata/pgbin/bin/pg_ctl stop -D .devdata/pgdata). Hourly packing verified on real data at 19:02: the hour 17–18 UTC (22 min, 18 series) became 18 blocks of 52 B on average (max 77 B), its partition was dropped. GitHub CI and security workflows green for d6b8c38.

## Next steps

1. **0.2: SNMP** — own BER/SNMP v1/v2c/v3 (USM: MD5/SHA/SHA-2 auth, DES/AES priv), GET/GETNEXT/GETBULK/walk; encrypted credentials (sealed, write-only); built-in templates (generic IF-MIB/HOST-RESOURCES, MikroTik, switch, printer, UPS) with low-level discovery of interfaces/disks; counter → rate handling (wrap, reset); interface traffic graphs. Lab target: **MikroTik CHR VM on Proxmox (VMID 9800)** from mikrotik.com, SNMP v3 read-only; also ESXi/pve SNMP if enabled.
2. 0.3 alerts (triggers, dependencies, maintenance, email via Mailpit, webhook), 0.4 agent, 0.5 maps + discovery, 0.6 launcher, 0.7 HTTP/DNS/TLS/DB/RouterOS/FortiGate/vSphere, 0.8 dashboards/API, 0.9 packages/appliance/user guide → 1.0.
3. User guide pages (/docs, 14 languages) — start with 0.2 so docs grow with the features.

## Open questions

- FortiGate-VM needs a Fortinet account/licence — later, when 0.7 comes (Stefan has none now).
