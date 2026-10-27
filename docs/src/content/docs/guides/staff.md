---
title: Staff access
description: Give staff their own login to edit the dev server, and nothing else.
---

Staff get their own login that can change the dev server's files and nothing more: they can't touch
prod, read `secrets.cfg`, or change the settings that decide where dev listens. What they save loads
on dev at once and shows up in Discord to [Ship or Discard](/fxship/guides/dev-edits/).

Code they add runs on dev with dev's own access, so give staff access to people you'd trust with
dev's database. Dev can't reach prod's files or database either way.

## Ubuntu

Staff connect over SSH with their own key, so there's no password to leak. Ask them for their
**public** key (the `.pub` file; `ssh-keygen -t ed25519` makes one), then:

```bash
sudo fxship-add-staff sam 'ssh-ed25519 AAAAC3Nza... sam@laptop'
```

They connect with any SFTP editor: [VS Code](https://code.visualstudio.com) with Remote - SSH,
[WinSCP](https://winscp.net), or FileZilla. On Windows, [SSHFS-Win](https://github.com/winfsp/sshfs-win)
maps it as a drive. Dev's files are in `/srv/fivem/dev/server-data`.

Besides editing dev's files they may start, stop and restart dev, and read both servers' status and
logs:

```bash
sudo systemctl restart fivem@dev
sudo journalctl -u fivem@dev -f
sudo fxship status
```

Prod's files are read-only to them, and prod is restarted from txAdmin, which warns players.

Remove someone with `sudo fxship-remove-staff sam`. Running `fxship-add-staff` again for the same
name replaces their key.

## Windows Server

Staff get a Windows account that can only open the dev server's folder through a file share:

```powershell
fxship staff add sam
```

It prints the share (`\\your-server\fxship-dev`), their login and a generated password: send those
privately. They map it in File Explorer (**This PC > Map network drive**) and edit the files like
any folder on their PC. They can't sign in to the machine itself.

The share is only reachable over Tailscale, or from the IPs you allowed for admin pages. With
Tailscale, invite them to your tailnet first.

`fxship staff list` shows who has access; `fxship staff remove sam` takes it away.

## In Discord

Separately from file access, Discord roles decide who may press what:

- **Shipper roles** may Ship or Discard dev edits (which opens a release), and use `/deploy status`.
- **Approver roles** may also approve and reject releases, roll back, apply now and refresh the dev
  database.

Set them with `DISCORD_SHIPPER_ROLE_IDS` and `DISCORD_APPROVER_ROLE_IDS`, or re-run the installer.
