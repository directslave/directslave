# Configuration

The configuration file is `/usr/local/directslave/etc/directslave.conf`. The
path is fixed. The format is one `option value` pair per line; lines starting
with `#` are comments.

The file is read once at start. To apply a change, restart the service.
A reload (`SIGHUP`) re-reads the zone list, the certificate and reopens the
logs, but not this file.

Options in the first group are **required**: DirectSlave exits at start if one
is missing. Everything about the integrated DNS server is optional, so a 3.x
configuration file works unchanged.

After editing, run:

```sh
/usr/local/directslave/bin/directslave --check
```

## Process

| Option | Required | Default | Meaning |
|---|---|---|---|
| `background` | yes | | `1`: detach and run as a daemon. `0`: stay in the foreground. Use `1` with the rc.d script and with systemd. |
| `uid` | yes | | Numeric user id DirectSlave switches to after binding its ports. A name is not accepted. |
| `gid` | yes | | Numeric group id. |
| `pid` | yes | | Pid file. In integrated mode the control socket used by `--dump-db` is created next to it, with the same name and the extension `.sock`. |
| `debug` | yes | | `1`: verbose logging, including full API requests. `0`: normal. |

On an upgrade from 3.x keep the `uid`/`gid` you already use (normally the
`bind`/`named` user), so existing zone files stay readable. On a new install
53 is a common choice; any unprivileged id works as long as
`directslave-prepare` has been run with it.

## API and dashboard (HTTP)

