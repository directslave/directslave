# Operations

## Service control

FreeBSD:

```sh
service directslave start
service directslave stop
service directslave restart
service directslave reload
service directslave status
```

Linux:

```sh
systemctl start directslave
systemctl stop directslave
systemctl restart directslave
systemctl reload directslave
systemctl status directslave
```

| Action | Effect |
|---|---|
| reload (`SIGHUP`) | Re-reads the zone list, reopens the logs, re-reads the HTTPS certificate. Queries are answered throughout. Does not re-read `directslave.conf`. |
| restart | Everything, including `directslave.conf`. In integrated mode the [startup check](dns-server.md#startup-check) runs again. |
| stop (`SIGTERM`) | Closes the listeners, removes the pid file, exits. |

`directslave.conf` needs `background 1` for both the rc.d script and the
systemd unit.

## Logs

| File | Content |
|---|---|
| `log/access.log` | Every API and dashboard request |
| `log/action.log` | Changes and logins: who added or removed what, hooks that ran |
| `log/error.log` | Errors, reloads, shutdown |
| `log/dns.log` | Integrated mode: transfers, NOTIFY, timers, startup check |
| `log/dns-query.log` | Integrated mode, only when `dns_query_log` is on |

With `debug 1` complete API requests are written to `error.log`. Use it to see
what DirectAdmin sends, and turn it off afterwards.

### Rotation

DirectSlave does not rotate its logs. It reopens them on `SIGHUP`, so any
rotation tool works.

FreeBSD, `/usr/local/etc/newsyslog.conf.d/directslave.conf`:

```
/usr/local/directslave/log/access.log  53:53  640  7  *  @T00  JC  /usr/local/directslave/run/directslave.pid
/usr/local/directslave/log/action.log  53:53  640  7  *  @T00  JC  /usr/local/directslave/run/directslave.pid
/usr/local/directslave/log/error.log   53:53  640  7  *  @T00  JC  /usr/local/directslave/run/directslave.pid
/usr/local/directslave/log/dns.log     53:53  640  7  *  @T00  JC  /usr/local/directslave/run/directslave.pid
```

Linux, `/etc/logrotate.d/directslave`:

```
/usr/local/directslave/log/*.log {
    daily
    rotate 7
    compress
    delaycompress
    missingok
    notifempty
    create 0640 53 53
    sharedscripts
    postrotate
        /bin/kill -HUP "$(cat /usr/local/directslave/run/directslave.pid 2>/dev/null)" 2>/dev/null || true
    endscript
}
```

Replace `53` with your `uid`/`gid` if they differ.

## HTTPS

```
ssl       on
sslport   2224
ssl_cert  /usr/local/directslave/ssl/fullchain.pem
ssl_key   /usr/local/directslave/ssl/privkey.pem
```

Both files must be readable by the DirectSlave `uid`. Use the full chain, not
only the leaf certificate, or DirectAdmin may refuse the connection.

The certificate is re-read on reload, so a renewal needs no restart. With
certbot or acme.sh, copy the renewed files into `ssl/` and reload in the deploy
hook:

```sh
install -o 53 -g 53 -m 0640 /etc/letsencrypt/live/ns2.example.com/fullchain.pem /usr/local/directslave/ssl/fullchain.pem
install -o 53 -g 53 -m 0640 /etc/letsencrypt/live/ns2.example.com/privkey.pem  /usr/local/directslave/ssl/privkey.pem
kill -HUP "$(cat /usr/local/directslave/run/directslave.pid)"
```

If the new certificate cannot be loaded, the old one stays in use and the
reason is written to `error.log`.

The plain HTTP port stays open when HTTPS is on. TLS versions up to 1.2 are
offered.

## Backups

What is worth keeping:

| Path | Why |
|---|---|
| `etc/directslave.conf` | Configuration, including the cookie key |
| `var/db/passwd` | Users |
| `var/db/directslave.inc` | The zone list with each zone's primary |
| `ssl/` | Certificate and key, unless they are restored from elsewhere |

Zone files in `var/zones/` do not need a backup. They are copies; given the
zone list, DirectSlave transfers every zone again from its primary.

To move to another host: install DirectSlave there, copy the four items above,
run `directslave-prepare`, start. Remember that the primaries must allow
transfers to the new address.

## Hook scripts

DirectSlave runs a script from `/usr/local/directslave/scripts/` on these API
events, as the DirectSlave user:

| Script | When |
|---|---|
| `dns_write_post.sh` | DirectAdmin saved a zone |
| `pop_create.sh` | A mailbox was created on the DirectAdmin server |
| `pop_modify.sh` | A mailbox was changed |
| `pop_delete.sh` | A mailbox was deleted |

The scripts shipped do nothing. Parameters arrive as environment variables.
For `dns_write_post.sh`:

| Variable | Content |
|---|---|
| `domain` | Zone name |
| `master` | Address of the primary |
| `username` | DirectAdmin user that owns the domain |
| `zonedir` | `named_workdir` |
| `body` | The zone as DirectAdmin sent it |

The mailbox scripts receive the fields of DirectAdmin's request (`domain`,
`user`, `passwd`, `quota` and so on) under the same names. Note that this
includes the mailbox password in clear text; treat such a script and anything
it writes accordingly.

A script must be executable. Its run is recorded in `action.log`; with
`debug 1` its output goes to `error.log`. DirectSlave waits for the script to
finish before it answers DirectAdmin, so keep hooks fast.

## Security

- **Privileges.** The service starts as root only to bind port 53 and the HTTP
  ports, then switches to `uid`/`gid`. Everything under
  `/usr/local/directslave` belongs to that user.
- **Firewall.** Open 53 UDP and TCP to the world. Open the API port only to
  your DirectAdmin servers and to the addresses you administer from.
- **HTTPS.** API calls carry the user's password with every request (HTTP Basic
  authentication). Over plain HTTP it can be read in transit.
- **Passwords.** `--password` reads them from the terminal, never from the
  command line. `var/db/passwd` and `etc/directslave.conf` should be mode 0600.
- **`cookie_auth_key`.** Whoever knows it can forge a dashboard session. Use a
  long random value, and change it if it may have leaked.
- **Profiler.** `pprof_enable` is on unless you set it to `0`. It listens on
  localhost only.
- **Unauthenticated endpoint.** `/CMD_API_MEMORY_MONITOR` returns memory
  counters without a login. It reveals nothing about zones or users, but it is
  another reason to firewall the API port.
- **Confinement on Linux.** The systemd unit, the AppArmor profile and the
  SELinux module restrict the service to its own directory and its ports. When
  something is denied, see [Troubleshooting](troubleshooting.md#linux-confinement).

## Monitoring

Simple checks that cover most failures:

```sh
# the service answers for a zone it holds
dig @127.0.0.1 example.com SOA +norecurse +short

# no zone is out of sync
/usr/local/directslave/bin/directslave --dump-db | awk '$2=="unsynced" || $2=="awaiting"'

# the API is up
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:2222/login
```

`--dump-db stats` prints the counters in one block, suitable for a collector.
