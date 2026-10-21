# Capacity and scalability

**There is no verified 1,000-agent capacity claim.** Agent-record count, task
count, connected clients and simultaneously active writers are different loads.
Expensive model/tool execution runs on workers; Foundation coordinates state.

## Current evidence and costs

The local stress suite creates 1,000 tasks, paginates without duplicates, applies
192 accepted updates through 24 concurrent writers with optimistic retries,
checks integrity and retains exactly three histories. It is a correctness and
contention workload, not a network/agent throughput benchmark. Hardware and
race-detector overhead affect elapsed time; do not convert a prior run's seconds
into a production throughput guarantee.

Code-derived healthy idle traffic per worker:

| Activity | Interval | Approximate requests/sec for 1,000 workers |
| --- | --- | --- |
| Shared events | 1 second | 1,000 |
| Urgent inbox | 1 second | 1,000 |
| Remote sessions | 500 ms | 2,000 |
| Heartbeat | 30 seconds | 33 |

This is roughly 4,000 requests/second before task preparation/reporting. It is a
derived load estimate, not measured supported throughput. Slow responses,
coalesced ticks and active sessions change the actual rate.

The current event watch is not recipient-scoped: unrelated shared changes can
wake every worker and amplify selection traffic. A connected worker also keeps
an SSH transport; reconnect storms exercise SSH admission, processes, sockets,
memory and file descriptors as well as the Go API.

SQLite permits concurrent readers but serializes write transactions. WAL/full
synchronization, transaction duration, the eight-connection pool, busy timeout,
disk latency and checkpoint behavior determine practical limits. Never weaken
durability or authorization just to pass a throughput test.

## Required local fleet benchmark

Before implementation, agree expected connected/active counts, workload mix,
task/entry/message rates, session concurrency, state size/growth, latency SLOs,
error budget, test duration and recovery limits. Do not invent acceptance
thresholds after seeing a convenient result.

Use isolated local processes/in-process protocol fixtures, real temporary
SQLite and TLS, deterministic provider executables and bounded native SSH
fixtures. No VMs, containers, paid inference or changes to deployed services are
needed. Include at least:

1. 1,000 connected idle identities and a mixed active/idle fleet.
2. Task assignment/spec changes, claims, comments, urgent messages and reports.
3. Slow consumers, disconnected workers and simultaneous reconnect/restart.
4. Lost acknowledgements, exact replay and unsafe-side-effect fences.
5. Long-running growth, index/query plans, history bounds and disk pressure.

Measure API p50/p95/p99, successful/error/conflict rates, SQLite transaction and
busy-wait latency, CPU/RSS, disk latency/growth/WAL/checkpoints, sockets/FDs, SSH
processes/admission, wakeup fairness, and drain/recovery time. Keep source,
hardware, toolchain, workload parameters and raw redacted measurements.

## Improvement order

1. Consolidate fixed polling into bounded agent-scoped long polling or an event
   stream; preserve current revocation/consent and durable cursor semantics.
2. Avoid unrelated wakeups; add reconnect jitter and measured backoff. Keep
   urgent handling fair and serialized with actual work.
3. Reduce duplicate reads and keep transactions short. Consider heartbeat
   coalescing only with an explicit presence/durability contract.
4. Size a server with sufficient sustained CPU, memory and disk performance
   from measured load, not an untested machine-type guess.
5. Consider PostgreSQL or a different persistence boundary if sustained write
   contention or high-availability requirements justify the migration.

A larger database or VM alone does not fix a polling amplification bug. SQLite
is not categorically unsuitable for 1,000 agents, but neither it nor the current
single-host deployment is automatically a 1,000-agent or high-availability
solution. This roadmap does not authorize cloud expenditure.
