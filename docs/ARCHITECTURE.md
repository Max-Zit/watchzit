# WatchZit — architecture proposal (draft, 2026-10-27)

Status: **approved by Stefan on 2026-10-27** (retention defaults, ports 8443/8444, agent before maps, launcher variant A+B). Versions 0.1–0.3 are built on develop; the order was changed to map first (section 10).

WatchZit follows ControlZit's newer layout (Go server + PostgreSQL, html/template
console, JSON i18n catalogs, numbered SQL migrations, separate console and agent
listeners, mTLS agent channel over WebSocket) and BackupZit's release machinery
(signed self-update, update channels, appliance images, MSI agent). All code is
written anew in this repository; nothing is shared at runtime with the other
products.

## 1. Components

| Component | What it does |
|---|---|
| `watchzit-server` (Linux, Windows for development) | Console (HTTPS 8443), agent listener (mTLS 8444, separately firewallable), pollers, trigger engine, notifications, metric store, root helper for self-update/appliance settings |
| PostgreSQL | Configuration, audit log, alerts **and** metrics (own compressed format, see 3) — one database to back up |
| `watchzit-agent` (Windows service + Linux systemd) | Local metrics (CPU, memory, disks, network, services, processes, uptime, event log later), pushed to the server; optional *remote poller* mode for sites behind NAT (later) |
| `watchzit-launcher` (Windows first, per-user) | Opens devices with local programs (PuTTY, mstsc, WinBox, VNC, custom paths) from signed links — see 6 |

## 2. Collectors and polling

- Server-side scheduler: every item has an interval (default 60 s, 10 s–1 h); items of one device are polled together (one SNMP GET/GETBULK carries many OIDs). Worker pool with per-device and per-collector concurrency limits, jitter so 500 devices do not fire in the same second, timeouts everywhere.
- Collectors (first wave): ICMP (latency, loss; unprivileged ping on Linux via `CAP_NET_RAW` on the service), TCP port, HTTP(S) (status, time, content match), DNS, TLS certificate expiry, SNMP v1/v2c/v3 (authPriv SHA-2/AES).
- Second wave: PostgreSQL, MySQL/MariaDB, SQL Server (read-only monitoring user), MikroTik RouterOS API (8729 TLS / REST on v7), FortiGate REST API (API token), VMware vSphere API, SNMP traps (UDP 162), syslog receiver.
- Agent items are pushed by the agent every interval over its existing WebSocket (no inbound port on the monitored machine). **As built in 0.4:** the agent makes an ed25519 key and a self-signed client certificate; it enrolls with a one-time-shown code (`WZ1-` + server URL, SHA-256 pin of the agent certificate, token whose hash only is stored) over `POST /agent/enroll`, then keeps one WebSocket `/agent/connect` on port 8444 (hello → welcome with interval and watched services → report every 60 s, inventory daily or on request). The server knows agents by their public key; checks agent, agent_cpu, agent_mem, agent_disk, agent_net, agent_service are created from the reports, and agent interfaces feed the map like SNMP ones.
- **Templates** (Zabbix idea, simpler): a template = items + triggers + graphs + default tools; built-in ones for Generic SNMP (IF-MIB, HOST-RESOURCES), MikroTik, FortiGate, generic switch, printer (Printer-MIB), UPS (UPS-MIB), Windows/Linux agent, PostgreSQL/MySQL/MSSQL. Interfaces, disks etc. are found by low-level discovery (SNMP table walk / agent list) and become items automatically. No MIB parser in v1 — templates carry OIDs.
- Credentials (SNMP communities/v3 keys, API tokens, DB passwords) sealed with AES-GCM under a server key (like ControlZit `secret.go`), write-only in the UI.

## 3. Metric storage (PostgreSQL, own compressed blocks)

Sizing target: **500 devices × 50 metrics × 1 min = 25,000 series, 36 M samples/day.**

A plain row per sample would be ~70 B → ~2.5 GB/day and heavy vacuum/index load. Instead:

1. **Hot table** `samples_hot` (series_id, ts, value), partitioned by hour, filled with `COPY` once per second/batch. Holds only the last ~2 h.
2. **Compaction** every hour: each closed hour is packed per series into one block (delta-of-delta timestamps + XOR floats, Gorilla-style, own code) in `samples_raw(series_id, hour, block bytea)`; the hot partition is dropped (no bloat). ~60 points → ~150 B per block → **~100 MB/day**.
3. **Rollups** written by the same job: 5-minute and 1-hour min/avg/max/count, also packed per series (per day / per month).
4. **Retention** (Settings, defaults): raw 30 days (~3 GB), 5 min 90 days (~6 GB), 1 h 2 years (~5 GB) → **~15 GB for 500 × 50**, ~1.5 GB for 50 devices. Partition/row deletion by date.
5. Latest value per series kept in memory and in `series_latest` (UI, map colours, triggers). Graph queries pick raw / 5 min / 1 h automatically by time range and pixel width.

