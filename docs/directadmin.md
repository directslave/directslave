# DirectAdmin setup

DirectSlave answers the same API calls a second DirectAdmin server would
answer in a Multi-Server setup. Nothing has to be installed on the DirectAdmin
side.

## 1. Create an API user on DirectSlave

One user per DirectAdmin server is the usual arrangement.

```sh
/usr/local/directslave/bin/directslave --password da1
```

The command asks for the password twice. See
[Command line](command-line.md#users-and-passwords).

## 2. Add the secondary in DirectAdmin

As admin, open **Multi Server Setup** (in the Evolution skin it is under
Server Manager) and add a server:

| Field | Value |
|---|---|
| IP | address of the DirectSlave host |
| Port | `port` from `directslave.conf` (2222), or `sslport` when you tick SSL |
| User / Password | the user created in step 1 |
| SSL | on only if `ssl on` is set in `directslave.conf` |

Then enable **Zone Transfer** for that server. **Domain Check** is optional: it
makes DirectAdmin ask the secondary whether a domain already exists before a
user may add it.

Use the test button. DirectSlave answers a successful test with `Login OK`; the
attempt is also written to `log/action.log`:

```
Authorization passed for da1 from 198.51.100.7
```

## 3. Let the primary serve the secondary

DirectAdmin only tells DirectSlave *that* a zone exists. The records travel by
a normal DNS zone transfer, so BIND on the DirectAdmin server has to:

- allow AXFR to the secondary's address (`allow-transfer`), and
- notify it of changes (`also-notify`, or simply an NS record for the secondary
  in each zone; BIND notifies the name servers listed in a zone).

How DirectAdmin's `named.conf` is arranged is described in the DirectAdmin
documentation for Multi-Server setups.

## 4. Check with one zone

Create a domain in DirectAdmin, or re-save the DNS of an existing one. Then on
the secondary:

```sh
/usr/local/directslave/bin/directslave --domain search example.com
/usr/local/directslave/bin/directslave --dump-db example.com
dig @127.0.0.1 example.com SOA +norecurse
```

The first command shows the zone and its primary. The second (integrated mode)
shows its state, which should be `synced`. Existing zones are pushed to a new
secondary only when DirectAdmin next saves them.

## How it works

| DirectAdmin does | DirectSlave does |
|---|---|
| Zone saved (`action=rawsave`) | New zone: adds it with the caller's address as primary and starts a transfer. Known zone: checks the serial and transfers if it changed. |
| Zone deleted (`action=delete`) | Removes the zone, its file and its line in the zone list. |
| Domain check (`action=exists`) | Reports whether the zone exists and on which primary. |
| Connection test | Checks the user and password. |

### The primary is the caller's address

When a zone is added through the API, its primary is set to **the IP address
the request came from**. Three things follow:

- The DirectAdmin server must reach DirectSlave from the same address its BIND
  answers zone transfers on. If the request leaves through another address
  (several addresses on the host, NAT, a proxy), transfers go to the wrong
  place. Set the correct primary on the Domains page or with `--domain`.
- The address family matters. If DirectAdmin reaches the API over IPv6, the
  primary is an IPv6 address and the zone is transferred over IPv6; the same
  for IPv4. A host name in Multi Server Setup that resolves to both families
  lets DirectAdmin pick, and zones may then end up with different primaries
  for the same server. Enter the IP address of the family you want.
- A zone belongs to its primary. Another DirectAdmin server, even with a valid
  user, cannot update or delete it; it gets `Domain already exist on master` or
  `No permission to remove domain`. This is what stops two servers from
  claiming the same domain.

Deleting a zone that is already gone is reported as success, so a retry from
DirectAdmin does no harm.

### Internationalized domains

DirectAdmin sends an IDN in its native script. DirectSlave converts it to
punycode, which is the form stored, transferred and answered. The dashboard and
`--domain search` find the zone by either spelling.

## Several DirectAdmin servers

Any number of DirectAdmin servers can use one DirectSlave. Give each its own
user so the action log shows who did what, and so one server's credentials can
be revoked without touching the others. Zone ownership is by primary address,
not by user name.

## Mail account hooks

DirectAdmin also sends mailbox changes to its Multi-Server peers
(`/CMD_API_POP`). DirectSlave does nothing with them itself; it runs a hook
script so you can. See [Operations](operations.md#hook-scripts).
