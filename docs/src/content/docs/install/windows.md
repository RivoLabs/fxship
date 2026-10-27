---
title: Install on Windows Server
description: Set up prod, dev, the database and the deploy agent on Windows Server 2022 or 2025.
---

You need a fresh Windows Server 2022 or 2025 machine, an administrator login (RDP), and the things
on [Before you start](/fxship/start/requirements/).

## Run the installer

Open **PowerShell as administrator** (right-click > Run as administrator) and run:

```powershell
irm https://github.com/RivoLabs/fxship/releases/latest/download/install.ps1 | iex
```

It downloads the latest release, checks it against the release's checksums, and asks the same
questions as the [Ubuntu installer](/fxship/install/linux/#what-it-asks), with two differences:

- **Time zone**: restarts use the machine's own time zone. Set it in Settings > Time & language
  before installing if it's wrong.
- **Monitoring**: Uptime Kuma and Netdata aren't installed on Windows. `fxship doctor` and the
  Discord cards still tell you when something is wrong.

It installs what it needs (Git for Windows, MariaDB, restic and the Visual C++ runtime, each checked
against its publisher's checksum or signature), starts both servers, takes a first backup, and
finishes with [`fxship doctor`](/fxship/guides/monitoring/#fxship-doctor).

## What you end up with

| | |
| --- | --- |
| Prod and dev servers | the `fxship-prod` and `fxship-dev` services, in `C:\fxship\prod` and `C:\fxship\dev` |
| The deploy agent | the `fxship` service |
| Database | the `fxship-mariadb` service, on 127.0.0.1 only |
| Settings | `C:\ProgramData\fxship\fxship.env` (Administrators only) |
| Passwords | `C:\ProgramData\fxship\credentials.txt` (Administrators only) |
| Logs | `C:\ProgramData\fxship\logs` (install, agent, each server's console, backups) |
| fxship itself | `C:\Program Files\fxship`, on the system PATH |

Each game server runs as its own Windows account (`NT SERVICE\fxship-prod`, `NT SERVICE\fxship-dev`)
that can only reach its own folder: a bad resource on dev can't read prod's files, the database
passwords or the deploy key. Dev is held to 2 CPU cores and 4 GB of memory by default
(`DEV_CPU_CORES`, `DEV_MEMORY_MAX_MB`), so it can't slow prod down.

Use `Get-Service fxship*` to see them, and txAdmin to start, stop and restart the game servers:
txAdmin warns players first.

## Change a setting

Run the installer again, from an administrator PowerShell. Every question offers what you answered
last time. It only changes what's different and never restarts a server that's up.

```powershell
irm https://github.com/RivoLabs/fxship/releases/latest/download/install.ps1 | iex
```

## Without questions

Put your answers in a file in the same `KEY="value"` format as `fxship.env` (see
[Settings](/fxship/reference/configuration/)) and pass it in from an unpacked release:

```powershell
.\install.ps1 -Config answers.env
```

`-Upgrade` re-applies everything with the saved settings, which is what `fxship upgrade` runs.

## Windows Update

Windows can restart the machine to finish updates, which takes the servers down with it. In
**Settings > Windows Update > Advanced options**, set active hours around your busiest times, or
schedule updates for just after a scheduled restart.

## Next

- Push a change to your branch and watch the card appear: [Deploying changes](/fxship/guides/deploying/).
- Give staff access to dev: [Staff access](/fxship/guides/staff/#windows-server).
