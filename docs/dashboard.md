# Dashboard

The dashboard is served on the same ports as the API:

```
http://<host>:2222/          plain HTTP, `port`
https://<host>:2224/         HTTPS, `sslport`, when `ssl on`
```

Log in with any user from `authfile`. API users and dashboard users are the
same accounts. Create the first one on the command line:

```sh
/usr/local/directslave/bin/directslave --password admin
```

Passwords travel in the login form, so use HTTPS when the dashboard is reached
over an untrusted network, or keep the port closed to everything except your
own addresses.

## Domains

The list of zones. In integrated mode each row shows what the zone is doing
right now:

| Column | Meaning |
|---|---|
| Zone | Zone name. |
| State | See [zone states](dns-server.md#zone-states). Rows that need attention are highlighted. |
| Primary | Address the zone is transferred from. |
| Serial | SOA serial of the copy held here. |
| Records | Number of records in the zone. |
| DNSSEC | Marked for signed zones. |
| Confirmed | When the primary last confirmed this serial. |
| Transfer | When the zone was last transferred. |
| Refresh / Expires | When the next serial check is due and when the zone would expire. |
| Fails / Last error | Consecutive failed attempts and the reason for the last one. |

Above the table you can search by name, filter by state, primary or DNSSEC, and
sort by any column. Clicking a state or a primary in a row filters by it. The
`auto` checkbox refreshes the page periodically.

Actions:

- **Add** a zone with its primary address. Use it for zones that do not come
  from DirectAdmin.
- **Edit** changes the primary of a zone. The zone is then transferred from the
  new address.
- **Remove** deletes the zone and its zone file.
- **Reload** forces a transfer from the primary now.

Every change is written to `action.log` with the user name and address.

A zone added by DirectAdmin and then edited or removed here is not changed on
the DirectAdmin side. If DirectAdmin saves the zone again, the secondary gets
it again.

In external mode the page lists the zones and their primaries only; state and
statistics belong to BIND.

## Statistics

Integrated mode only. Totals for the whole server (zones, records, transfers,
failures, memory in use) and a breakdown per primary: how many zones each one
feeds and how many of them are in sync. This is the quickest way to spot a
primary that stopped answering.

The same numbers are available on the command line:

```sh
/usr/local/directslave/bin/directslave --dump-db stats
```

## Manage Users

Add a user, change a password, remove a user. User names are latin letters and
digits. Changes apply immediately.

The password field hides what you type. Passwords may contain any printable
characters; see [Command line](command-line.md#users-and-passwords) for the
details.

Removing the user you are logged in as does not end your session; log out
afterwards.

## Log Files

Shows the tail of `access.log`, `action.log`, `error.log` and, in integrated
mode, `dns.log` and the query log when it is enabled. Useful for a quick look
without a shell; for anything longer use the files themselves.

## Update notice

DirectSlave asks `https://directslave.com/version` for the current version at
start and every 12 hours, and shows a notice in the dashboard when a newer one
exists. It never downloads or installs anything. If the host cannot reach the
Internet, the check fails quietly.