Why not an embedded TSDB library: the good ones are parts of other monitoring products (excluded by the rules), and a second storage engine doubles backup/restore and upgrade work. If 5,000+ devices ever matter, the block format can move to files without changing the API.

## 4. Map, discovery, device page

- **Maps** (The Dude): several maps and submaps, own SVG editor in a JS file (CSP `script-src 'self'`): drag devices, draw links, labels, background image, grid snap, zoom/pan, auto-layout for new devices. Device icon colour = worst state (up / warning / down / unknown / maintenance); links coloured and labelled by interface utilisation (from IF-MIB counters bound to the link). Submap icon shows the worst state inside.
- **Click a device** → side panel with status, latest values and small graphs + buttons "Graphs", "Open web UI", tools (6); double click → full device page (all graphs, items, triggers, alerts, tools, notes).
- **Discovery**: subnet ranges → ICMP sweep + TCP probe (22, 80, 443, 3389, 8291, …) → SNMP identification (sysObjectID/sysDescr with the configured credentials) → device type + template suggestion; neighbours from LLDP-MIB, CDP, MikroTik MNDP and bridge/ARP tables to propose links. Results go to a review list ("add", "ignore"), optional scheduled rescans.
- **Graphs and dashboards** (Grafana): own canvas chart code (time series with zoom/crosshair, gauges, tables, status grid), time range picker, dashboards made of panels by users; graph images for alert emails later.

## 5. Triggers, alerts, notifications

