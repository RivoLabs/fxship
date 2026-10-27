---
title: Backups and restores
description: What fxship backs up every night, where to, and how to get it back.
---

Your resources are in GitHub, so backups cover everything else: the part you can't get back from a
repository.

## What's in a backup

- A dump of each server's database.
- Every `.cfg` file of both servers, `secrets.cfg` included.
- txAdmin's data (admins, bans, warnings, settings), without its logs.
- Files your resources wrote while running: anything git sees as changed or new in the server
  folders (JSON data, uploaded images and the like).
- fxship's own settings.

Backups are made with [restic](https://restic.net): encrypted, deduplicated, and kept as 7 daily,
4 weekly and 6 monthly snapshots. The first one runs during install, then every day at
`BACKUP_TIME` (04:30 by default).

## Where they go

Keep them off the machine: a dead disk or a lost server takes local backups with it.

| Location | `BACKUP_REPO` | Also set |
| --- | --- | --- |
| Amazon S3 or any S3-compatible store | `s3:https://s3.amazonaws.com/bucket/fxship` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` |
| Backblaze B2 | `b2:bucket-name:fxship` | `B2_ACCOUNT_ID`, `B2_ACCOUNT_KEY` |
| Your own server over SFTP | `sftp:user@host:/backups/fxship` | an SSH key for root on Ubuntu that the host accepts |
| A folder on this machine | `/var/backups/fxship/restic` or `C:\fxship-backups` | nothing, but see above |

The installer asks for these. Everything is encrypted with `RESTIC_PASSWORD`, saved in the
credentials file. **Keep a copy of that password somewhere else**: without it the backups can't be
read, by you or anyone.

## Commands

```bash
fxship backup run                         # back up now
fxship backup list                        # the snapshots
fxship backup restore-db prod             # load prod's database from the latest snapshot
fxship backup restore-db dev 4f2a91c      # ...or from a given one
fxship backup restore-files latest /tmp/restore
```

Run them as root on Ubuntu, from an administrator PowerShell on Windows.

`restore-db` replaces the whole database, so it asks you to type its name first. Stop that server
in txAdmin before you run it, and start it again after.

`restore-files` unpacks a snapshot into a folder without touching anything live. Copy back what you
need: a `.cfg`, txAdmin's `admins.json`, a resource's data file.

## Test a restore

A backup you've never restored is a hope. Once after installing, and now and then after that:

```bash
fxship backup restore-files latest /tmp/restore-test
```

and look inside. `fxship doctor` reports a failure when the last backup failed or is more than a day and a half
old.

## Rebuilding a lost machine

1. Get the settings out of the latest snapshot. On any machine with [restic](https://restic.net)
   and the storage keys set as environment variables:

   ```bash
   restic -r BACKUP_REPO restore latest --target ./restore --include '**/fxship.env'
   ```

   It asks for `RESTIC_PASSWORD`. The settings file is inside `./restore`.
2. Install fxship on the new machine with those settings: `--config fxship.env` (Ubuntu) or
   `-Config fxship.env` (Windows). If the machine's IPs changed, change `PUBLIC_IP`, `PROD_IP` and `DEV_IP` in it first.
3. `fxship backup restore-db prod` (and `dev`), then copy back txAdmin's data and any runtime files
   from `fxship backup restore-files latest <folder>`.
