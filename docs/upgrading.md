# Upgrading

There are two separate steps, and you can take them on different days:

1. Replace DirectSlave 3.x with DirectSlave 4, still using BIND.
2. Switch from BIND to the integrated DNS server.

## 1. From 3.x to 4, keeping BIND

A 3.x `directslave.conf` works unchanged. Without `dns_server` in it, version 4
behaves as 3.5 did: it maintains the zone list, BIND serves the zones, `rndc`
reloads BIND.

```sh
# keep a copy of the old binary and templates
cp -p /usr/local/directslave/bin/directslave /usr/local/directslave/bin/directslave.3
cp -Rp /usr/local/directslave/www /usr/local/directslave/www.3

# unpack the new release somewhere else and take the binary and www from it
mkdir /tmp/ds4 && tar -xzf directslave-4.0.18-freebsd-amd64.tar.gz -C /tmp/ds4
service directslave stop
cp /tmp/ds4/directslave/bin/directslave /usr/local/directslave/bin/directslave
cp -R /tmp/ds4/directslave/www/. /usr/local/directslave/www/
/usr/local/directslave/bin/directslave --check
service directslave start
```

On Linux use the `linux-amd64` archive and `systemctl stop|start directslave`,
or your existing init script.

Do not extract a release archive straight over a running 3.x install without
looking: it contains no `directslave.conf` or `passwd`, so those are safe, but
it does replace `scripts/`. If you customised the hook scripts, copy them
aside first.

### What changes for you

- **`--password` asks for the password.** `--password user:password` is no
  longer accepted. Scripts that create users have to pipe the password in; see
  [Command line](command-line.md#from-a-script).
- **`--check` is stricter.** It also tests that the zone list can be rewritten
  (the directory of `named_conf` must be writable by `uid`), and it reports a
  leftover `named_conf.tmp` from an interrupted delete. Remove such a file.
- **IPv6 works for the API and the dashboard.** Earlier releases recorded a
  broken primary for a DirectAdmin server that connected over IPv6. If your
  DirectAdmin was set up with a host name that has an AAAA record, check which
  address it uses now; see [DirectAdmin setup](directadmin.md#the-primary-is-the-callers-address).
- **New commands:** `--domain search|add|delete|reload`.
- **The update notice** compares versions properly, so a 4.x install is no
  longer told that 3.x is newer.

Existing users and their passwords keep working. The format of `passwd` has
not changed.

### Going back

Stop the service, put `bin/directslave.3` and `www.3` back, start. The zone
list and zone files are in the same format in both versions as long as you
stayed on BIND.

## 2. From BIND to the integrated server

Before you start:

- Port 53 has to be free. BIND on this host must be stopped, or bound to other
  addresses than the ones DirectSlave will use.
- The primaries keep allowing zone transfers to this host and notifying it.
  Nothing changes on their side.
- Zone files written by BIND in **raw** format cannot be read by DirectSlave.
  That is not a problem: a zone whose file is missing or unreadable is simply
  transferred again from its primary. If `named_format text` was set, the
  existing files are loaded directly.

Add to `directslave.conf`:

```
dns_server      integrated
dns_listen_ip4  *
dns_listen_ip6  *
dns_port        53
dns_server_log  /usr/local/directslave/log/dns.log
```

On a host without IPv6 use `dns_listen_ip6 none`.

Then:

```sh
# FreeBSD
service directslave stop
service named stop
sysrc named_enable=NO
/usr/local/directslave/bin/directslave --check
service directslave start
```

```sh
# Linux
systemctl stop directslave
systemctl disable --now named      # or bind9
/usr/local/directslave/bin/directslave --check
systemctl start directslave
```

The service now has to start as root, because it binds port 53 itself. If your
3.x setup started DirectSlave under an unprivileged account, switch to the
rc.d script or systemd unit from the release archive.

### What happens at the first start

Every zone in the zone list is loaded. Zones with a readable text zone file
are checked against their primary by serial; the rest are transferred. With
many zones this takes a while; watch it:

```sh
tail -f /usr/local/directslave/log/dns.log
/usr/local/directslave/bin/directslave --dump-db stats
```

Until a zone has been confirmed or transferred it is answered SERVFAIL, and
resolvers use your other name servers. Do the switch on one secondary at a
time.

When the counters settle, list what is left:

```sh
/usr/local/directslave/bin/directslave --dump-db | awk '$2!="synced"'
```

Zones that stay `awaiting` or `unsynced` usually have a primary that refuses
the transfer or no longer exists. BIND was hiding those; see
[Troubleshooting](troubleshooting.md#a-zone-does-not-transfer).

### `named.conf`

Remove the `include` of DirectSlave's zone list from BIND's `named.conf` if you
keep BIND installed for other purposes. The file itself stays: it is now
DirectSlave's own list, in the same format.

`rndc_path`, `named_format` and `retry_time` must stay in the configuration
file (they are required options), but `rndc` is no longer called.

### Going back to BIND

Remove or comment out `dns_server integrated`, restart DirectSlave, start BIND.
The zone list is the same file. Zone files written by the integrated server are
plain text; with `named_format text` BIND reads them as they are, otherwise it
transfers the zones again.