- Trigger = expression over items: `last/avg/min/max(window) op value`, `change`, `nodata(window)`, with severity, recovery threshold (hysteresis) and "for N minutes". Built-in ones come from templates; per-device overrides.
- **Dependencies** (The Dude/Zabbix): if the router in front is down, devices behind it show *unreachable* and do not alert separately.
- Maintenance windows, acknowledge with comment, alert history, events on the device and map.
- Actions: email (SMTP, tested with Mailpit), webhook (generic JSON; Teams/Slack/Telegram presets later), escalation steps (e.g. second email after 30 min unacknowledged). Rate limiting so a power cut does not send 500 mails — one grouped mail.
- **As built in 0.5:** `internal/alert` evaluates every 15 s (and right after a poll changes a check's status) against the stored state: triggers `device_down` (device status down for N s), `check_down` (one check down, skipped while the whole device is down) and `metric` (latest value of a check above/below a threshold for N s; scope all devices / a group / one device, optional check types). One open problem per condition (key trigger+device+check, unique while open); problems resolve when the condition ends, keep severity/title/detail current and can be acknowledged with a note. Suppression: maintenance windows (devices and groups, one-off) and dependencies (`devices.parent_id`, any ancestor down → "unreachable"); suppressed problems are recorded but sent only if still open when the suppression ends. Notifications per channel (email over SMTP, generic webhook signed with HMAC-SHA256 in `X-WatchZit-Signature`, Slack, Teams) with a lowest severity and "also when resolved"; one evaluation's events go out as one message per channel (≤ 50 events), in the configured language. Channel URLs, webhook keys and the SMTP password are sealed. Resolved problems are deleted after a year. Not yet: hysteresis, nodata, escalation steps, recurring maintenance, map colouring for unreachable devices.

## 6. Opening devices with local programs

Browsers cannot start local programs. Proposal:

**A. Works without anything installed** (always available):
- "Open web UI" → `https://<ip>` / custom URL in a new tab.
- RDP → download a generated `.rdp` file (mstsc opens it).
- `ssh://` / `telnet://` / `vnc://` links for users who already have a handler.

**B. WatchZit Launcher** (small Windows program, per-user MSI, no admin needed: protocol registered under `HKCU\Software\Classes\watchzit`):
- The console builds a link `watchzit://open?p=<payload>&s=<signature>`; payload = tool id, host, port, username (never passwords), server id, expiry 60 s; signed with the server's ed25519 key.
- The launcher is paired with a server once (fingerprint shown in the console, like ControlZit's enrollment code); it refuses unsigned, expired or foreign links.
- Built-in tools: PuTTY, OpenSSH (Windows Terminal/cmd), mstsc, WinBox, VNC viewer, Telnet, browser; **custom applications** defined in the console (name, program path on the PC, argument template with `{host}`, `{port}`, `{user}`). Arguments are passed as an argv list (no shell), host/user validated against a strict charset.
- Safety: the first time a program path is used on a PC the launcher asks "Allow WatchZit to start C:\…\winbox.exe?" and remembers the answer locally — a compromised server cannot run arbitrary programs silently.
- Without a code signing certificate SmartScreen will warn on download (same as the other products today).
- Linux/macOS launcher later (xdg-open handler / app bundle).
- **As built in 0.6:** as above, with these choices: pairing by clicking a watchzit://pair link (user confirms the fingerprint) instead of a code; the launcher is a self-installing per-user exe (MSI later); built-in tools are switchable and limited by device kind; per-device user and port overrides; .rdp download as the no-launcher fallback; links refused when the request is cross-site.

## 7. Console, security, operations (reuse from BackupZit/ControlZit)

Layout, sidebar, cards, light/dark with the moon button, 14 languages, user guide (/docs), roles (admin / operator / viewer, plus map/device-group scope later), argon2id, TOTP 2FA with recovery codes, sessions, CSRF via Origin, login rate limits and lockout, IP allow-lists for console and agent ports, hash-chained audit log, CSP without inline scripts/styles, Secure/HttpOnly/SameSite cookies, certificate page (self-signed / upload / ACME later), REST API with tokens, LDAP/AD + OIDC/SAML later, diagnostics page, Report a bug / Contact support links.

## 8. Packaging and releases

Linux server `.deb`/`.rpm` + `install.sh`; appliance ISO/OVA/qcow2/VHDX (Debian, PostgreSQL inside, console on 8443); agent MSI (WiX 5.0.2) and Linux `.deb`/`.rpm`; launcher MSI; signed self-update with the WatchZit release key and `stable`/`testing` channels; candidate flow like BackupZit; embedded PostgreSQL for development and tests; CI (`ci.yml`, `security.yml`) on `main` and `develop`.

## 9. Dependencies (planned, all AGPLv3-compatible)

pgx (MIT), coder/websocket (ISC), golang.org/x/{crypto,net,sys} (BSD-3), rsc.io/qr (BSD-3, TOTP QR), embedded-postgres (MIT, dev/tests only). No Zabbix/Prometheus/Grafana/Telegraf code or libraries.

Stefan wants an authentic product (2026-10-27), so the protocol code that defines the product is our own: **SNMP v1/v2c/v3 (BER, USM, AES/DES) is written in WatchZit** instead of using gosnmp; vSphere is read through its REST API without govmomi; the metric store, charts, ICMP and the map are our own too. Database monitoring reuses pgx for PostgreSQL; for MySQL/MariaDB and SQL Server we decide at 0.7 (own minimal wire protocol for the few monitoring queries, or a driver: go-sql-driver/mysql MPL-2.0, go-mssqldb BSD-3).

## 10. Proposed feature order

**2026-10-27 evening — changed, confirmed by Stefan the same evening:** Stefan wants WatchZit to feel like The Dude (a network diagram that is live monitoring, devices linked port to port, smooth zoom) with Zabbix and Grafana parts; the agent with token enrollment is optional, SNMP must work without it. New order after 0.2: **0.3 map** (built), **0.4 agent**, **0.5 alerts**; the rest unchanged.

| Version | Content |
|---|---|
| 0.1 | Server skeleton (login, 2FA, roles, audit, settings, 14-language scaffold, theme), devices + groups, ICMP/TCP checks, metric store with compaction/rollups, device page with graphs, CI |
| 0.2 | SNMP v1/v2c/v3, built-in templates (generic, MikroTik, switch, printer, UPS), interface traffic graphs, encrypted credentials |
| 0.3 | Triggers, alerts, dependencies, maintenance, email + webhook |
| 0.4 | Agent Windows + Linux (MSI, deb/rpm), enrollment, system metrics, services (Stefan: agent before maps) |
| 0.5 | Maps (editor, status, links with traffic, click → graphs) and discovery |
| 0.6 | Opening devices: web UI, .rdp, launcher |
| 0.7 | HTTP(S)/DNS/TLS, databases, RouterOS API, FortiGate REST, vSphere |
| 0.8 | Dashboards, reports, REST API |
| 0.9 | User guide, packages, appliance images, self-update, ZAP scan → first public release 1.0 |

Later: SNMP traps, syslog, remote pollers (agent in poller mode), LDAP/SSO, NetFlow (ask).
