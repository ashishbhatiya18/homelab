# Restoring without hbr (disaster recovery)

Use this when the Mac that ran hbr is gone. You need:

- the backup folder (e.g. from Dropbox), which contains `keys/` and one folder per app;
- **either** your backup password **or** your recovery key (`AGE-SECRET-KEY-1…`);
- [`age`](https://age-encryption.org) and Postgres client tools
  (`brew install age libpq`, or `apt install age postgresql-client`).

The easiest path is to reinstall hbr, point it at the folder, and use
`hbr restore … --recovery` if needed. The steps below work without it.

## 1. Unlock a private key

With the password:

```sh
age -d keys/daily-key.age > /tmp/key.txt      # prompts for the password
```

Or with the recovery key: save it into `/tmp/key.txt` (one line).

## 2. Decrypt and unpack a snapshot

```sh
mkdir /tmp/snap && cd /tmp/snap
age -d -i /tmp/key.txt ~/Backups/myapp/myapp-20261004T020000Z.tar.age | tar -x
cat manifest.json            # what's inside, when it was taken, row counts
```

Each source is a folder; Postgres sources contain `database.dump`.

## 3. Restore into any Postgres

```sh
createdb -h NEWHOST -U ADMIN myapp
pg_restore -h NEWHOST -U ADMIN -d myapp \
  --no-owner --no-privileges --exit-on-error --single-transaction \
  db/database.dump
```

The dump has no owners or grants, so it restores under whichever role you
connect as, on the same or any newer Postgres version.

## 4. Clean up

```sh
rm -rf /tmp/snap /tmp/key.txt
```
