---
title: How it works
description: The moving parts, the files on disk, and why fxship is built the way it is.
---

## The pieces

- **Two FXServers with txAdmin**, prod and dev, each a service of its own. txAdmin runs the game
  server, its restart schedule and its admin pages; fxship talks to txAdmin's web API the same way
  you do in the browser.
- **The agent** (`fxship daemon`), a small Go program running as a service. It polls GitHub, moves
  the servers between commits, runs the checks, posts the Discord cards and answers the buttons. It
  has a local API on 127.0.0.1 that the `fxship` command uses.
- **One clone of your repository** (`repo.git`), with each server's folder checked out from it as a
  git worktree. A deploy is a checkout plus restarting the right resources, so it takes seconds even
  for a very large repository.

## On disk

| | Ubuntu | Windows Server |
| --- | --- | --- |
| Servers | `/srv/fivem/prod`, `/srv/fivem/dev` | `C:\fxship\prod`, `C:\fxship\dev` |
| Each server's files | `<server>/server-data` | `<server>\server-data` |
| txAdmin data | `<server>/txData` | `<server>\txData` |
| The repository | `/srv/fivem/repo.git` | `C:\fxship\repo.git` |
| fxship's state | `/srv/fivem/deploy/state.json` | `C:\fxship\deploy\state.json` |
| FXServer builds | `/opt/fivem/artifacts` | `C:\fxship\artifacts` |
| Settings | `/etc/fxship/fxship.env` | `C:\ProgramData\fxship\fxship.env` |
| Logs | `journalctl -u fxship`, `-u fivem@prod` | `C:\ProgramData\fxship\logs` |

In the resources layout each server's `server-data/resources` is the checkout. In the server-data
layout `server-data` itself is.

## server.cfg

Each server starts from `fxship.cfg`, which fxship writes:

```cfg
exec instance.cfg       # this server's endpoints, name and listing
exec secrets.cfg        # this server's license key, database and other secrets
exec server.fxship.cfg  # your server.cfg, minus the lines above
```

- `instance.cfg` and `secrets.cfg` are written by the installer between `# BEGIN fxship` and
  `# END fxship` markers. Lines you add outside the markers are kept. Neither goes in git.
- `server.fxship.cfg` is your `server.cfg` with the per-server and secret lines commented out, each
  with a note saying where its value comes from now. fxship regenerates it within seconds whenever
  `server.cfg` changes.

So you keep editing one `server.cfg`, shared by both servers, and dev can never pick up prod's port,
database, license listing or Tebex secret from it.

## Live or restart

For each changed resource, in this order:

1. a `.cfg` file in the server-data layout: restart (read at startup);
2. a deleted resource: restart;
3. in `HOT_RESOURCES`: live;
4. in `CORE_RESOURCES`: restart;
5. streams assets (a `stream/` folder, `data_file`, `this_is_a_map` or `replace_level_meta` in its
   manifest): restart;
6. under a `COLD_PREFIXES` folder: restart;
7. anything else: live, with `refresh` and `ensure`.

A release is live only if all of its resources are.

## Telling a new error from an old one

Before loading a change, fxship knows which errors dev's console already prints. After loading it,
only errors it hasn't seen before count. So a server with a few noisy resources still gets a clean
result when a change is fine, and a real new error isn't lost among the old ones.

## Security

- The settings file, the credentials file and the deploy key are readable by root (Ubuntu) or
  SYSTEM and Administrators (Windows) only.
- txAdmin, Uptime Kuma, Netdata and the Windows file share are only reachable over Tailscale or from
  the IPs you list. MariaDB and the agent's API listen on 127.0.0.1 only.
- Each game server runs as its own account (`fivem` and `fivem-dev` on Ubuntu, `NT SERVICE\fxship-prod`
  and `NT SERVICE\fxship-dev` on Windows). Dev's can't open prod's folder, and dev is held to its CPU
  and memory cap.
- Staff can change dev's files, never prod's, and can't read `secrets.cfg` or change where dev
  listens. Code they add does run on dev with dev's access, though, so dev's license key and
  database password are only as private as dev is: that's why dev has its own database user.
- Downloads (FXServer aside, which Cfx doesn't publish checksums for) are checked against their
  publisher's checksums or signatures. fxship's own releases are checked against the release's
  `checksums.txt`.

## Decisions

**Why a dev server on the same machine?** It tests the change against the same FXServer build, the
same resources and a copy of the same data, for the cost of some RAM. Testing on a different machine
tests a different server.

**Why not restart prod on every deploy?** Restarts kick everyone. Most changes don't need one, and
the ones that do can wait a few hours for the restart you already have.

**Why GitHub and not a file upload?** History, blame, review and rollback come for free, and the
deploy key means the machine never holds anyone's personal credentials.

**Why txAdmin's API and not rcon?** txAdmin is already there, knows when the server is up, owns the
restart schedule, and warns players before a restart. fxship works with it rather than around it.
