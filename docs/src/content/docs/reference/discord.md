---
title: Discord
description: The cards, the buttons, the /deploy command, and who may use what.
---

## Release cards

One card per release, edited in place as it moves along: *Working...*, *Waiting for approval*,
*Approved, waiting for the restart*, *Live on prod*, *Rejected*, *Superseded* or *Failed*. A card
shows:

- the commits, linked to GitHub, and who made them;
- what goes live now and what waits for a restart, and why;
- the syntax check and what dev saw when it loaded the change;
- the number of files and lines changed, prod's commit before and after, and **View diff**.

Buttons: **Approve for prod** and **Reject** while it waits, **Cancel** once it's queued for a
restart.

## Dev edit cards

When files change on dev, one card lists them, with the syntax check and dev's reaction, and offers
**Ship to prod...** (asks what changed, then commits) and **Discard**. See
[Editing on dev](/fxship/guides/dev-edits/).

## /deploy

Registered in your Discord server when the agent starts. Answers are only visible to you, and it only
works in the deploy channel.

| | Who | |
| --- | --- | --- |
| `/deploy status` | shippers and approvers | What prod and dev run and what's waiting. |
| `/deploy poll` | approvers | Check GitHub now. |
| `/deploy rollback commit:<sha>` | approvers | Move prod back to an earlier commit. |
| `/deploy apply-now` | approvers | Land the queued release now (asks to confirm). |
| `/deploy refresh-dev-db` | approvers | Copy prod's database over dev's (asks to confirm). Only when fxship runs the database. |

Server administrators can always do everything. Rename the command with `DISCORD_SLASH_COMMAND`.

## Staying within Discord's limits

However busy the server gets, fxship stays well inside Discord's rate limits:

- each card is edited at most once every 4 seconds, and the channel at most about once a second;
  only the newest state of a card is sent;
- a release replaced by a newer push before anyone decided has its card deleted
  (`SUPERSEDED_CARDS=keep` greys it out instead);
- a file a resource rewrites on its own doesn't produce card after card (see
  [Editing on dev](/fxship/guides/dev-edits/#files-resources-write-themselves)).

Deploys never wait on Discord: if it's unreachable, releases carry on, `fxship status` shows where
they are, and approvals work from the command line.
