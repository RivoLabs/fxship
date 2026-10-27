---
title: Settings
description: Every setting in fxship.env, what it does and its default.
---

Everything lives in one file: `/etc/fxship/fxship.env` on Ubuntu, `C:\ProgramData\fxship\fxship.env`
on Windows. Each line is `KEY="value"`. The installer writes it, and every question it asks maps to
one of these.

**To change a setting**, re-run the installer: it offers each saved answer as the default and
applies what changed. For settings only the agent reads (most of *Deploys*, *Dev edits* and
*Discord* below), you can also edit the file and restart the agent: `sudo systemctl restart fxship`
or `Restart-Service fxship`. Restarting the agent never restarts a game server.

## Servers

| Setting | Default | |
| --- | --- | --- |
| `BRAND_NAME` | | Your server's name, on cards, in txAdmin and the dev server's listing (with " [DEV]"). |
| `BRAND_ICON_URL` | your Discord server's icon | Image shown on cards. |
| `POWERED_BY` | `true` | The small "Powered by Rivo" line at the foot of cards. |
| `TZ` | the machine's | Time zone of `RESTART_TIMES` (Ubuntu; Windows uses the machine's). |
| `TOPOLOGY` | `one-ip` | `one-ip`, `two-ip` or `prod-only`. |
| `PUBLIC_IP`, `PROD_IP`, `DEV_IP` | | The IPs players connect to. With `two-ip`, each server binds only its own. |
| `PROD_GAME_PORT`, `DEV_GAME_PORT` | `30120`, `30130` | Game ports (both 30120 with `two-ip`). |
| `DEV_ENABLED` | `true` | `false` on prod-only boxes. |
| `RESTART_TIMES` | `06:00,18:00` | Prod's scheduled restarts, written into txAdmin. Changes that need a restart land at these. |
| `FX_BUILD` | `recommended` | FXServer build: `recommended`, `latest` or a build number. |
| `DEV_CPU_CORES`, `DEV_MEMORY_MAX_MB` | `2`, `4096` | Dev's hard CPU and memory cap. |
| `TX_USER` | `admin` | txAdmin admin login for both servers. |
| `PROD_TX_PASSWORD`, `DEV_TX_PASSWORD` | generated | txAdmin passwords. fxship signs in with them too. |
| `PROD_LICENSE_KEY`, `DEV_LICENSE_KEY` | | Cfx.re keys. Dev uses prod's if empty. |
| `CFG_SOURCE` | `starter` | Where `server.cfg` came from on first install (resources layout). |

## Repository

| Setting | Default | |
| --- | --- | --- |
| `REPO_URL` | | `git@github.com:owner/name.git`. |
| `REPO_BRANCH` | `main` | The branch fxship deploys. |
| `REPO_LAYOUT` | detected | `resources` or `server-data`. |
| `GIT_SSH_COMMAND` | set by the installer | How git reaches GitHub with the deploy key. |
| `POLL_INTERVAL` | `60s` | How often GitHub is checked for new commits. |

## Deploys

| Setting | Default | |
| --- | --- | --- |
| `CORE_RESOURCES` | the common frameworks | Resources whose changes always wait for a restart. The default covers Qbox, QBCore, ESX, ox, ND, vRP, database and voice resources; replacing it replaces the whole list. |
| `HOT_RESOURCES` | | Resources that always go live, whatever detection says (deleting one still waits). |
| `COLD_PREFIXES` | | Folders whose resources always wait, from the top of the repository: `[maps]/,[cars]/`, or `resources/[maps]/` in the server-data layout. |
| `APPLY_LEAD` | `90s` | How long before a scheduled restart queued releases are checked out. |
| `DEV_SETTLE` | `20s` | How long dev's console is read after loading a change. |
| `SUPERSEDED_CARDS` | `delete` | `delete` or `keep` the card of a release a newer push replaced before anyone decided. |

## Dev edits

