---
title: Make the Discord bot
description: Create your server's own deploy bot, invite it, and pick its channel and roles.
---

Each server uses its own bot, so the cards carry your server's name and only your Discord can
press the buttons. It takes about five minutes.

## Create it

1. Open the [Discord Developer Portal](https://discord.com/developers/applications) and press
   **New Application**. Name it after your server, for example *Sunset RP Deploys*.
2. On the **Bot** tab, press **Reset Token** and copy the token. The installer asks for it. Keep it
   secret: anyone with it can post as your bot.
3. Still on the **Bot** tab, you can turn **Public Bot** off so nobody else can invite it.
   fxship needs no privileged intents.
4. Optional: set an avatar on the **General Information** tab. Cards show your Discord server's
   icon unless you set `BRAND_ICON_URL`.

## Invite it

The installer prints an invite link once it has the token. To make it yourself, take your
application id from **General Information** and open:

```text
https://discord.com/oauth2/authorize?client_id=APPLICATION_ID&scope=bot+applications.commands&permissions=117760
```

That asks for View Channel, Send Messages, Embed Links, Attach Files and Read Message History, plus
the right to add the `/deploy` command. Nothing else.

## A channel and roles

- **A channel for the cards.** A private `#deploys` channel that your developers and staff can see
  works best. The bot needs to view it, send messages and read its history there.
- **Approver roles** may approve releases for prod, reject them, roll back and apply queued
  releases now. Server administrators always can.
- **Shipper roles** (optional) may ship or discard edits made on dev, which opens a release, but
  can't approve it for prod. Good for trusted staff who aren't developers.

The installer lists your channels and roles and lets you pick them by number. To find ids yourself,
turn on **Developer Mode** (User Settings > Advanced), then right-click a channel or role and
**Copy ID**.

## Check it

After installing, `fxship doctor` checks the token, that the bot can post in the channel, that the
roles exist, and that `/deploy` is registered.