| Option | Required | Default | Meaning |
|---|---|---|---|
| `host` | yes | | Address to listen on: `*` for every IPv4 and IPv6 address, or one IPv4 or IPv6 address. |
| `port` | yes | | Plain HTTP port. DirectAdmin's default for Multi-Server is 2222. |
| `ssl` | yes | | `on`: also serve HTTPS on `sslport`. `off`: HTTP only. |
| `sslport` | with `ssl on` | | HTTPS port. |
| `ssl_cert` | with `ssl on` | | Certificate chain, PEM. |
| `ssl_key` | with `ssl on` | | Private key, PEM. |
| `cookie_sess_id` | yes | | Name of the dashboard session cookie. |
| `cookie_auth_key` | yes | | Secret the session cookie is signed with. Use a long random string and keep it private. Changing it logs every dashboard user out. |
| `authfile` | yes | | File with API and dashboard users. See [Command line](command-line.md#users-and-passwords). |

With `ssl on` the plain HTTP port stays open as well. If you only want HTTPS
reachable from outside, close `port` in the firewall.

## Logs

| Option | Required | Default | Meaning |
|---|---|---|---|
| `access_log` | yes | | Every API and dashboard request. |
| `action_log` | yes | | What was changed and by whom: zones added and removed, logins, hooks. |
| `error_log` | yes | | Errors, reload and shutdown messages. |
| `dns_server_log` | no | same as `action_log` | DNS events: transfers, NOTIFY, timers, startup check. Integrated mode only. |

## Zone storage

| Option | Required | Default | Meaning |
|---|---|---|---|
| `named_conf` | yes | | The zone list: one `zone "..." { type slave; ... };` line per zone. In external mode BIND includes this file. In integrated mode it is DirectSlave's own list. A lock file (`.lock`) is kept beside it. |
| `named_workdir` | yes | | Directory for zone files, `<zone>.db`. |
| `named_format` | yes | | `text` adds `masterfile-format text;` to each zone line, so BIND keeps readable zone files. Any other value leaves BIND's default. The integrated server always writes text. |
| `retry_time` | yes | | Seconds, written into each zone line as BIND's `min-retry-time`/`max-retry-time`. In integrated mode a numeric value is also the default for `dns_new_retry`. |
| `rndc_path` | yes | | Path to `rndc`, or `none` to never call it. Ignored in integrated mode. |

The directory that holds `named_conf` must be writable by `uid`: the list is
replaced by writing a new file and renaming it into place.

## DNS backend

| Option | Default | Meaning |
|---|---|---|
| `dns_server` | `external` | `integrated` (aliases `internal`, `directdns`): DirectSlave serves the zones itself. `external`: BIND serves them. |

Everything below applies to integrated mode only.

## DNS listener

| Option | Default | Meaning |
|---|---|---|
| `dns_listen_ip4` | `*` | IPv4 addresses to listen on. |
| `dns_listen_ip6` | `*` | IPv6 addresses to listen on. |
| `dns_port` | `53` | Port, UDP and TCP. |

Each listen option takes:

- `*` for every address of that family,
- one address,
- several addresses separated by spaces or commas,
- `none` to leave the family off.

Host names are not accepted. Every listed address has to exist on the host,
otherwise DirectSlave does not start.

When only one of the two options is present, the other family is **off**.
When neither is present, both are `*`. So:

```
dns_listen_ip4  192.0.2.10, 192.0.2.11
```

listens on those two IPv4 addresses and on no IPv6 address at all.

IPv4 and IPv6 always use separate sockets. The older single option
`dns_listen` is still read when neither new option is set, and `--check`
prints a deprecation warning for it.

## New zone timers

A zone that has been added but has not completed its first transfer has no SOA
of its own yet. Until it does, these values are used. After the first
transfer the timers come from the zone's SOA record.

| Option | Default | Meaning |
|---|---|---|
| `dns_new_refresh` | `300` | Seconds between serial checks. |
| `dns_new_retry` | `60`, or `retry_time` if that is numeric | Seconds before a failed transfer is retried. |
| `dns_new_expire` | `604800` | Seconds after which a zone that never transferred is given up. |
| `dns_expunge_expired` | `0` | What to do with an expired zone. `0`: stop serving it, keep its line in the zone list. `1`: also remove the line. |

## Transfers

| Option | Default | Meaning |
|---|---|---|
| `transfer_queue` | `20` | Maximum number of zone transfers running at once. |

## Startup check

While DirectSlave is stopped a zone can change on its primary. At start every
zone restored from disk is therefore compared with its primary by SOA serial
before it is answered. See [DNS server](dns-server.md#startup-check).

| Option | Default | Meaning |
|---|---|---|
| `startup_check` | `on` | `on`: do not answer a stored zone until its serial is confirmed. `off`: answer stored zones immediately. |
| `startup_check_workers` | `20` | Serial probes in flight over all primaries. |
| `startup_check_rate` | `20` | Probes per second sent to one primary. |
| `startup_check_grace` | `0` | Seconds after which zones that are still unconfirmed are answered from stored data anyway. `0`: wait until the zone is confirmed or its primary is found unreachable. |

A rough estimate of how long the check takes is the number of zones on your
busiest primary divided by `startup_check_rate`, in seconds. With tens of
thousands of zones raise the rate, or set a grace period.

## Query log

| Option | Default | Meaning |
|---|---|---|
| `dns_query_log` | `off` | `off`, `errors` (only negative and error answers), or `all`. |
| `dns_query_log_file` | `dns-query.log` next to `dns_server_log` | File for query lines. |
| `dns_query_log_buffer` | `8192` | Lines buffered in memory. When the buffer is full, log lines are dropped; queries are never delayed. |

The query log records client addresses and queried names. Enable it when you
need that data, and rotate the file.

## Profiler

| Option | Default | Meaning |
|---|---|---|
| `pprof_enable` | `1` | Go profiler on `localhost:pprof_port`. Set `0` to turn it off. |
| `pprof_port` | `2223` | Port of the profiler, bound to localhost only. |

Note that the profiler is **on** when the option is absent. This matches 3.x.

## A minimal integrated configuration

```
background      1
uid             53
gid             53
pid             /usr/local/directslave/run/directslave.pid
debug           0

host            *
port            2222
ssl             off
cookie_sess_id  DS_SESSID
cookie_auth_key <long random string>
authfile        /usr/local/directslave/var/db/passwd

access_log      /usr/local/directslave/log/access.log
action_log      /usr/local/directslave/log/action.log
error_log       /usr/local/directslave/log/error.log
dns_server_log  /usr/local/directslave/log/dns.log

named_conf      /usr/local/directslave/var/db/directslave.inc
named_workdir   /usr/local/directslave/var/zones
named_format    text
retry_time      1200
rndc_path       none

dns_server      integrated
dns_listen_ip4  *
dns_listen_ip6  *
dns_port        53
pprof_enable    0
```
