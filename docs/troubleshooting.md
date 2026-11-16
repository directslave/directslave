# Troubleshooting

Start with these three:

```sh
/usr/local/directslave/bin/directslave --check
tail -n 50 /usr/local/directslave/log/error.log
tail -n 50 /usr/local/directslave/log/dns.log
```

and, when the service does not stay up, run it in the foreground to see the
reason on the terminal:

```sh
/usr/local/directslave/bin/directslave --debug
```

## The service does not start

**A required option is missing.** The start aborts while reading the
configuration. Every option marked as required in
[Configuration](configuration.md) has to be present, including the BIND ones
(`rndc_path`, `named_format`, `retry_time`) in integrated mode.

**`address already in use` on port 53.** Something else listens there.

```sh
sockstat -l -P tcp,udp -p 53      # FreeBSD
ss -lntup | grep ':53 '           # Linux
```

Stop BIND, or on Ubuntu/Debian deal with `systemd-resolved` as described in
[Installation](installation.md#linux).

**The IPv6 socket cannot be opened.** The host has no IPv6. Set
`dns_listen_ip6 none`.

**A listed address cannot be bound.** Every address in `dns_listen_ip4` and
`dns_listen_ip6` has to be configured on the host. Host names are not accepted.

**`permission denied` on port 53 or 2222.** The service was not started as
root. Start it through the rc.d script or the systemd unit; do not set `User=`
in the unit.

**`Cant' use plain username as UID`.** `uid` and `gid` must be numbers.

**`--check` reports files that cannot be opened.** Ownership is wrong, usually
after `uid`/`gid` was changed or files were copied as root. Run:

```sh
/usr/local/directslave/bin/directslave-prepare
```

**`I'm already alive at pid ...`.** A previous instance is running, or the pid
file names a process id that now belongs to something else. Check with
`ps -p <pid>`; if it is not DirectSlave, delete
`/usr/local/directslave/run/directslave.pid`.

**HTTPS: the certificate or key cannot be read.** Both files must be readable
by `uid`, and the key must match the certificate.

### Linux confinement

When the same binary starts by hand but not as a service, look for denials:

```sh
journalctl -u directslave -b --no-pager
dmesg | grep -i apparmor
ausearch -m AVC -ts recent
```

The profiles expect everything under `/usr/local/directslave` and the default
ports. If you changed ports or paths in `directslave.conf`, the profile has to
allow them too.

## DirectAdmin cannot connect

1. From the DirectAdmin server:
   ```sh
   curl -s -u da1 http://<secondary>:2222/CMD_API_LOGIN_TEST
   ```
   `error=0&text=Login OK` means address, port, user and password are right.
2. `Login FAIL`: wrong user or password. Set the password again with
   `--password da1` and enter exactly the same one in DirectAdmin. Passwords
   are case sensitive and spaces count.
3. No answer at all: firewall, wrong port, or the service is down.
4. With SSL ticked in DirectAdmin, the port must be `sslport` and
   `directslave.conf` needs `ssl on`. A certificate that DirectAdmin does not
   trust also makes the test fail.

Each attempt is logged in `action.log` as `Authorization passed for ...` or
`Authorization failed from ...`.

## A zone is not created on the secondary

- Zone Transfer is not enabled for the server in Multi Server Setup.
- The zone already exists here with another primary. The API answers
  `Domain already exist on master <address>` and `action.log` has
  `reload forbidden`. Remove the stale zone, or correct its primary on the
  Domains page.
- Existing zones are sent only when DirectAdmin saves them. A zone that has not
  changed since the secondary was added is not pushed.

Turn on `debug 1`, restart, save the zone again, and read `error.log` to see
exactly what DirectAdmin sent.

## A zone does not transfer

```sh
/usr/local/directslave/bin/directslave --dump-db example.com
```

Look at `state`, `primary` and `last error`.

| What you see | Usual cause |
|---|---|
| The primary answers, but refuses the transfer | It does not allow AXFR to this host. Add the secondary's address to `allow-transfer` on the primary. |
| Timeouts, connection refused | Port 53 TCP or UDP to the primary is blocked, or the primary is down. |
| The primary does not know the zone | The domain was removed there. Delete it here too. |

Test the transfer by hand from the secondary:

```sh
dig @192.0.2.53 example.com SOA +short
dig @192.0.2.53 example.com AXFR | tail -n 3
```

**The primary address is wrong.** A zone added through the API gets the address
the API call came from. If the DirectAdmin server has several addresses, that
may not be the one its BIND serves transfers on. Change the primary on the
Domains page, or:

```sh
/usr/local/directslave/bin/directslave --domain delete example.com
/usr/local/directslave/bin/directslave --domain add example.com 192.0.2.53
```

**The primary is an IPv6 address and you expected IPv4, or the reverse.**
DirectAdmin reached the API over the other address family. A zone belongs to
the address it was added from, so the same server arriving from its other
address is refused with `Domain already exist on master`. Put the IP address
of the family you want into Multi Server Setup instead of a host name.

**Force a transfer:**

```sh
/usr/local/directslave/bin/directslave --domain reload example.com
```

## Changes arrive late

The secondary learns about a change from a NOTIFY. If none arrives, it finds
out at the next SOA refresh, which can be hours.

- NOTIFY must reach UDP port 53 of the secondary.
- It must come from the address recorded as the zone's primary; a NOTIFY from
  another address is ignored and logged in `dns.log`.
- On the primary, the secondary has to be in the zone's NS records or in
  `also-notify`.

## The secondary answers SERVFAIL

- Right after a restart: the [startup check](dns-server.md#startup-check) has
  not reached the zone yet. `--dump-db stats` shows how many zones are still
  unverified.
- The zone is `awaiting`: it was never transferred. See above.

## The secondary answers REFUSED

It does not hold the zone. Check the spelling with `--domain search`. Recursive
queries (names outside its zones) are always refused; test with
`dig +norecurse`.

## The dashboard

**Login fails with `Invalid input.`** The user name contains something other
than letters and digits. Such a user can call the API but cannot log in to the
dashboard; create a second user for that.

**Logged out after every restart.** `cookie_auth_key` changes between starts,
for example because a deployment tool regenerates the file.

**The Domains page has no State column.** The service runs in external mode, or
the `www/` directory is from an older release than the binary. Replace `www/`
from the same archive the binary came from.

## Passwords

**`--password user:pass` is refused.** By design; run `--password user` and type
the password at the prompt.

**Nothing appears while typing.** Also by design. Type the password and press
Enter.

**A password that used to work stopped working after it was set with a `:` in
it.** Releases up to 4.0.17 took `user:password` on the command line and cut the
password at a `:` inside it. Set it again with the current release.

**The terminal shows no input after an interrupted `--password`.** The command
restores the terminal when it is interrupted. If a terminal is ever left in
that state, type `stty echo` and press Enter.

## Getting help

Include with a report:

- the version (first line of `directslave --check`),
- the operating system,
- the output of `directslave --check`,
- the relevant lines of `error.log` and `dns.log`,
- for a zone problem, the output of `directslave --dump-db <zone>`.

Remove passwords and the `cookie_auth_key` before posting a configuration file.
