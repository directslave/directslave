# Installation

## Requirements

- FreeBSD or Linux, amd64. The release binaries are static and need no
  libraries or packages. Other platforms can be built from source.
- Root access for installation and for starting the service. DirectSlave binds
  its ports as root and then switches to an unprivileged numeric `uid`/`gid`.
- For the integrated DNS server: nothing else may listen on port 53 on the
  addresses DirectSlave is going to use.
- Network:
  - inbound TCP on `port` (2222 by default) and, with HTTPS, `sslport`, from
    your DirectAdmin servers and from wherever you open the dashboard;
  - inbound UDP and TCP 53 from the Internet;
  - outbound UDP and TCP 53 to your primaries (SOA checks and zone transfers);
  - outbound HTTPS to `directslave.com` for the update check. It is optional:
    if it is blocked, DirectSlave only reports that the check failed.
- The API, the dashboard, zone transfers and NOTIFY all work over IPv4 and
  IPv6. The primary of a zone is recorded as the address the API call came
  from, and that address is the one zone transfers are requested from, so
  DirectAdmin has to reach DirectSlave over the address family, and from the
  address, its BIND serves transfers on.

Everything is installed under `/usr/local/directslave`. The path is fixed.

## FreeBSD

```sh
# 1. Nothing else on port 53
sockstat -l -P tcp,udp -p 53
service named stop          # only if named is running
sysrc named_enable=NO

# 2. Extract
tar -xzf directslave-4.0.18-freebsd-amd64.tar.gz -C /usr/local

# 3. Configuration with a random cookie key
cp /usr/local/directslave/etc/directslave.conf.sample /usr/local/directslave/etc/directslave.conf
sed -i '' "s/CHANGE_THIS_TO_A_LONG_RANDOM_VALUE/$(od -An -N48 -tx1 /dev/urandom | tr -d ' \n')/" /usr/local/directslave/etc/directslave.conf
chmod 0600 /usr/local/directslave/etc/directslave.conf

# 4. Runtime directories and ownership
/usr/local/directslave/bin/directslave-prepare

# 5. API user, then the self-check
/usr/local/directslave/bin/directslave --password admin
/usr/local/directslave/bin/directslave --check

# 6. Service
install -m 0555 /usr/local/directslave/etc/rc.d/freebsd.directslave /usr/local/etc/rc.d/directslave
sysrc directslave_enable=YES
service directslave start
service directslave status
```

`uid`/`gid` 53 in the sample configuration is the `bind` user of the FreeBSD
base system.

## Linux

```sh
# 1. Nothing else on port 53
ss -lntup | grep ':53 '

# 2. Extract
tar -xzf directslave-4.0.18-linux-amd64.tar.gz -C /usr/local

# 3. Configuration, systemd unit, AppArmor or SELinux, runtime directories
/usr/local/directslave/install-linux.sh

# 4. API user, then the self-check
/usr/local/directslave/bin/directslave --password admin
/usr/local/directslave/bin/directslave --check

# 5. Service
systemctl start directslave
systemctl status directslave
```

`install-linux.sh` does the following and does not start the service:

- refuses to continue while `named.service` or `bind9.service` is active;
- creates `etc/directslave.conf` from the sample with a random
  `cookie_auth_key`, unless the file already exists;
- links the systemd unit and enables it;
- loads the AppArmor profile when AppArmor is present;
- installs the SELinux policy module when SELinux is enabled;
- runs `directslave-prepare`.

On Ubuntu and Debian `systemd-resolved` usually holds `127.0.0.53:53`. If step 1
shows it and the start fails with "address already in use", either list your
public addresses in `dns_listen_ip4` instead of `*`, or disable the stub
listener (`DNSStubListener=no` in `/etc/systemd/resolved.conf`, then
`systemctl restart systemd-resolved`).

Do not add `User=` or `Group=` to the systemd unit. The service has to start as
root to bind port 53; it drops privileges itself.

## A host without IPv6

The sample configuration listens on every IPv4 and IPv6 address. If the host
has no IPv6, DirectSlave refuses to start because the IPv6 socket cannot be
opened. Set:

```
dns_listen_ip6  none
```

## Directory layout

```
/usr/local/directslave/
  bin/directslave             daemon and command line tool
  bin/directslave-prepare     creates runtime directories, sets ownership
  etc/directslave.conf        configuration
  etc/directslave.conf.sample
  etc/rc.d/                   FreeBSD rc.d script, SysV-style Linux script
  etc/systemd/                systemd unit              (Linux)
  etc/apparmor/, etc/selinux/ confinement profiles      (Linux)
  log/                        access.log, action.log, error.log, dns.log
  run/                        pid file, control socket
  scripts/                    hooks run on API events
  ssl/                        certificate and key for HTTPS
  var/db/directslave.inc      zone list
  var/db/passwd               API and dashboard users
  var/zones/                  zone files, one <zone>.db per zone
  www/                        dashboard templates and static files
```

`directslave-prepare` reads `uid`, `gid` and the DNS options from the
configuration, creates `log`, `run`, `var/db` and `var/zones`, creates empty
log files, the zone list and `passwd`, and gives the whole tree to the
configured `uid:gid`. Run it again after changing `uid` or `gid`.

## First start

`--check` verifies the configuration before you start the service: it drops to
the configured user, then tries to open every log file, the zone list, the pid
file and the zone directory, tests that the zone list can be rewritten, and
validates the DNS listen addresses.

```sh
/usr/local/directslave/bin/directslave --check
```

It ends with `*** All OK! You can safely run with --run flag.` or with the
number of errors. To watch a start in the foreground:

```sh
/usr/local/directslave/bin/directslave --debug
```

`--debug` stays attached to the terminal and turns verbose logging on. Stop it
with Ctrl+C.

Next: [connect DirectAdmin](directadmin.md).

## Building from source

Go 1.21 or newer.

```sh
cd build
./Build-free-amd64.sh      # or Build-linux-amd64.sh, Build.sh for all targets
```

or directly:

```sh
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o builds/directslave-freebsd-amd64 .
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o builds/directslave-linux-amd64 .
go test ./...
```
