# Command line

The same binary is the daemon and the administration tool:

```
/usr/local/directslave/bin/directslave <option> [arguments]
```

| Option | Purpose |
|---|---|
| `--check` | Check the configuration and file permissions |
| `--run` | Start the service (as a daemon when `background 1`) |
| `--debug` | Start in the foreground with verbose logging |
| `--status` | Tell whether the service is running |
| `--password <user>` | Create a user or change its password |
| `--delete <user>` | Delete a user |
| `--domain search <text>` | Find zones |
| `--domain add <zone> <primary>` | Add a zone |
| `--domain delete <zone>` | Delete a zone and its zone file |
| `--domain reload <zone>` | Force a transfer of a zone |
| `--dump-db [stats\|<zone>]` | Show the zone registry of the running service |

Run the administrative commands as root. They switch to the configured
`uid`/`gid` before touching any file, so files they create have the right
owner. Without an option, the usage text is printed.

## Users and passwords

Users in `authfile` (`var/db/passwd`) can call the API and log in to the
dashboard. There is one kind of user; every user can do everything.

### Create a user or change a password

```sh
/usr/local/directslave/bin/directslave --password da1
```

```
Enter new password for da1:
Repeat new password for da1:
User da1 created (added to /usr/local/directslave/var/db/passwd).
```

Nothing is shown while you type. If the user already exists, the same command
changes the password and prints `Password for da1 changed.` If the two entries
differ, it prints `Password mismatch.` and changes nothing.

The password is not accepted as an argument. `--password da1:secret`, the form
used before, is refused: an argument stays in the shell history and is visible
in the process list while the command runs.

**User names** of new users are latin letters and digits, 64 characters at
most. A user that already exists under another name can still be given a new
password or be deleted.

**Passwords** may contain any printable characters, including spaces, quotes,
`:`, `&`, `%`, `$`, `\` and non-ASCII letters, and are case sensitive. Leading
and trailing spaces are part of the password. Control characters (tab, escape)
and an empty password are refused. There is no length limit.

One practical limit is not DirectSlave's: the password also has to be typed
into DirectAdmin's Multi Server form, so pick something that form accepts.

### From a script

When standard input is not a terminal, the password is read from it and no
prompt is printed. One line is enough; if there are two, they must match.

```sh
printf '%s\n' "$DS_PASSWORD" | /usr/local/directslave/bin/directslave --password da1
```

Take the password from a file or a secret store. Writing it literally into the
command line of `printf` or `echo` puts it back into the shell history.

### Delete a user

```sh
/usr/local/directslave/bin/directslave --delete da1
```

### Notes

- Changes take effect immediately. The running service reads the file on every
  login; no reload is needed.
- A dashboard session that is already open stays valid after its user is
  deleted or its password changed, until logout. To end every session at once,
  change `cookie_auth_key` and restart.
- The file holds salted MD5-crypt hashes, one `user:hash` line per user. It is
  created with mode 0600. Keep it readable by the DirectSlave user only.
- Users can also be managed on the dashboard, under Manage Users.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | Nothing changed: mismatch, empty password, unknown user for `--delete`, or the file could not be written |
| 200 | Wrong arguments |

## Zones

In normal operation DirectAdmin manages the zones. These commands are for
checking, for repairs, and for zones that do not come from DirectAdmin.

### Search

```sh
/usr/local/directslave/bin/directslave --domain search example
```

Prints `zone:primary`, one per line, for every zone whose name contains the
text. The text is a plain substring, not a pattern. An IDN can be searched in
its native script or in punycode.

### Add

```sh
/usr/local/directslave/bin/directslave --domain add example.com 192.0.2.53
```

Adds the zone with that primary and tells the running service to load it. An
IDN typed in its native script is converted to punycode. If the service is not
running, the zone is loaded at the next start.

### Delete

```sh
/usr/local/directslave/bin/directslave --domain delete example.com
```

Removes the zone from the list and deletes its zone file. The name has to be
spelled the way `--domain search` prints it.

### Reload

```sh
/usr/local/directslave/bin/directslave --domain reload example.com
```

Transfers the zone from its primary now, whatever the serial. In external mode
this runs `rndc retransfer` instead.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | Refused: not found, or it already exists |
| 200 | Wrong or missing arguments |
| 255 | The zone list or the zone directory cannot be used |

## Zone registry

Integrated mode only, and only while the service is running.

```sh
/usr/local/directslave/bin/directslave --dump-db stats        # counters
/usr/local/directslave/bin/directslave --dump-db              # counters and one line per zone
/usr/local/directslave/bin/directslave --dump-db example.com  # one zone in detail
```

The per-zone report:

```
zone          example.com.
state         synced (the primary confirmed this copy)
primary       192.0.2.53
serial        2026111601
records       24
dnssec        unsigned
last payload  1.9 KB
soa timers    refresh 3600, retry 600, expire 1209600, minttl 3600
confirmed     4m ago
last transfer 2h10m ago
last attempt  4m ago
failures      0
next refresh  56m
next retry    -
expires in    13d23h
zone file     /usr/local/directslave/var/zones/example.com.db
```

The states are explained in [DNS server](dns-server.md#zone-states). The list
form is column aligned and can be filtered with `grep` and `awk`:

```sh
/usr/local/directslave/bin/directslave --dump-db | grep -v ' synced '
```

The command talks to the service through a unix socket next to the pid file,
readable by the DirectSlave user only. Nothing is exposed on the network.

## Service

```sh
/usr/local/directslave/bin/directslave --check
/usr/local/directslave/bin/directslave --status
```

Start and stop the service through `service directslave ...` (FreeBSD) or
`systemctl ... directslave` (Linux) rather than with `--run`. See
[Operations](operations.md).
