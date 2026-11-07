# Security policy

## Supported versions

Security fixes are released for the newest release only. Please update to it (the console can update itself) before reporting a problem; older releases do not get fixes.

## Reporting a vulnerability

Please report security problems privately, not in a public issue:

1. Open the [Security tab](https://github.com/Max-Zit/watchzit/security) of this repository.
2. Click **Report a vulnerability** and describe the problem, the affected version and how to reproduce it.

We confirm reports within a few working days, fix confirmed problems in a new release and credit the reporter in the release notes unless they prefer otherwise.

## How WatchZit is tested

**On every change** (GitHub Actions):

- go vet, the test suite, and the whole suite again with the Go **race detector**;
- **fuzzing** of every parser that reads untrusted input — watchzit:// links, enrollment codes, SSO state and SAML metadata, SNMP answers and traps, syslog, NetFlow/IPFIX/sFlow, JSON data sources, network allow-lists, web scenario steps and stored measurements;
- CodeQL, govulncheck, staticcheck, Dependabot, secret scanning with push protection and OpenSSF Scorecard.

**Before every release**, in a test lab:

- source code: gosec, Semgrep, Trivy and Gitleaks;
- the running console: an authenticated OWASP ZAP scan, a crawl of every page as administrator, operator and viewer, and a check that every write or admin route refuses roles that may not use it;
- the appliance image: Trivy over its operating-system packages, a Lynis hardening audit, testssl.sh on the console (8443) and agent (8444) ports, and Nuclei;
- end-to-end tests against real devices (MikroTik, SNMP agents, Windows and Linux agents, remote collectors).

Findings are fixed before the release or listed in its notes.
