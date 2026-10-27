---
title: Troubleshooting
description: What to do when something isn't right.
---

Start with `fxship doctor`. It checks everything and says how to fix each problem it finds.

## No card after a push

- Did the push go to the branch fxship deploys (`REPO_BRANCH`)?
- `fxship poll`, then `fxship status`: if the release is there, it's a Discord problem (next
  section); if not, check `fxship doctor`'s **github read** line.
- The agent's log says what it's doing: `journalctl -u fxship -n 100` (Ubuntu) or
  `C:\ProgramData\fxship\logs\fxship.log` (Windows).

## Discord

| Symptom | Fix |
| --- | --- |
| No cards at all | `fxship doctor`'s discord lines. Usually the bot can't see the channel or lacks Send Messages there. |
| `/deploy` missing | Re-invite the bot with the `applications.commands` scope ([the invite link](/fxship/guides/discord-bot/#invite-it)), then restart the agent. |
| "You don't have a deploy approver role" | Add the role's id to `DISCORD_APPROVER_ROLE_IDS`, or give the person that role. |
| Buttons say "This interaction failed" | The agent was down when it was pressed. Check it's running and press again. |

## Dev says a change failed

The card shows the syntax error with file and line, or the console errors that are new since the
change. Open dev's console in txAdmin to see them in context. Errors dev already printed before the
change don't count, so what's on the card came with it.

## A change that should be live waits for a restart, or the other way round

The card says why each resource waits. Override it with `HOT_RESOURCES` (always live) or
`CORE_RESOURCES` / `COLD_PREFIXES` (always wait). `fxship detect <folder>` shows what detection
sees.

## Dev keeps reloading, or a card lists files nobody edited

A resource is writing into its own folder while it runs. fxship stops treating a file as an edit
once it changes 8 times in a minute, but files written less often look like edits. Add them to
`DEV_IGNORE`, for example `**/data/*.json`, and restart the agent.

## A server won't start

- txAdmin's console for that server shows FXServer's own error.
- **License key**: `sv_licenseKey` in that server's `secrets.cfg` must be a valid key from
  portal.cfx.re.
- **Port in use**: another program on 30120/30130, or on two-IP setups an IP that isn't on the
  machine.
- **Database**: `mysql_connection_string` in `secrets.cfg`, and on Ubuntu `docker ps` shows
  `fxship-mariadb-1` running; on Windows `Get-Service fxship-mariadb`.

## Ship fails

| Message | Fix |
| --- | --- |
| "the server's deploy key is read only" | On GitHub, delete the deploy key and add it again with **Allow write access** ticked. |
| "a newer push to GitHub changes the same lines" | Discard, then redo the edit on top of the new version. |

## Restart times are off by hours

txAdmin's schedule and fxship's must agree, in the same time zone. `fxship doctor` compares them.
On Ubuntu `TZ` sets both; on Windows it's the machine's time zone (Settings > Time & language), and
the services need a restart after changing it.

## Still stuck

Open an issue at [github.com/RivoLabs/fxship](https://github.com/RivoLabs/fxship/issues) with the
output of `fxship doctor` and the relevant log lines. Take out IPs and anything secret first.
