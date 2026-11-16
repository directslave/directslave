# DirectSlave v4 + integrated DirectDNS

This tree is based on the supplied `directslave-3` source, with the current
DirectSlave 3.x API compatibility fixes carried forward and the supplied
standalone DirectDNS 4.0 server integrated into the same process.

## Compatibility policy

Legacy installations remain on the external DNS backend unless they explicitly
enable the integrated server. Existing `directslave.conf` files therefore do
not need the new DNS keys.

Default behavior when `dns_server` is absent:

```text
dns_server = external
DirectSlave -> named_conf -> rndc -> BIND/named
```

Integrated behavior is enabled with:

```text
dns_server integrated
```

(`internal` and `directdns` are accepted aliases.)

The existing `named_conf` one-line zone declarations remain the control-file
registry in both modes. This keeps the DirectSlave UI, ownership checks and an
existing zone list compatible while allowing the integrated DNS engine to load
the same declarations.

## DirectAdmin API compatibility

The DNS API handler includes the supplied/current 3.x compatibility behavior:

- GET/query and form-urlencoded POST delete requests;
- `domain=` and `select0..selectN` deletion formats;
- idempotent deletion of a zone which is already absent;
- preservation of the raw request body for `dns_write_post.sh`;
- `zonedir` is passed to the external DNS hook;
- zone ownership remains tied to the primary/client IP.

The HTTP routes, authentication model, Web UI paths, external scripts and
legacy BIND/rndc path are retained.

## Integrated DNS path

When integrated DNS is enabled:

```text
DirectAdmin /CMD_API_DNS_ADMIN
        -> existing DirectSlave ownership/control-file logic
        -> in-memory authoritative zone registry
        -> SOA check / AXFR from primary
        -> persistent text zone file
        -> authoritative UDP + TCP DNS listener
```

Implemented behavior includes:

- UDP and TCP authoritative service;
- NOTIFY validation against the configured primary;
- SOA serial polling;
- AXFR;
- bounded AXFR concurrency;
- per-zone overlapping-transfer suppression;
- refresh/retry/expire timers;
- persistent zone files and startup reload;
- BIND text zone parsing (`$ORIGIN`, `$TTL`, multiline records, relative names);
- atomic replacement of transferred zone files;
- authoritative NXDOMAIN/NODATA responses with SOA authority data;
- CNAME and basic wildcard matching;
- SERVFAIL while a new zone is waiting for its initial AXFR, avoiding false
  cacheable NXDOMAIN answers;
- stale AXFR results are discarded if a zone is removed or edited while a
  transfer is running;
- SIGHUP reloads the integrated zone registry from `named_conf`;
- clean DNS shutdown on the existing termination path.

RNDC is not started or called in integrated mode.

## Privileges

Legacy external mode keeps the existing DirectSlave startup model.

Integrated mode needs to bind port 53 before dropping privileges. The process
therefore binds UDP/TCP DNS sockets first and then switches to the configured
`uid`/`gid`. In background mode daemonization/re-exec happens before the bind so
the child still has the privilege required to open port 53.

If a non-privileged DNS port is configured, root is not inherently required by
the DNS listener itself.

## Configuration

See `directslave.conf.v4-additions.example`. The new keys are optional. Defaults
were selected so an unmodified 3.x config stays on the external backend.

## Version notes

The update checker now compares numeric versions instead of treating any
non-equal version as newer; this prevents the public 3.x version endpoint from
being reported as an "upgrade" to a local 4.x build.

## DNS query logging

Integrated mode now supports an optional asynchronous query log:

```text
dns_query_log        off|errors|all
dns_query_log_file   /usr/local/directslave/log/dns-query.log
dns_query_log_buffer 8192
```

The default is `off`, so existing configurations are unchanged. Query logging is
non-blocking on the DNS request path; when the bounded queue is full, log entries
are dropped and a `QUERY_LOG: dropped=N reason=buffer_full` summary is emitted
when the writer catches up.

Example:

```text
2026/09/10 20:51:12 QUERY: proto=udp client=127.0.0.1 id=34872 qname=static.example.com. qtype=A qclass=IN rcode=NOERROR aa=1 zone=static.example.com. answers=1 authority=0 latency_us=87 write=ok
```
