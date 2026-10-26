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

Pre-cutover version 0.2.11 code-derived healthy idle traffic per worker:

| Activity | Interval | Approximate requests/sec for 1,000 workers |
| --- | --- | --- |
| Shared events | 1 second | 1,000 |
| Urgent inbox | 1 second | 1,000 |
| Remote sessions | 500 ms | 2,000 |
| Diagnostic/recovery controls | 5 seconds | 200 |
| Heartbeat | 30 seconds | 33 |

This is roughly 4,200 requests/second before task preparation/reporting. It is a
derived load estimate, not measured supported throughput. Slow responses,
coalesced ticks and active sessions change the actual rate. API/schema 2 also
adds identity owner-control/renewal, inner mTLS and API/join SSH stdio costs;
the table does not measure or include those costs. Rebenchmark the integrated
security transport rather than carrying forward a pre-upgrade capacity claim.

### SSH cutover acceptance profile and baseline

The approved source-fixture profile is 1,000 connected logical identities,
no periodic business-discovery polling, scoped-update latency p95 <= 250 ms
and p99 <= 1 second, and priority-control cancellation p99 <= 1 second.
This is an isolated local-harness gate, not a production/native capacity claim.
Transport keepalive, the existing 30-second health heartbeat, explicit initial
synchronization and recovery are accounted for separately.

Before the cutover, version 0.2.11 source `ae390a55` was sampled on Go 1.27.1,
darwin/arm64, using temporary SQLite and an in-process TLS business fixture.
After bounded connection warmup, 1,000 distinct actors completed 18,900
discovery requests in 5.012 seconds (3,770.7/second), with zero errors:
request latency p50 16.061 ms, p95 32.704 ms and p99 39.251 ms.
These are request latencies, not notification-delivery latencies. The sampled
one-second/500-ms/five-second discovery pattern excludes heartbeat, work
selection, provider calls and real multi-UID SSH. The host was not dedicated.
An un-warmed synchronized connection burst produced connection errors, so
cold/reconnect qualification must remain distinct from the warm steady state.

### Protobuf push harness results

The approved unchanged harness uses 1,000 admitted logical identities with
distinct ephemeral mTLS certificates, loopback TLS 1.3/protobuf, temporary SQLite
and trusted server-side principal binding. Each timed run performs 128 real
commands through eight writers, delivers 256 scoped updates, checks 128 remote
cancellations and leaves 744 unrelated subscribers blocked. It verifies exact
replay, privacy, no implicit message reads and owned-resource cleanup.

On Go 1.27.1, darwin/arm64, a shared 16-CPU host, the initial instrumented race
runs failed update tails (p99 2.89/3.83 seconds); those are retained failures.
Investigation found eight blocked IMMEDIATE writers monopolizing the
eight-connection pool and private commits scanning unrelated agent records.
Context-aware writer admission before connection acquisition, lazy
transaction-local Founder lookup and deferred read snapshots fixed the root
causes without weakening WAL/FULL durability or authority checks.

Two unchanged race-enabled reruns then passed: update submission-to-delivery
p95/p99 **159.303/168.235 ms** and **149.250/170.388 ms**; cancellation p99
**2.314 ms** and **1.330 ms**, with zero unexpected errors or duplicate delivery.
Submission timing includes the mutation itself and is conservative for
post-commit delivery. Idle transport pings caused no business-handler requests.

A separate uninstrumented pre-fix run also passed (update p95/p99
81.016/182.931 ms; cancellation p99 0.515 ms), illustrating why race and ordinary
timings must not be mixed. That run retained 2,019 FDs, about 75 MiB Go heap and
204,192 KiB RSS at steady state, then released all owned connections/watches.
These measurements are not a guarantee for native SSH process/account fleets,
other state sizes, hardware, workload, desktop traffic or production deployment.
No native multi-UID or full-host recovery qualification is implied.

Final-source unchanged-harness reruns after the writer fix passed both ordinary
and race builds. Update p95/p99 were **10.324/11.202 ms** ordinary and
**159.452/169.139 ms** with race detection; remote cancellation p99 was
**0.491 ms** and **1.366 ms**, respectively. Both runs had zero unexpected
errors, no idle business-handler requests and complete owned-resource cleanup.
The ordinary run held 2,019 FDs, 57,117,184 Go heap bytes and 229,056 KiB RSS at
steady state. These are logical-identity source fixtures, not production SLOs.

The pre-cutover event watch was not recipient-scoped: unrelated shared changes
could wake every worker and amplify selection traffic. The protected identity service
also maintains owner control and API/join SSH streams; reconnect storms exercise
SSH admission, certificate/owner renewal, processes, sockets, memory and file
descriptors as well as the Go API. Do not reduce peer/key/owner-generation checks
or duplicate-proof freshness to meet a load target.

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
6. Per-identity SSH admission/account reservations, device/owner renewal, owner
   reconnect storms and revocation/ban stream shutdown under load. Preserve the
   fresh proved-duplicate threshold; dead owners must not create mass false bans.

Measure API p50/p95/p99, successful/error/conflict rates, SQLite transaction and
busy-wait latency, CPU/RSS, disk latency/growth/WAL/checkpoints, sockets/FDs, SSH
processes/admission, wakeup fairness, and drain/recovery time. Keep source,
hardware, toolchain, workload parameters and raw redacted measurements.

## Improvement order

1. Qualify the implemented scoped durable push over the SSH-native message
   session; see the
   [implementation plan](IMPLEMENTATION_PLAN.md#ssh-native-messages-and-scoped-push-source-implementation).
   Preserve admission, revocation/consent and cursor/replay guarantees.
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
