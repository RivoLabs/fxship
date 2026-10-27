---
title: Upgrading and removing
description: Keep fxship and FXServer up to date, or take fxship off a machine.
---

## fxship

```bash
fxship upgrade --check     # is there a newer release?
fxship upgrade             # install it
```

It downloads the release, checks it against the release's checksums, and runs its installer with
your saved settings. The agent restarts; the game servers don't, and nothing changes for players.
Releases and their notes are on
[GitHub](https://github.com/RivoLabs/fxship/releases).

## FXServer

Set `FX_BUILD` to `recommended` (the default), `latest` or a build number, and re-run the installer.
The new build is downloaded next to the old one. txAdmin's scheduled restarts reuse the FXServer
that's running, so each server switches to the new build when its service restarts: after a reboot,
or when you restart it at a quiet moment (`sudo systemctl restart fivem@prod`, or
`Restart-Service fxship-prod` on Windows; this disconnects players). Do dev first and play on it for
a bit. To go back, set the old build number and re-run.

## Removing fxship

fxship never deletes your data. To take it off a machine:

**Ubuntu**

```bash
sudo systemctl disable --now fxship fivem@prod fivem@dev fxship-backup.timer fxship-dev-perms.timer
sudo docker compose -p fxship down            # MariaDB and Kuma; add -v to delete their data too
sudo rm /etc/systemd/system/fxship* /etc/systemd/system/fivem@* /usr/local/bin/fxship
```

**Windows**

```powershell
Stop-Service fxship, fxship-prod, fxship-dev, fxship-mariadb
sc.exe delete fxship; sc.exe delete fxship-prod; sc.exe delete fxship-dev; sc.exe delete fxship-mariadb
schtasks /Delete /TN fxship-backup /F
Get-NetFirewallRule -Group fxship | Remove-NetFirewallRule
```

Your servers' files (`/srv/fivem` or `C:\fxship`), the settings and the backups stay where they are
until you delete them.
