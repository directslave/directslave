# DNS server

This page describes the integrated server (`dns_server integrated`). In
external mode all of this is done by BIND and none of it applies.

## What it is

An authoritative **secondary** server. It holds copies of zones whose primary
is somewhere else, keeps them current, and answers queries for them. It does
not resolve other names (recursion is not offered), it is never a primary, and
it never edits or signs a zone.

- UDP and TCP, IPv4 and IPv6.
- EDNS0. UDP answers are limited to 1232 bytes; an answer that does not fit is
  sent truncated and the client repeats the query over TCP.
- A query for a zone it does not hold is answered REFUSED.
- NXDOMAIN and empty answers carry the zone's SOA, as required for negative
  caching. CNAMEs and wildcards are followed.

## Life of a zone

1. **Declared.** DirectAdmin (or the dashboard, or `--domain add`) adds the
   zone with a primary address. It gets a line in the zone list.
2. **First transfer.** DirectSlave asks the primary for the SOA and pulls the
   zone with AXFR. Until this succeeds the zone is answered SERVFAIL, not
   NXDOMAIN, so resolvers do not cache "this name does not exist".
3. **Stored.** The zone is written to `var/zones/<zone>.db` as a normal text
   zone file, through a temporary file renamed into place, and loaded into
   memory.
4. **Kept current.** Three things trigger a serial check against the primary:
   - a NOTIFY from the primary,
   - the zone's SOA refresh timer,
   - a save of the zone in DirectAdmin.

   If the primary has a newer serial, the zone is transferred again. Serial
   numbers are compared the way RFC 1982 defines, so a rollover is handled.
5. **Retry and expire.** If the primary cannot be reached, the check is
   repeated at the SOA retry interval. If that goes on for the whole SOA expire
   period, the zone expires: it is no longer answered and its zone file is
   deleted. `dns_expunge_expired` decides whether its line in the zone list is
   removed as well.
6. **Removed.** A delete from DirectAdmin, the dashboard or `--domain delete`
   removes the zone from memory, the zone list and the disk.

A transfer that finishes after its zone was removed or given another primary
is discarded.

### NOTIFY

A NOTIFY is honoured only when it comes from the address recorded as that
zone's primary. Addresses are compared as addresses, not as text, so the way
an IPv6 address is written in the zone list does not matter. Others are logged and ignored:

```
TRANSFER ERROR: notify example.com. primary 203.0.113.9 - not allowed to transfer zone to us
```

If your primary sends NOTIFY from a different address than the one zone
transfers are served on, changes still arrive, at the next refresh.

### Transfers

Zone transfers are AXFR over TCP to port 53 of the primary, which may be an
IPv4 or an IPv6 address. `transfer_queue`
limits how many run at once, and a zone never has two transfers in flight.
IXFR and TSIG are not supported, so access control on the primary is by
address (`allow-transfer`).

## Zone states

Shown on the Domains page and by `--dump-db`.

| State | Answered | Meaning |
|---|---|---|
| `synced` | yes | The primary has confirmed the copy held here. The normal state. |
| `unsynced` | yes | Answered from stored data that the primary has not confirmed: the primary is unreachable or refuses. Look at the last error. |
| `awaiting` | no (SERVFAIL) | Declared, never transferred. |
| `unverified` | no (SERVFAIL) | Restored from disk at start, serial not confirmed yet. |
| `transfering` | as before the transfer | A transfer is running now. |
| `empty` | | Loaded without records. Should not happen; reload the zone. |
| `notloaded` | no | In the zone list but not in memory: a reload is pending or the zone failed to load. |
| `undeclared` | yes | Still in memory but no longer in the zone list. Disappears at the next reload. |

`unsynced` is the one to watch. A zone can sit in it for a long time and still
be answered, with data that may be out of date.

## Startup check

While the service is stopped, zones can change on their primaries, and the
refresh timer restored with a zone may be hours away. So at start each zone
loaded from disk is compared with its primary by one SOA query before it is
answered. A zone whose serial moved is transferred first.

- Zones are grouped by primary. Each primary receives at most
  `startup_check_rate` queries per second.
- A primary that does not answer five probes in a row is treated as
  unreachable; its remaining zones are answered from stored data right away.
- `startup_check_grace` sets a deadline after which everything still unchecked
  is answered from stored data.
- The check runs at start only, not on a reload.

The trade-off: with the check on, a restart of a large installation answers
SERVFAIL for a zone until its turn comes. Resolvers then ask your other name
servers, which is why a zone should never have a single name server. If that
window is not acceptable, raise `startup_check_rate`, set
`startup_check_grace`, or turn the check off with `startup_check off`.

## DNSSEC

DirectSlave serves signed zones; it does not sign them. Signing happens on the
primary. The AXFR carries the DNSKEY, RRSIG, NSEC or NSEC3 records, and
DirectSlave stores and serves them unchanged.

For a query with the DO bit set it adds the signatures and, for negative
answers, the NSEC or NSEC3 records that prove the name or type does not exist,
including the wildcard cases.

What this means in practice:

- Enable DNSSEC for the domain in DirectAdmin as usual. Nothing has to be
  configured on the secondary.
- Signatures expire. The primary re-signs and raises the serial; the secondary
  must be able to transfer the zone before the old signatures run out. A signed
  zone stuck in `unsynced` will eventually fail validation, so watch for it.
- The Domains page marks signed zones. A zone shown as half signed (a DNSKEY
  set without a signed SOA, or the reverse) points at a problem on the primary.

## Reload

```sh
service directslave reload      # FreeBSD
systemctl reload directslave    # Linux
kill -HUP "$(cat /usr/local/directslave/run/directslave.pid)"   # either, by hand
```

A reload re-reads the zone list and reconciles it with memory: unchanged zones
are not touched, new ones are loaded, zones with a new primary are transferred,
zones that left the list are dropped. Queries are answered throughout. It also
reopens the log files and re-reads the HTTPS certificate. It does **not**
re-read `directslave.conf`.

## Query log

Off by default. With `dns_query_log all` every query produces a line:

```
2026/09/10 20:51:12 QUERY: proto=udp client=127.0.0.1 id=34872 qname=static.example.com. qtype=A qclass=IN rcode=NOERROR aa=1 zone=static.example.com. answers=1 authority=0 latency_us=87 write=ok
```

`errors` logs only negative and failed answers, which is usually what you want
when looking for a misconfigured zone. Writing is asynchronous: under load, log
lines are dropped (and counted in a `QUERY_LOG: dropped=N` line), queries are
not slowed down.

## Checking a zone from outside

```sh
dig @192.0.2.20 example.com SOA +norecurse
dig @192.0.2.20 example.com AXFR            # expected to fail: no outgoing transfers
dig @192.0.2.20 example.com DNSKEY +dnssec +norecurse
```

Compare the SOA serial with the primary's:

```sh
dig @192.0.2.53 example.com SOA +short
dig @192.0.2.20 example.com SOA +short
```

## Limits

- Secondary only.
- AXFR only: no IXFR, no TSIG.
- One primary per zone when the zone is added through DirectAdmin.
- No views, no response rate limiting. Put rate limiting in the firewall if you
  need it.
