---
title: Put your server in git
description: Move an existing FiveM server into a private GitHub repository, with secrets left out.
---

fxship deploys whatever is in one branch of a GitHub repository. If your server isn't in git yet,
this page gets it there. It takes about fifteen minutes, most of it uploading.

## Pick a layout

fxship understands two shapes of repository. The installer works out which one you have.

**Resources only** (the most common). The repository *is* your `resources` folder: the folders
like `[qb]` or `[standalone]` sit at the top of it. `server.cfg` stays on the machine, and the
installer copies it there from your old server.

```text
my-server-resources/
├── [core]/
├── [jobs]/
├── [maps]/
└── my-hud/
```

**Whole server-data**. The repository holds `server.cfg` and the `resources` folder beside it.
Pick this if you want config changes to go through approval too.

```text
my-server/
├── server.cfg
├── resources.cfg
└── resources/
    ├── [core]/
    └── ...
```

In this layout fxship takes the lines that must differ between prod and dev, or must stay secret,
out of `server.cfg` before a server reads it: endpoints, `sv_hostname`, `sv_licenseKey`,
`mysql_connection_string`, `rcon_password`, `sv_tebexSecret`, `sv_listingIpOverride`, `sv_master1`
and `onesync` (txAdmin owns that one). Each server gets its own values from files that never go in
git. See [How it works](/fxship/reference/how-it-works/#servercfg).

## Keep secrets out

Anything in the repository can be read by anyone with access to it, forever, even after you
delete it. Before your first commit:

1. Move these out of every `.cfg` you commit: `sv_licenseKey`, `mysql_connection_string`,
   `rcon_password`, `sv_tebexSecret`, `steam_webApiKey`, Discord bot tokens, webhook URLs and any
   API keys. fxship puts the license key and database in each server's own `secrets.cfg`; put the
   rest there yourself after installing (it's kept out of git, and staff can't read it).
2. Look for keys inside resources too: `config.lua` files often hold webhooks or API keys.
   Resources that read them with `GetConvar` can take them from `secrets.cfg` instead.

If a secret was ever committed, change it at the source (a new license key, a new webhook). Removing
it from the repository afterwards doesn't take it back.

## Leave out what the server writes

Add a `.gitignore` at the top of the repository:

```gitignore
# Written by FXServer and txAdmin while the server runs
cache/
txData/
*.log
.replxx_history

# Kept on each server, never in git
secrets.cfg

# Dependencies some resources install themselves
node_modules/
```

Keep built files that resources need to run, like a UI's `web/dist` or `html/` folder, unless the
resource builds them on start.

## Push it to GitHub

1. On GitHub, create a **private** repository (New repository, Private, no README).
2. On the machine with your current server, in the folder you picked above:

   ```bash
   git init -b main
   git add .
   git commit -m "Server as it runs today"
   git remote add origin git@github.com:YOUR-ORG/YOUR-REPO.git
   git push -u origin main
   ```

   No git there? [GitHub Desktop](https://desktop.github.com) does the same: File > Add local
   repository, then Publish repository with "Keep this code private" ticked.

GitHub refuses files over 100 MB. Large streamed assets usually stay under that; if one doesn't,
compress or split it. Git LFS isn't supported yet.

## Then

[Make the Discord bot](/fxship/guides/discord-bot/), then install on
[Ubuntu](/fxship/install/linux/) or [Windows Server](/fxship/install/windows/). The installer
shows you what it found in the repository before it changes anything.
