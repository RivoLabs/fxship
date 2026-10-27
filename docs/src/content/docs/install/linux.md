---
title: Install on Ubuntu 24.04
description: Set up prod, dev, the database and the deploy agent on a fresh Ubuntu 24.04 machine.
---

You need a fresh Ubuntu 24.04 machine (x86_64), root access, and the things on
[Before you start](/fxship/start/requirements/).

## Run the installer

As root (or with `sudo`):

```bash
curl -fsSL https://github.com/RivoLabs/fxship/releases/latest/download/install.sh | sudo bash
```

It downloads the latest release, checks it against the release's checksums, and starts asking
questions. Every question has a default in brackets: press Enter to take it.

## What it asks

1. **Your server**: its name (shown on cards and in txAdmin), the time zone your restarts are in,
   one IP, two IPs or prod only, and prod's scheduled restart times. Changes that need a restart
   land at these.
2. **Your GitHub repository**: `owner/name` and the branch to deploy. It makes a deploy key and
   shows it; add it on GitHub under **Settings > Deploy keys** with **Allow write access** ticked
   (staff need that to ship edits from Discord), then press Enter. It then fetches the repository
   and shows what it found: layout, framework, core and streaming resources.
3. **Server config** (resources-only repositories): the folder of your current `server.cfg` on
   this machine, or `starter` for a minimal one you edit later.
4. **License keys** for prod and, if you want, a separate one for dev.
5. **txAdmin**: the admin username. Passwords are generated.
6. **Database**: whether to run MariaDB here, the database names, and an optional `.sql` or
   `.sql.gz` dump to import into prod. Dev starts as a copy of prod.
7. **Discord**: the bot token, then the channel and roles from a numbered list.
8. **Backups**, **monitoring** (Uptime Kuma, Netdata) and **admin access** (Tailscale, or the IPs
   allowed to reach txAdmin).

Then it installs everything, starts both servers, takes a first backup, and finishes with
[`fxship doctor`](/fxship/guides/monitoring/#fxship-doctor), which checks every piece.

## What you end up with

| | |
| --- | --- |
| Prod and dev servers | `fivem@prod` and `fivem@dev` systemd services, in `/srv/fivem/prod` and `/srv/fivem/dev` |
| The deploy agent | the `fxship` service |
| Database | MariaDB 12 in Docker, on 127.0.0.1 only |
| Settings | `/etc/fxship/fxship.env` (root only) |
| Passwords | `/root/fxship-credentials.txt` (root only) |
| Install log | `/var/log/fxship-install.log` |

## Change a setting

Run the installer again. Every question offers what you answered last time, so press Enter through
the ones you keep. It only changes what's different and never restarts a server that's up: changes
to a running server apply at its next restart.

```bash
curl -fsSL https://github.com/RivoLabs/fxship/releases/latest/download/install.sh | sudo bash
```

That also brings fxship up to the latest release. To stay on the version you have, run the copy it
installed from instead: `sudo bash /opt/fxship/releases/<version>/install.sh`.

## Without questions

Put your answers in a file in the same `KEY="value"` format as `/etc/fxship/fxship.env` (see
[Settings](/fxship/reference/configuration/)) and pass it in:

```bash
sudo bash install.sh --config answers.env
```

Settings you leave out keep their saved value or default; passwords are generated once and kept.
`--upgrade` re-applies everything with the saved settings, which is what `fxship upgrade` runs.

## Next

- Push a change to your branch and watch the card appear: [Deploying changes](/fxship/guides/deploying/).
- Give staff access to dev: [Staff access](/fxship/guides/staff/).
