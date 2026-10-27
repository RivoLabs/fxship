---
title: Deploying changes
description: From a push to GitHub to players seeing it, and what to do when something goes wrong.
---

## Push, test, approve

1. **Push to your branch.** fxship checks GitHub every minute (`fxship poll` or `/deploy poll` checks
   now).
2. **Dev gets it first.** fxship checks out the new commit on dev, syntax-checks every changed Lua
   file, then reloads the changed resources (or restarts dev if a change needs it) and reads dev's
   console for errors that weren't there before. Errors dev already had don't count against you.
3. **A card appears in Discord** showing the commits and who made them, the resources that change
   and whether each goes live or waits for a restart, the syntax check, the dev result, and a link
   to the diff on GitHub.
4. **Someone with an approver role presses Approve** (or runs `fxship approve`). **Reject** leaves
   prod as it is; the next push makes a new card.

If you push again before anyone decides, the new card covers both pushes and the old card is
removed, so the channel only ever has one release waiting.

## Live now, or at the next restart

Most changes go live on prod the moment they're approved: fxship checks out the commit and restarts
only the changed resources, so players stay connected. Some changes can't be loaded into a running
server safely, and those wait for prod's next scheduled restart:

| Waits for a restart | Why |
| --- | --- |
| Streamed files: anything with a `stream/` folder, `data_file` entries, `this_is_a_map` or `replace_level_meta` | the game client only loads them on connect |
| Core resources: your framework, inventory, target, database and voice resources | everything else depends on them |
| `server.cfg` and other `.cfg` files (server-data layout) | read once at startup |
| Deleted resources | other resources may still use them |

A card that waits says so, and when: about 90 seconds before the restart (`APPLY_LEAD`), fxship
checks out the commit so the restart starts with it. If more releases are approved before then,
they all land at that restart. Changed your mind? **Cancel** on the card (or `fxship reject`) takes
a queued release back off the restart.

To decide yourself what waits, see `CORE_RESOURCES`, `HOT_RESOURCES` and `COLD_PREFIXES` in
[Settings](/fxship/reference/configuration/#deploys).

## Can't wait for the restart

`/deploy apply-now` (or `fxship apply-now`) lands the queued release now: players get a
60-second warning, then prod restarts with it. It asks you to confirm first.

## Undo a release

`/deploy rollback <commit>` (or `fxship rollback <sha>`) moves prod back to an earlier commit. It
counts as already approved: if the way back is hot it happens at once, otherwise at the next restart.
The commits you rolled back stay in GitHub; push a revert when you've fixed them, and that goes
through dev and approval like any change.

## Prod only

On a box without a dev server, the card says nothing was tested live, and an approval goes straight
to prod. The syntax check still runs.

## When dev finds a problem

The card says what dev saw: the syntax error with file and line, or the new console errors.
Approve stays available, because sometimes an error is expected (a missing config on dev, say), but
think twice. Usually you fix it, push again, and the new card replaces the old one.
