---
title: Editing on dev
description: Change files on the dev server directly, see them live, then ship them to GitHub from Discord.
---

Not everyone on your team uses git, and nobody wants to download a 15 GB repository to change a
price. So staff can edit the dev server's files directly, and fxship turns those edits into a
proper commit when they're happy.

## How it goes

1. A staff member opens dev's files ([how they get access](/fxship/guides/staff/)) and saves a change.
2. Within a few seconds fxship notices, syntax-checks it, and restarts the resources it touched on
   dev. A change to a `.cfg` file restarts dev.
3. A **Dev edits** card appears in Discord listing the changed files and who made new ones, the
   syntax check, and what dev's console said. More saves update the same card.
4. When it works, someone presses **Ship to prod...**, says what changed ("Fixed shop prices"), and
   fxship commits the edits to GitHub under that person's name and opens a normal release card.
   An approver still has to approve it for prod.
5. Or **Discard** puts every edited file back to what GitHub has and reloads dev.

Approvers, server administrators and the shipper roles may press Ship and Discard. From the
command line: `fxship ship "what changed"` and `fxship discard`.

## Whose name goes on the commit

The commit's author is whoever pressed Ship: their Discord name at `SHIP_EMAIL_DOMAIN`, or their
real git identity if you map it with `SHIP_AUTHORS`:

```bash
SHIP_AUTHORS="sam=Sam Carter <sam@example.com>;alex=Alex Diaz <12345+alex@users.noreply.github.com>"
```

Use the GitHub no-reply address if you want the commits linked to their GitHub profile.

## If someone pushes meanwhile

Ship replays the edits on top of whatever is on GitHub now, so nobody's work is lost. If the same
lines changed on both sides, nothing is shipped and Ship says so: Discard and redo the edit on top of
the new version.

Pushes from GitHub still land on dev while edits are open, and the edits stay. If a push changes a
file someone is editing, dev keeps the edit and holds the push back: its release card says so, and
dev catches up once the edits are shipped or discarded.

## Files resources write themselves

Some resources save data into their own folder while running: logs, caches, JSON databases. These
aren't edits. fxship ignores `**/output/**`, `**/*.log` and `**/cache/**` by default, and when a file
changes on its own 8 times within a minute it's treated as runtime data for the next five
minutes and the card says so. Add your own patterns with `DEV_IGNORE`.
