<p align="center"><img src="brand/watchzit-logo-on-light.svg" alt="WatchZit" height="64"></p>

# WatchZit

**Free, open-source, self-hosted network and infrastructure monitoring** — a network map like The Dude, metrics and alerts like Zabbix, dashboards like Grafana, in one web console that is easy to install.

WatchZit watches routers, switches, firewalls, servers, printers, UPSs and services over ping, SNMP, HTTP and a small agent, draws them on live network maps, keeps their measurements for years, and tells you when something breaks — by email, chat or webhook. It runs on one Linux server or as a ready-made appliance for VMware, Proxmox and Hyper-V.

WatchZit is made by [MaxZit](https://maxzit.com), like [BackupZit](https://github.com/Max-Zit/backupzit) and [ControlZit](https://github.com/Max-Zit/controlzit).

## Contents

- [Features](#features)
- [Installation](#installation)
  - [Downloads](#downloads)
  - [1. Ready-made VM image](#1-ready-made-vm-image)
  - [2. Appliance ISO](#2-appliance-iso)
  - [3. Install script](#3-install-script-existing-linux-server)
  - [4. Packages by hand](#4-packages-by-hand)
- [First sign-in](#first-sign-in)
- [The appliance: setup menu and settings](#the-appliance-setup-menu-and-settings)
- [Agents](#agents)
- [Updates](#updates)
- [Requirements and ports](#requirements-and-ports)
- [Documentation, support, license](#documentation-support-license)

## Features

- **Network maps:** devices on maps with live state and traffic on the cables, real pictures of some 3,500 device models (ports and drive bays drawn where they are), racks, hypervisors with their virtual machines, HA groups, VLANs and VXLAN, submaps, the path between two devices, kiosk displays for a monitoring TV.
- **Discovery:** ping sweeps, SNMP, LLDP/CDP and ARP neighbours; suggested links between devices.
- **Checks:** ping, TCP, HTTP(S) with text and certificate expiry, DNS, SNMP v1/v2c/v3 (interfaces, CPU, memory, disks, temperatures, UPS, printers), MikroTik RouterOS, FortiGate, PostgreSQL, MySQL/MariaDB, SQL Server, JSON APIs, VRRP roles.
- **Agent** for Windows and Linux: CPU, memory, disks, interfaces, services — one outbound connection, enrolled with a one-time code.
- **Graphs and dashboards:** years of history (raw, 5-minute and hourly), zoom, graphs you arrange, resize and colour like dashboard panels, and **data sources** — PostgreSQL, MySQL, SQL Server, Prometheus, InfluxDB and JSON — next to WatchZit's own measurements.
- **Syslog and SNMP traps:** received from devices, searchable under Logs, with triggers that open problems from them.
- **Alerts:** thresholds, dependencies (a router down hides the alarms behind it), maintenance windows, email, Slack, Microsoft Teams, Discord, Telegram, ntfy, PagerDuty, SMS and webhooks; availability reports.
- **Open a device** from the map: its web interface, SSH/PuTTY, RDP, WinBox, VNC or your own tools.
- **Secure by default:** HTTPS, two-factor sign-in, roles, allowed networks, brute-force blocking, audit log, encrypted secrets, signed updates, 14 console languages, REST API.

## Installation

Choose one of four ways. The first one is the fastest.

| Way | Best for | Needs |
|---|---|---|
| **1. Ready-made VM image** (OVA, qcow2, VHDX) | import, start, sign in, in a few minutes | VMware, Proxmox/KVM or Hyper-V; no internet access |
| **2. Appliance ISO** | a new VM or a physical server, unattended | internet access during installation |
| **3. Install script** | an existing Debian, Ubuntu, AlmaLinux or Rocky Linux server | root and internet access |
| **4. Packages by hand** | your own PostgreSQL, configuration management, offline servers | the server package from the release |

**Requirements:** a VM or server with 2 vCPU, 2 GB RAM and a 16 GB or larger disk; the appliance uses a bigger disk by itself. Browsers reach the console on **TCP 8443**, agents on **TCP 8444**.

### Downloads

These links always point to the newest release:

| File | What |
|---|---|
| [watchzit-appliance.ova](https://github.com/Max-Zit/watchzit/releases/latest/download/watchzit-appliance.ova) | VM image for VMware ESXi, Workstation, VirtualBox |
| [watchzit-appliance.qcow2](https://github.com/Max-Zit/watchzit/releases/latest/download/watchzit-appliance.qcow2) | VM image for Proxmox VE and KVM |
| [watchzit-appliance-vhdx.zip](https://github.com/Max-Zit/watchzit/releases/latest/download/watchzit-appliance-vhdx.zip) | VM image for Hyper-V (generation 1) |
| [watchzit-appliance.iso](https://github.com/Max-Zit/watchzit/releases/latest/download/watchzit-appliance.iso) | appliance installer ISO |
| [install.sh](https://github.com/Max-Zit/watchzit/releases/latest/download/install.sh) | install script for an existing Linux server |
| [SHA256SUMS-appliance](https://github.com/Max-Zit/watchzit/releases/latest/download/SHA256SUMS-appliance) | checksums of the images and the ISO |

The server packages (deb, rpm) and the agent installers (MSI, Linux) are on the [release page](https://github.com/Max-Zit/watchzit/releases/latest); every release is signed with the WatchZit release key.

### 1. Ready-made VM image

1. Import the image:
   - **VMware ESXi:** in the Host Client choose *Create / Register VM → Deploy a virtual machine from an OVF or OVA file* and select `watchzit-appliance.ova`. On vCenter use *Deploy OVF Template*.
   - **Proxmox VE:** create a VM without a disk (2 vCPU, 2 GB RAM, VirtIO network). Copy `watchzit-appliance.qcow2` to the node, then run:
     ```
     qm importdisk <vmid> watchzit-appliance.qcow2 local-lvm
     ```
     Attach the imported disk as SCSI, make it the boot disk and enable the QEMU guest agent.
   - **Hyper-V:** unzip `watchzit-appliance-vhdx.zip`. Create a *generation 1* VM with 2 GB RAM and choose the VHDX as its disk.
   - **VirtualBox / VMware Workstation:** *File → Import Appliance* and choose the OVA.
2. Start the VM. Its screen shows **the address of the web console and the setup token**.
3. Open `https://<address>:8443` and continue with [First sign-in](#first-sign-in).

The appliance gets its address by DHCP; set a fixed one in the [setup menu](#the-appliance-setup-menu-and-settings) or later in the console. To give it more space, enlarge the VM disk: the appliance grows its file system at the next start.

### 2. Appliance ISO

1. Create a VM or use a server with 2 vCPU, 2 GB RAM and a 16 GB or larger disk.
2. Boot it from `watchzit-appliance.iso`. After 5 seconds the installation starts by itself:
   - it **erases the first disk**;
   - it installs Debian, PostgreSQL and WatchZit;
   - it takes about 10–20 minutes and needs internet access.
3. After the restart the screen shows the console's address and the setup token, as with the VM image.

### 3. Install script (existing Linux server)

On Debian 12+, Ubuntu 22.04+, AlmaLinux or Rocky Linux 9+ (x86-64), as root:

```
curl -fsSL https://github.com/Max-Zit/watchzit/releases/latest/download/install.sh | sudo sh
```

The script:
- installs PostgreSQL;
- downloads the newest release and checks its signature and checksum;
- installs WatchZit and creates the database with a random password;
- turns on automatic security updates of the operating system;
- opens ports 8443 and 8444 in firewalld or ufw;
- prints the console's address and the setup token.

Options go after `sh -s --`:

| Option | Effect |
|---|---|
| `--version 1.0.0` | install this release instead of the newest |
| `--db postgres://user:pw@host:5432/db` | use an existing PostgreSQL database |
| `--no-auto-updates` | leave the automatic security updates as they are |
| `--source URL` | a mirror or a local folder with the release files |

Running the script again updates WatchZit and keeps the configuration and the data. To read the script first: `curl -fsSLO …/install.sh; less install.sh; sudo sh install.sh`.

### 4. Packages by hand

**Debian / Ubuntu**

```
apt install postgresql
apt install ./watchzit-server_*.deb
```

**AlmaLinux / Rocky Linux**

```
dnf install postgresql-server && postgresql-setup --initdb && systemctl enable --now postgresql
dnf install ./watchzit-server-*.rpm
```

Then create a database and a user for WatchZit, put its address into `/etc/watchzit/server.env` and start the service:

```
sudo -u postgres psql -c "CREATE USER watchzit PASSWORD '<password>'" -c "CREATE DATABASE watchzit OWNER watchzit"
# /etc/watchzit/server.env:  WATCHZIT_DB=postgres://watchzit:<password>@localhost/watchzit
systemctl enable --now watchzit-server
```

On AlmaLinux and Rocky Linux PostgreSQL must accept passwords on 127.0.0.1: change `ident` to `scram-sha-256` in `/var/lib/pgsql/data/pg_hba.conf` and restart PostgreSQL.

## First sign-in

1. Open `https://<server>:8443`. The browser warns once about the console's own certificate; accept it (later you can install a trusted one, see below).
2. Enter the **setup token** (on the appliance's screen, printed by the install script, or in `/var/lib/watchzit/setup-token.txt`) and create the first administrator.
3. Scan the QR code with an authenticator app — two-factor sign-in is required — and keep the recovery codes.
4. Add devices, or let WatchZit discover a network, and draw your first map.

## The appliance: setup menu and settings

On the VM's own screen sign in as **`wzadmin`** (first password `watchzit`, which must be changed at once). A setup menu opens:

| Menu | What |
|---|---|
| Network | DHCP or a fixed address, gateway and DNS; settings that cut the appliance off are undone by themselves |
| Server name | the host name |
| Time zone | e.g. `Europe/Belgrade` (graphs, reports and notifications use it) |
| Console address and setup token | where to open the console |
| Allow the console from all private networks again | when a too narrow allow-list locked you out |
| SSH | on or off, and its port |
| Password of a console user | a new password for e.g. `admin`, and all sign-in blocks lifted |
| Restart / shut down | WatchZit or the whole appliance |

Most of it is also in the web console, for administrators:

- **Settings → Updates and server:** new WatchZit versions (now, or automatically at a chosen hour), installing a release **uploaded as files** on servers without internet access, **operating system updates** (waiting and security updates, install them all, automatic security updates on or off, restart the server), the time zone, the **network of the appliance** (DHCP or fixed address, gateway, DNS, server name) and **SSH** (on or off, port).
- **Settings → Certificate:** the console's own certificate, a certificate you **upload** (PEM, also for an IP address), or one from **Let's Encrypt**, renewed by itself — checked over port 80 or with a DNS record through Cloudflare when the console is not reachable from the internet. Port 80 can also send browsers to HTTPS. Agents keep their own pinned certificate on port 8444.
- **Settings → Network and access:** the console and agent ports and the networks allowed to reach them.

The appliance is Debian with WatchZit, PostgreSQL, automatic security updates, the guest tools of VMware, Proxmox/KVM and Hyper-V, SSH without root sign-in, and no answers to ICMP timestamp requests. The source code of its Debian packages is listed in `/usr/share/doc/watchzit/SOURCE-CODE.txt`.

## Agents

The agent is optional: it reports what the network cannot see — CPU, memory, disks, interfaces and services of Windows and Linux computers. Under **Agents** create an enrollment code, then:

- **Windows:** `msiexec /i watchzit-agent-x64.msi ENROLLCODE="WZ1-…" /qn`
- **Linux:** the Agents page shows a command that downloads the agent from your server, checks it and installs it as the `watchzit-agent` service.

The code holds the server's address and the fingerprint of its agent certificate, so an agent talks only to your server.

## Updates

WatchZit checks for new releases itself and shows them under **Settings → Updates and server**. Only releases signed with the WatchZit release key are installed. Before installing, the server backs up its database and program; if the new version does not start, it goes back to the old one by itself.

## Requirements and ports

| Part | Supported |
|---|---|
| Server | Debian 12+, Ubuntu 22.04+, AlmaLinux / Rocky Linux 9+ (x86-64), PostgreSQL 13+; or the appliance |
| Hypervisors for the appliance | VMware ESXi 6.7+ and Workstation, Proxmox VE / KVM, Hyper-V (generation 1), VirtualBox |
| Agent | Windows 10/11 and Windows Server (x86-64); Linux x86-64 and arm64 with systemd |
| Browsers | current Chrome, Edge, Firefox, Safari |

| Port | Direction | What |
|---|---|---|
| TCP 8443 | browsers → server | web console (can be changed; 443 with a trusted certificate) |
| TCP 8444 | agents → server | agent endpoint |
| TCP 80 | browsers, Let's Encrypt → server | optional: redirect to HTTPS and the Let's Encrypt check |
| ICMP, UDP 161, TCP … | server → devices | the checks you set up (ping, SNMP, HTTP, databases …) |
| UDP 514, UDP 162 | devices → server | optional: syslog messages and SNMP traps |
| TCP 22 | admins → appliance | optional SSH |

## Documentation, support, license

- The **user guide** is built into the console (*User guide* in the menu), in English and Serbian, with a search.
- Questions and ideas: [Discussions](https://github.com/Max-Zit/watchzit/discussions). Bugs: [Issues](https://github.com/Max-Zit/watchzit/issues). Security reports: see [SECURITY.md](SECURITY.md).
- Paid support is offered by [MaxZit](https://maxzit.com).

GNU Affero General Public License v3.0 — see [LICENSE](LICENSE). Third-party components and their licenses: [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