| Setting | Default | |
| --- | --- | --- |
| `DEV_IGNORE` | `**/output/**,**/*.log,**/cache/**` | Files on dev that are never edits. |
| `DEV_WATCH` | `3s` | How often dev's files are checked for edits. |
| `DEV_DEBOUNCE` | `3s` | How long edits must sit still before dev reloads. |
| `DEV_EDIT_SETTLE` | `5s` | How long dev's console is read after reloading an edit. |
| `SHIP_AUTHORS` | | Git identities for Ship: `discordname=Full Name <email>;other=...` |
| `SHIP_EMAIL_DOMAIN` | `users.noreply.fxship.invalid` | Email domain for shippers not in `SHIP_AUTHORS`. |

## Discord

| Setting | Default | |
| --- | --- | --- |
| `DISCORD_TOKEN` | | The bot token. Empty: no cards, approve from the command line. |
| `DISCORD_CHANNEL_ID` | | Channel for the cards. |
| `DISCORD_APPROVER_ROLE_IDS` | | Roles that may approve, reject, roll back, apply now and refresh dev's database. Server admins always can. |
| `DISCORD_SHIPPER_ROLE_IDS` | | Roles that may also ship or discard dev edits. |
| `DISCORD_SLASH_COMMAND` | `deploy` | Name of the slash command; `none` registers none. |

## Database

| Setting | Default | |
| --- | --- | --- |
| `DB_INSTALL` | `true` | Run MariaDB on this machine. |
| `DB_IMPORT` | | A `.sql` or `.sql.gz` loaded into prod's empty database on install. |
| `DB_PROD_NAME`, `DB_PROD_USER`, `DB_PROD_PASSWORD` | from `BRAND_NAME`, generated | Prod's database and its user. |
| `DB_DEV_NAME`, `DB_DEV_USER`, `DB_DEV_PASSWORD` | from `BRAND_NAME`, generated | Dev's. |
| `DB_HOST`, `DB_PORT` | `127.0.0.1`, `3306` | Where the databases are, for refreshes and backups. |
| `DB_SCRUB_SQL` | `dev-db-scrub.sql` next to `fxship.env` | SQL run on dev's database after each refresh, if the file exists. |
| `MARIADB_ROOT_PASSWORD` | generated | |

## Backups

| Setting | Default | |
| --- | --- | --- |
| `BACKUP_ENABLED` | `true` | |
| `BACKUP_REPO` | a local folder | restic repository: `s3:...`, `b2:...`, `sftp:...` or a folder. |
| `BACKUP_TIME` | `04:30` | Daily backup time. |
| `RESTIC_PASSWORD` | generated | Encrypts the backups. Keep a copy elsewhere. |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | | For `s3:` locations. |
| `B2_ACCOUNT_ID`, `B2_ACCOUNT_KEY` | | For `b2:` locations. |

## Access and monitoring

| Setting | Default | |
| --- | --- | --- |
| `TAILSCALE` | `true` | Admin pages and the dev file share only over Tailscale. |
| `TAILSCALE_AUTHKEY` | | Joins the tailnet without the login link (for unattended installs). |
| `ADMIN_ALLOW_IPS` | your SSH client's IP (Ubuntu) | Without Tailscale: IPs allowed to reach admin pages. |
| `MONITOR_KUMA`, `MONITOR_NETDATA` | `true`, `false` | Ubuntu only. |
| `ALERT_WEBHOOK` | | Discord webhook for Kuma and Netdata alerts. |
| `KUMA_PASSWORD` | generated | Uptime Kuma's `admin` password. |
| `DOKPLOY` | `false` | Ubuntu: MariaDB and Kuma run as Dokploy apps instead of fxship's own compose project. |

## Set by the installer

You shouldn't need to touch these: `API_TOKEN` and `API_ADDR` (the agent's local API, default
`127.0.0.1:7070`), `FIVEM_ROOT`, `PROD_WORKTREE` and `DEV_WORKTREE`, `PROD_INFO_URL` and
`DEV_INFO_URL`, `REPO_DETECTED`, `TS_NAME`, and on Windows `FXSHIP_PATH`. For `fxship upgrade`
from a private fork, set `UPGRADE_REPO` (owner/name) and `GITHUB_TOKEN`.
