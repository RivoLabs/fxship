---
title: Commands
description: Everything the fxship command does.
---

Run `fxship` as root on Ubuntu (`sudo fxship ...`), or from an administrator PowerShell on Windows:
it reads the settings file, which only they can.

## Deploys

These talk to the running agent.

| Command | |
| --- | --- |
| `fxship status` | What prod and dev run, recent releases, and any open dev edits, as JSON. |
| `fxship approve [id]` | Approve the waiting release (or the one given) for prod. |
| `fxship reject [id]` | Reject a waiting release, or take a queued one off the next restart. |
| `fxship rollback <sha>` | Move prod back to an earlier commit. Counts as approved. |
| `fxship apply-now` | Land the queued release now: 60-second warning to players, then prod restarts. |
| `fxship poll` | Check GitHub for new commits now instead of within the minute. |
| `fxship ship "message"` | Commit the edits made on dev, push them, and open a release. |
| `fxship discard` | Put every edited file on dev back to what GitHub has. |
| `fxship db-refresh` | Replace dev's database with a copy of prod's. Dev is stopped meanwhile. |

Releases, edits and refreshes started here show in Discord like any other, credited to your login.

## The machine

| Command | |
| --- | --- |
| `fxship doctor` | Check the whole setup and say how to fix what's wrong. `--json` for scripts. Exits 1 if anything failed. |
| `fxship detect <folder>` | Show the layout, framework, core and streaming resources fxship sees in a folder. `--json` for scripts. |
| `fxship backup run` | Back up now. |
| `fxship backup list` | List the backups. |
| `fxship backup restore-db <prod\|dev> [snapshot]` | Replace a database with its copy from a backup (default: the latest). |
| `fxship backup restore-files <snapshot> <folder>` | Unpack a backup into a folder. |
| `fxship upgrade` | Update to the latest release (`--check` only says if there's one; `--version v1.2.3` picks one). |
| `fxship version` | Print the version. |

## Staff

| | |
| --- | --- |
| Ubuntu | `fxship-add-staff <name> '<ssh public key>'`, `fxship-remove-staff <name>` |
| Windows | `fxship staff add <name>`, `fxship staff remove <name>`, `fxship staff list` |

See [Staff access](/fxship/guides/staff/).

## Used by the services and installers

`fxship daemon` is the agent itself, and `fxship fxserver <prod|dev>` runs a game server on Windows;
their services start them. `fxship install` is the Windows installer, which `install.ps1` runs.
`fxship cfg-sanitize <folder>` regenerates `server.fxship.cfg` from `server.cfg` (the agent does
this whenever `server.cfg` changes).
