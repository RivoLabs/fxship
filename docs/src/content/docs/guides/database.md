---
title: The database
description: MariaDB for both servers, copying prod's data into dev, and connecting with a database tool.
---

If you let the installer run MariaDB (the default), each server gets its own database and its own
user that can only reach that database. Dev never writes to prod's data.

| | Ubuntu | Windows Server |
| --- | --- | --- |
| Server | MariaDB 12 in Docker | the `fxship-mariadb` service |
| Listens on | 127.0.0.1:3306 only | 127.0.0.1:3306 only |
| Root password | `/root/fxship-credentials.txt` | `C:\ProgramData\fxship\credentials.txt` |

Each server's connection string is in its `secrets.cfg`
(`set mysql_connection_string "mysql://user:password@127.0.0.1:3306/name?charset=utf8mb4"`), which
oxmysql and the other database resources read.

## Bring your existing data

Give the installer your current database dump (`.sql` or `.sql.gz`, from HeidiSQL's export or
`mysqldump`) when it asks, and it's loaded into prod's empty database. Dev then starts as a copy of
prod.

## Refresh dev from prod

Dev's data drifts while you test. To start it again from a fresh copy of prod:

- in Discord: `/deploy refresh-dev-db` (approvers), or
- on the machine: `fxship db-refresh`.

fxship stops dev, replaces dev's database with a copy of prod's, and starts dev again. It takes
seconds for most servers. Anything that only existed in dev's database is gone.

### Scrubbing the copy

To change the copy before dev uses it, for example to remove real players' Discord ids or webhook
settings, put SQL in `/etc/fxship/dev-db-scrub.sql` (Ubuntu) or
`C:\ProgramData\fxship\dev-db-scrub.sql` (Windows). It runs against dev's database after every
refresh. For example (adjust it to your tables):

```sql
UPDATE users SET discord = NULL, ip = NULL;
DELETE FROM phone_messages;
```

## Connect with a database tool

MariaDB only listens on the machine itself. To browse it from your PC with
[HeidiSQL](https://www.heidisql.com) or DBeaver:

- **Ubuntu**: connect through SSH. In HeidiSQL pick *MariaDB or MySQL (SSH tunnel)*, enter your SSH
  login on the SSH tunnel tab, and use host `127.0.0.1`, port `3306`.
- **Windows Server**: run the tool on the machine over RDP, connecting to `127.0.0.1:3306`.

Use the server's own database user from its `secrets.cfg` for everyday work, and root only when
you need to.

## Your own database server

Answer **no** to "Run MariaDB on this machine?" and fxship leaves databases to you: put
`mysql_connection_string` in each server's `secrets.cfg` yourself, below fxship's block. To use
`db-refresh` and backups with it, set the `DB_*` settings (see
[Settings](/fxship/reference/configuration/#database)).
