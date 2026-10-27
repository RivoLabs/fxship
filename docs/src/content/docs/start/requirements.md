---
title: Before you start
description: The machine, accounts and keys fxship needs.
---

## A machine for the servers

fxship takes over a whole machine: prod, dev, the database and the deploy agent all run on it.
Start from a fresh install.

| | Minimum | Comfortable |
| --- | --- | --- |
| System | Ubuntu 24.04 LTS, or Windows Server 2022 / 2025 | |
| CPU | 4 cores | 8 or more |
| Memory | 8 GB | 16 GB or more |
| Disk | 60 GB SSD | 150 GB NVMe |

Dev is capped at 2 cores and 4 GB by default, so it can never starve prod. Change that with
`DEV_CPU_CORES` and `DEV_MEMORY_MAX_MB` in the [settings](/fxship/reference/configuration/#servers).
Without a dev server (prod only) the minimum drops to what your prod server needs on its own.

Pick how the servers face players:

- **One IP** (most servers): prod on port 30120, dev on 30130.
- **Two IPs**: each server on its own IP, both on 30120. The second IP must already be routed to
  the machine by your host.
- **Prod only**: no dev server. Approved changes go straight to prod without a test.

## Accounts and keys

- **A GitHub repository** with your server's resources. If it isn't in git yet,
  [put it there first](/fxship/guides/server-in-git/). Private repositories are fine: the
  installer gives you a deploy key to add to it.
- **A Cfx.re license key** for each server from [portal.cfx.re](https://portal.cfx.re). Dev can share
  prod's key; its own key keeps it off prod's server listing.
- **A Discord bot** for the approval cards. [Making one](/fxship/guides/discord-bot/) takes five
  minutes. You can skip it and approve from the command line.
- **Optional:** a [Tailscale](https://tailscale.com) account (free) so txAdmin and the dev files
  are only reachable over your private network, and somewhere off the machine for backups
  (S3, Backblaze B2 or an SFTP server).

## Ports

| Port | What | Who can reach it |
| --- | --- | --- |
| 30120 TCP and UDP | prod game server | everyone |
| 30130 TCP and UDP | dev game server (one-IP setups) | everyone |
| 40120, 40130 TCP | txAdmin for prod and dev | Tailscale, or the IPs you list |
| 3001 TCP | Uptime Kuma (Linux, optional) | Tailscale, or the IPs you list |
| 19999 TCP | Netdata (Linux, optional) | Tailscale, or the IPs you list |
| 445 TCP | dev file share (Windows) | Tailscale, or the IPs you list |
| 22 TCP | SSH (Linux) | everyone, keys only once you add one |

The installer opens these in the machine's firewall (ufw on Ubuntu, Windows Firewall on Windows)
and leaves any rules you added yourself alone. If your host has its own firewall in front of the
machine, open the game ports there too.
