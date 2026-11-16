# DirectSlave v4 / integrated DirectDNS options.
#
# These options are OPTIONAL. If they are absent, DirectSlave keeps the legacy
# external BIND/rndc behavior used by DirectSlave 3.x.
#
# Append the required options to an existing directslave.conf.

# integrated/internal/directdns -> serve authoritative DNS inside DirectSlave
# external                  -> legacy BIND/rndc backend (default)
dns_server          integrated

# Authoritative DNS listener, one directive per address family. Each takes "*"
# (every address of that family), one address, a list separated by spaces or
# commas, or "none" to leave the family off. Hostnames are not accepted.
# With only one of the two set, the other family is off; with neither set,
# both are "*". On a host without IPv6 set dns_listen_ip6 to none.
# (The single "dns_listen" of early 4.0 builds still works but is deprecated.)
#
# Port 53 is bound before uid/gid privileges are dropped, so native daemon mode
# must initially be started with sufficient privileges to bind it.
dns_listen_ip4      *
dns_listen_ip6      *
dns_port            53

# Optional dedicated DNS log. If omitted, action_log is used.
dns_server_log      /usr/local/directslave/log/dns.log

# Optional per-query DNS log. Disabled by default.
# off    - no query log
# errors - log negative/error responses only (NXDOMAIN, SERVFAIL, REFUSED, etc.)
# all    - log every DNS QUERY request
#
# Query logging records client IP addresses and queried names. Enable it only
# when that operational data is required. Logging is asynchronous; if the
# bounded buffer fills, query-log entries are dropped instead of delaying DNS.
dns_query_log        off
dns_query_log_file   /usr/local/directslave/log/dns-query.log
dns_query_log_buffer 8192

# Temporary SOA timers used before the first successful AXFR. After transfer,
# the timers from the zone SOA are used.
dns_new_refresh     300
dns_new_retry       60
dns_new_expire      604800

# 0: expire only from memory/storage; keep the zone declaration in named_conf.
# 1: also remove the expired zone declaration from named_conf.
dns_expunge_expired 0

# Maximum number of concurrent AXFR operations.
transfer_queue      20

# Startup consistency check.
#
# While DirectSlave is down a zone can change on its primary, and the refresh
# timer restored with that zone may be hours away. At start every zone loaded
# from local storage is therefore confirmed against its primary with a single
# SOA query, and it is NOT answered (SERVFAIL) until that answer arrives; a
# zone whose serial drifted is transferred first. This runs once, at start, and
# never on a SIGHUP reload of a running instance.
#
# on  - withhold stored zones until their serial is confirmed (default)
# off - answer stored zones immediately, as v4.0.2 did
startup_check         on

# Pacing of the check. Zones are grouped by primary and every primary is probed
# by one worker at no more than startup_check_rate queries per second, while
# startup_check_workers caps the probes in flight over all primaries. Raise the
# rate on installations with tens of thousands of zones: the check needs about
# (zones on the busiest primary / rate) seconds before those zones are answered.
# A primary that misses 5 probes in a row is declared unreachable and its
# remaining zones are answered from stored data right away.
startup_check_workers 20
startup_check_rate    20

# Deadline, in seconds, after which zones that are still unverified are answered
# from stored data anyway. 0 keeps a zone withheld until it is confirmed or its
# primary is found unreachable. Set it to bound the window in which a large
# installation answers SERVFAIL after a restart.
startup_check_grace   0

# Existing DirectSlave profiler. These are optional; defaults preserve 3.x
# behavior (enabled on localhost:2223).
pprof_enable        1
pprof_port          2223
