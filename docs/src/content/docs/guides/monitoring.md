---
title: Monitoring
description: Know when a server is down, and check the whole setup in one command.
---

## fxship doctor

```bash
fxship doctor
```

checks every piece from the settings file outward: the agent, GitHub access (read, and write for
Ship), each server's service, txAdmin login and game port, license keys, that txAdmin's restart
schedule matches fxship's, the Discord bot, channel permissions, roles and `/deploy`, free disk,
the clock, and the last backup. Each problem comes with the fix. It exits non-zero if anything
failed, so it works in your own scripts too.

## Uptime Kuma (Ubuntu)

If you turned it on, [Uptime Kuma](https://github.com/louislam/uptime-kuma) watches:

| Monitor | Alerts |
| --- | --- |
| prod game server | yes, after about 3.5 minutes down |
| prod's txAdmin | yes |
| the fxship agent | yes |
| dev game server and dev's txAdmin | no: dev restarts all the time while you test |

A game server only counts as down after 5 failed checks in a row, so a scheduled restart of a big
server doesn't page anyone. Each alert is sent once when it goes down and once when it's back.

Alerts go to the Discord webhook you gave as `ALERT_WEBHOOK` (Channel settings > Integrations >
Webhooks). Open Kuma at `http://<server>:3001` over Tailscale or from an allowed IP; the login is
`admin` with the password in the credentials file.

## Netdata (Ubuntu)

[Netdata](https://www.netdata.cloud) draws live graphs of CPU, memory, disk and network, per
process too, and raises its own alerts (disk filling up, memory pressure) to the same webhook. Open
it at `http://<server>:19999`.

## Windows Server

Uptime Kuma and Netdata aren't installed on Windows. Run `fxship doctor` when something seems off,
and point any outside uptime checker at `http://<server>:30120/info.json`: it only answers while the
game server is up.
