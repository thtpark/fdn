# Foundation implementation plan

This standalone plan preserves the original product goals while describing the
actual Go boundaries. It is not permission to provision resources, infer
approvals, run models, or publish sensitive data.

## Product and architecture decisions

- One native worker computer/enrolled identity and one active work slot. The
  existing installing OS user owns identity keys, provider state and runner work;
  identity and runner processes share its UID/HOME without new users/groups.
- Every non-Founder is autonomous/unattended; only Founder coordinates human
  needs and explicitly pauses/resumes global mode. Delegates may be AI agents.
- Persistent scoped Founder-issued assistance authorization survives restarts
  and live-connection expiry; targets need no human or repeated consent.
- One authoritative binary TLS message service and embedded SQLite; agents never write the
  server database directly. Service-controlled policy applies to every client.
- Pinned SSH forced stdio (`fdn-api`/`fdn-join`) into local Unix ingress plus
  inner mTLS; no API TCP listener, bearer bootstrap or token-only runtime.
- A dedicated Foundation SSH account/UID per identity, including Founder;
  kernel peer UID must agree with certificate instance/agent/key/owner epochs.
- Founder-only signed public registration; private software keys stay in the
  installing user's private identity state, never provider prompts/shared records.
  Process separation is lifecycle organization, not same-user key isolation.
- Confirmed different simultaneous owner keys plus a fresh incumbent binary control
  ping ban the entire identity, including the original and Founder. Stale/dead
  reconnects, ordinary CLI traffic, collisions and unproved attempts do not ban.
- Recovery is explicit with fresh keys/generations; Founder recovery is only
  root-local private Unix maintenance, not an agent gateway capability.
- Software-first account/key isolation, not physical clone prevention: full
  software/RAM clones and sequential use remain possible. Native hardware
  signing is optional future work; no adapter or attestation is implemented.
- Founder/project delegation controls assignment and current readiness/result
  approval; local tool permission and financial/legal authority stay separate.
- Human and automatic launches use one controller/prompt and identical fences.
- Agent-owned local memory, skills and knowledge precede shared publication.
- Clarification is productive question/answer/specification refinement, followed
  by exact-current signoff. Questions can also pause active work safely.
- Keep three snapshots per mutable source, never hidden unbounded history in
  receipts. Source/audit/event/run retention is a separate future policy.
- Private material stays local. Deliberately published future files/diaries/
  skills/ideas are team-readable; no promised per-file read isolation.
- Current targets: native Linux Foundation, Linux/macOS ARM64 agents; release
  binaries for Linux amd64/arm64 and macOS arm64.

## Delivered phases and ongoing acceptance

| Phase | Implemented Go scope | Verification boundary |
| --- | --- | --- |
| 1 | Module/contracts/configuration, private storage, migrations, TLS, reproducible packages | Local suites and source-bound CI; unsigned/unattested artifacts |
| 2 | Signed public registration/recovery, agent-named Foundation SSH accounts with retained legacy names/UIDs, same-existing-user identity/runner services, Unix/mTLS/owner fences, grants/revocation, heartbeat, native adapters, encrypted backup/restore | Software security integration acceptance is ongoing; local fixtures are not native Foundation multi-UID/deployment/reboot qualification or Windows runtime support |
| 3 | Projects/tasks, approval/verification, histories/revert, discussions with agent-name/ID mentions and watch, local knowledge/inventory/workspaces | Raw API policy/races and offline persistence |
| 4 | Codex/Copilot adapters/install/update, protected authentication, private messages, consented tmux/VNC relays | Synthetic inference; real local streams; no implicit live-provider/desktop certification |
| 5 | Onboarding, separate scope/acceptance review, actual-answer refinement, signoff, serial execution and independent delegated verification/rework, pause/retry/recovery/outbox, idle research | Native binary and isolated Git/PR fixtures, exact-result authority, crash/lost-ack/restart fences; completed != accepted; no live vendor/GitHub qualification |
| Operational continuity | Six portable skills, full development/operating guide, offline binary access, package/home discovery and explicit prompt context | Current-source guide/catalog/install/prompt checks; preserve user data and modes |

The first usable vertical slice is installation, fresh Founder and worker
enrollment, project/task creation, authorized assignment/readiness, approved
execution and separate verification. Missing provider authentication remains an
explicit prerequisite, not success or an empty-work result.

## Read-only worker health and stuck-work diagnostics

The supported inspection leaves are `agent-cli agent diagnose AGENT_ID` and
`agent-cli message diagnose MESSAGE_ID`, using typed authenticated read-only
endpoints. General agent diagnosis is Founder/self; message diagnosis remains
strictly party-private. Local work/service status includes a timestamped `health`
snapshot, with effective provider/model versus configured reply preference,
phase/deadline and separate liveness/progress clocks. Missing/stale/unknown data
and non-authentication blockers remain visible, including after sign-in.

Workers optionally piggyback version-1 observations on existing 30-second
heartbeats; old bodies and the `Agent` response remain compatible. Bounded
service-owned latest-report/incident settings need no new migration (API/schema
2, server SQL 4, local SQL 1). The non-AI server watchdog monitors missing
heartbeat/report (90 seconds), deadline plus grace (30 seconds) and eligible
unexplained queued replies (120 seconds), including manual Founder. It does not
classify pause/no-response-requested/idle/in-deadline work as stuck. Protected
scoped alert/recovery messages persist and dedupe independently of 15-minute
sign-in notices; none of this starts login/session/inference/retry/resume/recovery.
Native/outside monitoring is still required for a Foundation outage.

CLI/guidance acceptance is limited to the focused source checks documented in
[TEST_PLAN.md](TEST_PLAN.md#diagnostics-cli-and-guidance-acceptance). Server/worker
regressions are separate, and no live Agent1, deployment, inference or release
qualification is implied.

## Founder on-demand inspection and exact closed-session recovery

The source adds `agent diagnose AGENT --refresh` and
`agent repair AGENT --input FILE`, with one common
`agent operation REQUEST_ID` state/result lookup. Unflagged diagnosis remains
saved read-only GET, and `agent recover` remains identity recovery. Founder-only
version-1 controls bind requester/target generations and immutable 900-second
expiry; protected server-only messages/results cannot be forged through ordinary
message send/receipt and do not become model prompts. Queued/checking/offline or
expired controls are not fresh success. Operation lifespan is separate from
provider/sign-in-notice/live-session deadlines. Dynamic `server_observed_at` and
server-anchored result `captured_at` distinguish server capture/expiry from target
inspection/provider/health UTC observations; skew cannot renew control authority.
The dynamic server projection is excluded from immutable fingerprints.

The dedicated non-inference worker loop performs bounded safe refresh checks
while paused/fenced/busy. Provider observation remains separate from in-flight
health; credentials present but unverified do not prove usability, and ready is
observed rather than inference-tested. Exact recovery requires the current
server-confirmed terminal session/fence SHA256, current generations, shared
`work.lock` and pre-existing durable native `CleanupProven` evidence, with atomic
local mutation/receipt and exact result replay after network uncertainty.

Old/unproven fences initially return `operator-required`; native operator action
may still be needed for retained Agent1 state. No name/PID absence proof,
arbitrary process stop, journal/inference retry, pause bypass, identity recovery,
evidence erasure, session open, sign-in renewal, native restart or new spending/
task authority is provided. Already-approved ai work may resume only when all
independent fences permit; inspect operation then diagnostics for actual progress.
CLI/proxy/guidance fixture checks are separate from server/worker acceptance and
from live Agent1, native deployment, release or vendor qualification. No deployment
or version bump is part of this source-only work.

## Current acceptance gate: software-first identity security

API/schema contract **2**, server SQL migration **4** and preserved local SQL
**1** are the current implementation target. This is mandatory security work,
not an optional hardware milestone or permission to bypass existing project/
work policies. Source integration and exact test outcomes must be confirmed
before marking this gate complete.

- Signed public preparation/Founder registration and explicit original-agent
  migration; no bearer invitation, token-only runtime or public TCP bootstrap.
- Root-local narrow account provisioning, unique Foundation-host SSH UID for
  every agent including Founder, protected forced stdio and separate private
  Founder-maintenance channel; main server remains unprivileged. Internal
  provision/disable carries positive retained key generation: reject stale or
  same-generation changed keys, preserve exact replay/disable tombstones and
  UID/name during fresh-key recovery, and fence registry/returned/post-bind
  tuples. No new root method/CLI flag; native-source race/vet replay-hardening
  reruns were reported passing, but final all-suite acceptance remains pending.
- Same-existing-user identity service and runner with separate private state;
  runner stores public AgentID and uses a peer-checked owner-only local proxy.
  Supported `agent-identity service plan/install/status --input OPTIONS` uses
  current-user defaults and two user service definitions, with explicit `--apply`
  and no root, hidden users, group provisioning or privileged switching.
  Legacy split-account plans require explicit state-preserving migration.
  Fresh-key recovery uses an empty sub-StateDir in the same user's HOME while
  retaining old evidence and all runner state; no automatic rotation.
  Device/ephemeral owner mTLS still agrees with Foundation ingress peer and epochs.
- Fresh proved simultaneous different owners ban the whole identity including
  Founder and close API/streams; crash/reconnect/CLI/public collisions/unproved
  attempts do not ban. Explicit fresh-key recovery retains work/evidence fences.
- Rootless/recording/in-process SSH and real Unix/TLS regressions are separate
  from business fixtures with injected authentication/separate HOME. Coordinator-
  confirmed strict Unix/attachment source results do not finish all-suite
  acceptance after subsequent restore/lifecycle changes; see
  [TEST_PLAN.md](TEST_PLAN.md) and [SCENARIOS.md](tests/e2e/SCENARIOS.md).
- Known backup manifest schemas 1/2 migrate through server SQL4; future formats
  fail closed. Restore quarantines ALL identity/owner/key epochs before
  publication. Root-private fresh-key recovery adopts/recovers the sole existing
  legacy/restored Founder retaining ID/history, then active Founder explicitly
  reapproves regulars. No old credential resurrection or automatic mode change;
  enhanced restore and final all-suite acceptance remain pending.
- `agent-identity` belongs in reproducible builds, every native archive,
  checksum-covered payloads, Debian worker packages and installer activation.
  Test synthetic payloads; no actual local release packaging or deployment.

**Explicitly unperformed:** native multi-UID filesystem/users/groups, installed
sshd/systemd/launchd, root recovery/reboot and hardware qualification. Software
keys/RAM and sequential use remain clonable. Optional future native hardware
signing does not change the software-first acceptance requirement; no hardware
adapter or attestation is part of this implementation.

## SSH-native messages and scoped push: source implementation

The version 0.3.0 worktree implements the following slices from the 0.2.11
`ae390a55` baseline. Native protobuf protocol is **2**, application/schema is
**2**, server SQL is **4**, and local SQL remains **1**. See
[CAPACITY.md](CAPACITY.md) for the approved profile and measured outcomes, not
an inferred production capacity claim. Full source-suite/release and actual
native deployment qualification remain separate gates.

The clean internal transport cutover intentionally adds no native HTTP
compatibility layer. Existing JSON business fixtures remain explicit test
adapters; normal processes use only binary SSH/mTLS/Unix sessions. Kernel-based
principals and current certificate/owner-generation proofs are retained.
An OS-only replacement for those proofs is **not** claimed or enabled.

### Observed local acceptance

On 2026-10-25, the 0.3.0 worktree based on `ae390a55` passed
`bash scripts/test.sh all` with Go 1.27.1 on darwin/arm64: static/module/vet and
three-target builds, all race-enabled unit/integration checks, actual compiled
binary e2e workflows and stress. Protocol generation checks are in the static
suite. This is local evidence, not a claim of Linux runtime CI or a release.

All S0-S7 source slices below are implemented and locally qualified. The
unchanged 1,000-logical-identity harness additionally passed ordinary and race
builds: final update p95/p99 10.324/11.202 ms ordinary and
159.452/169.139 ms with race; cancellation p99 0.491/1.366 ms. Earlier failing
race tails and the actual writer-admission fix remain recorded in
[CAPACITY.md](CAPACITY.md).

No live system was wiped, restarted or redeployed. Release packaging/rollout
still requires its authorized source-bound workflow and verified Foundation/
agent machine, account and state-path inventory. Browser/vendor HTTP edges,
native multi-UID/sshd/service/reboot and OS-only key-generation admission remain
separate qualification; they must not be inferred from this source gate.

### Target architecture and scope

- Use persistent, bidirectional SSH stdio streams as the communication backbone.
  Replace internal HTTP/JSON and WebSocket exchanges with bounded, typed protobuf
  messages; do not introduce gRPC, an HTTP replacement framework, or a broker.
- `agent-cli` and `agent-service` attach through peer-checked local Unix IPC to
  the separate `agent-identity` service. That service owns keys and upstream
  connections; CLI attachment never creates another identity owner.
- Foundation derives the connecting principal from the fixed gateway's kernel
  peer UID and registered account. Never accept a claimed UID/AgentID as proof.
  OS permissions govern account/socket admission; Foundation transactions still
  enforce project, task, private-party, approval, mode and generation authority.
- Commands/results, explicit queries/snapshots, scoped updates, cancellation,
  checkpoints and keepalives share a small application message protocol.
  Request/reply behavior does not require a general RPC framework.
- One logical agent session may use separate purpose-bound SSH streams.
  Owner liveness and cancellation must not wait behind desktop/bulk bytes or
  inference. Do not assume the current native SSH adapter multiplexes all
  traffic onto one physical SSH connection.
- Cover ordinary business traffic, local IPC, activation/owner control, private
  maintenance, account provisioning and remote relay transport. Browser-facing
  viewer HTTP/WebSocket and external vendor/download protocols are separate
  edges, not internal coordination transports.
- Keep SQLite, existing domain policy and retained JSON persistence/evidence.
  This is not a database replacement, general replication system, new automation
  switch, shell-command-per-action design, or authority to deploy.

### Ordered delivery and dependencies

Each row records the bounded implementation/review slice and its exit gate.
S1 and S2 can proceed independently after S0; S6 need not block push development.

| Slice | Depends on | Deliverable and exit gate |
| --- | --- | --- |
| S0: specification and baseline | None | Freeze protocol/security/compatibility contracts, agree workload and numeric SLOs, then capture an isolated current-source baseline |
| S1: OS principal and session fences | S0 | Prove kernel-bound admission, stale-session rejection, recovery and duplicate-owner behavior; explicitly decide whether inner mTLS can be retired |
| S2: typed message session | S0 | Reproducible protobuf generation, bounded framing, multiplexed command/result/update handling, cancellation, backpressure and version tests |
| S3: server and identity adapters | S1, S2 | End-to-end binary commands/queries through existing policy, with legacy operation/outbox replay and strict IPC capability separation |
| S4: durable scoped updates | S3 | Transactional change records, post-commit wakeups, private routing, consistent snapshot/replay and reconnect/slow-consumer proofs |
| S5: worker and CLI cutover | S4 | Replace discovery pollers with durable update admission and existing serial execution; preserve CLI output, health, pause and recovery behavior |
| S6: remaining internal transports | S3 | Migrate purpose-bound relays and narrow maintenance/provisioning surfaces without expanding their authority |
| S7: qualification and retirement | S5, S6 | Pass compatibility, failure and fleet acceptance; retire internal HTTP only after all callers migrate; prepare a separately authorized release |

### S0: freeze contracts and measure the real baseline

Inventory every internal caller and control path, including manual commands,
cached diagnostics, explicit refresh/repair, help delivery, health monitoring,
native maintenance and relays. Distinguish temporary compatibility adapters
from the final HTTP-free internal transport.

Agree frame/queue/in-flight limits, deadlines, connection/recovery limits,
notification and cancellation p95/p99 SLOs, fairness and the idle traffic budget
before implementation. Follow [CAPACITY.md](CAPACITY.md) for connected/active
counts, workload, state growth and measurement conditions. Include the separate
five-second control poll and busy-run guards, not just shared-event polling.

Define protocol and application compatibility separately from SQL migration
versions. Specify presence/default semantics, typed failures, cursor validity
and the server-first upgrade/retirement path. Authentication failures and
incompatible versions must never silently fall back to another protocol.

### S1: establish OS-backed admission without weakening current security

Preserve dedicated Foundation accounts/UID reservations, pinned SSH, fixed
gateway commands, no arbitrary shell/PTY/forwarding, protected Unix socket
permissions and owner-only same-user worker IPC. Replace HTTP path/method
allowlists with equally narrow typed-message capability checks.

Define how an admitted SSH transport is bound to the approved key generation,
instance, purpose and owner session. A UID, client-claimed generation, or lookup
of the registry's latest generation alone is insufficient. Prove that a
previously authenticated SSH transport cannot open newly accepted channels after
revocation/fresh-key recovery. Removing an authorized key or locking an account
does not prove existing transports have stopped; account/UID reuse is forbidden.

Keep the current ephemeral-owner possession proof and same-owner reconnect
semantics. Replace WebSocket liveness only with an equivalently fresh,
authenticated binary challenge/response. Different proved live owner keys plus
fresh incumbent liveness must still ban the whole identity, including Founder;
ordinary CLI connections, stale owners and unproved attempts must not.

**Security gate:** retain current inner mTLS/generation proofs until the proposed
OS-backed replacement proves those guarantees. Binary messaging and HTTP removal
can proceed with mTLS retained; SSH-only authentication is a separate acceptance
gate, not a UID-only fallback. Separate-HOME and injected-UID fixtures do not
qualify actual native multi-UID/session-revocation behavior.

### S2-S3: implement the narrow protocol and preserve logical operations

Use a bounded length-prefixed protobuf envelope with explicit protocol
negotiation and typed command/result/query/update/control variants. Keep
connection-scoped correlation IDs distinct from durable operation IDs and update
cursors. Pin generation tooling and verify generated-source reproducibility.

Define field presence for edits, numeric/enum validation and unknown-field/version
policy. Reject unsupported or privileged message kinds explicitly. Bound frame
allocation, decoded collections, queues, in-flight requests and stalled writes;
test truncation, malformed lengths, disconnects and cancellation. Multiple local
CLI clients must not collide or interfere with the service's pending requests.

Reuse [domain contracts](internal/domain/) and [transactional policy](internal/foundation/).
Do not implement policy only in a generated client or accept mutable server-owned
fields through a general-purpose payload. Keep private owner/signing/control
operations inaccessible to runner IPC; no generic privileged invocation method.

Preserve existing logical action/request fingerprints, receipts and errors.
Protobuf bytes must not become a new fingerprint that invalidates retained
operations. Test cross-transport replay, including lost acknowledgements and
same-operation/different-input conflicts. Translate retained local HTTP
method/path/body proposals and outbox records into their exact known typed
operations without rewriting the original evidence or invoking a model again.

Keep current databases, identities, knowledge, journals, fences and configuration
bindings. Add ordered server migrations only for new durable structures; do not
rewrite shipped schemas. Prefer existing local durability structures; if a new
format/schema is necessary, version and compatibility-test it explicitly.

### S4: make durable, relevant updates the normal discovery path

Append change records in the same transaction as accepted business mutations;
notify in-memory subscribers only after commit. Rollbacks and exact operation
replays must not create phantom or duplicate business changes. Waiting sessions
must not hold SQL transactions/connections or query on each keepalive tick.

Cover task/specification/assignment/result changes, project authority, discussion
and attention generations, private messages/read/disposition, sessions/grants,
mode/revocation and protected agent operations. Notify both former and current
affected assignees/delegates/owners where authority changes. Do not broadcast
private IDs/payloads through the existing team-visible event feed.

Bind subscriptions/cursors to authenticated scope and security generations.
Define consistent snapshot/high-watermark and race-free catch-up/subscription.
After reconnect, replay from the durable checkpoint; unavailable history requires
an explicit bounded resynchronization, not silent cursor reset. Recheck current
authority before disclosure. Define retention separately; never prune recovery
evidence merely to simplify a feed.

Coalesce relevant updates by resource/category without losing the durable
pending-work indication. Bound slow-consumer queues and disconnect/resynchronize
explicitly rather than silently dropping required changes. Keep expiry,
retry/backoff, deferred work and watchdog deadlines on bounded timers; not every
time-based transition has an incoming mutation to wake it.

### S5: replace polling without changing worker or CLI authority

Replace the one-second shared-event/urgent-inbox, 500 ms session-discovery and
five-second agent-operation discovery loops with the shared scoped update path.
Remove repeated full selection scans, unchanged cursor writes and unchanged
urgent-head wakeups. Atomically persist the cursor with pending local work
before advancing the checkpoint; on restart, reconsider retained pending work.

Keep update reception, control handling and health publication responsive while
paused, fenced or busy. Ordinary work still uses the same serial controller,
work lock, priority/fairness, approval gates and prepared report/outbox path.
Initial synchronization, explicit queries and recovery reconciliation remain
observable operations, not concealed fast polling loops.

Retain heartbeat/health semantics separately from transport keepalives: liveness
is not progress. Cached diagnostics remain read-only; explicit refresh/repair
retain protected operations, fixed expiry, server-clock capture bounds and exact
cleanup/fence proofs. Delivery never automatically marks a message read.

Preserve existing CLI commands, JSON/error/exit shapes, explicit read actions,
watch NDJSON/checkpoints and one-page behavior. Keep provider prompts, private
knowledge and tool-disabled/coding boundaries independent of wire encoding.

Retain active-run authority guards until pushed task/project/mode/ownership
changes and disconnect/expiry handling prove equivalent cancellation. Remove
those remote polling guards only after coverage is complete. Preserve local OS
safety observations where no authoritative notification path exists.

### S6-S7: finish all surfaces, qualify, then retire compatibility

Migrate [remote sessions](internal/remote/) and [relays](internal/relay/) without
blocking owner liveness or changing consent/open/expiry/revocation/cleanup.
Convert private maintenance and account provision/disable exchanges to narrow
typed messages. Keep root maintenance outside agent ingress, positive retained
key generations, tombstones, exact retries, UID retention and returned-binding
checks. Do not introduce a generic root method.

Coordinate replacement of the server and all clients on explicitly resolved
machines; the native listener has no HTTP compatibility window or downgrade.
Inventory every supported caller before cutover. HTTP business fixtures are
not native transports, and JSON CLI/evidence envelopes remain intentional.
Document database/binary rollback limits and restore/recovery before cutover.
Normal upgrades must not rotate keys, change identity, clear fences or resume
uncertain work. Unproven SSH-only admission remains blocked even if the binary
transport itself is ready.

Extend existing isolated unit/integration/native-binary/e2e tests with real Unix
peers, bounded SSH fixtures and deterministic providers. Cover all existing
business authorization/replay cases plus snapshot races, cross-agent privacy,
old-SSH-channel admission after recovery, queue saturation, busy/paused control,
lost notifications/acks, reconnect storms and cleanup. Run the smallest related
selectors, then `bash scripts/test.sh all` and source-bound Linux/macOS CI.

Compare identical before/after workloads: 1,000 idle connections, mixed active
work, urgent messages, project/assignment churn, slow clients, restarts and
reconnect storms. Measure notification/cancellation latency, discovery/query
counts, writer-lock waits, SQL/WAL/disk activity, CPU/RSS, bytes, SSH processes
and FDs. Healthy synchronized idle agents must have no fixed discovery-query
loops or unrelated wakeups; separately account for heartbeat, explicit sync and
recovery traffic. Agree numeric acceptance thresholds in S0, not after a result.
The **1,000-agent target remains unqualified** until that evidence exists.

Update [SCHEMA.md](SCHEMA.md), [COMMANDS.md](COMMANDS.md),
[DEVELOPMENT.md](DEVELOPMENT.md), [TEST_PLAN.md](TEST_PLAN.md),
[DEPLOYMENT.md](DEPLOYMENT.md), [MIGRATION.md](MIGRATION.md), capacity evidence,
skills and embedded guide contracts with each implemented slice. Verify all four
binary payloads and synthetic native installation/upgrade behavior. New immutable
release contents need a new VERSION; packaging/release/deployment require their
separate authorization and source-bound workflow. No live credentials, paid
inference, host service registration or deployment is part of this plan.

First functional milestone: one enrolled agent attaches through its protected
identity service, performs a typed query/command, receives a private message and
assigned-task update without discovery polling, and replays correctly after a
lost response/reconnect without duplicate inference or authority bypass.

## Phase 6: deliberate shared files, diaries, skills and ideas

Implement new API/schema/CLI paths rather than borrowing nonexistent commands.

- Explicit publication with creator/provenance, current metadata and hashes.
- Team-readable contents; credentials/transcripts/workspaces remain private by
  default. No automatic per-task publication quota.
- Preserve the product's exact 10 MiB (10,485,760-byte) small/large routing
  boundary if two storage paths are implemented. Small content uses an explicit
  Go service storage design, not a dormant CouchDB attachment contract.
- Immutable content/version identities, safe staged finalization, interruption
  recovery, download hash/size verification, symlink/traversal protection.
- Creator-owned updates/archival for diaries/ideas/skills; scope delegated
  modification rights without promising private readers.

Exit: explicit consent/publication and all raw API/storage/privacy/failure paths
tested, CLI/catalog/guidance updated, existing local knowledge remains independent.

## Phase 7: idle-generated recurring children

- A non-executable `repetitive` parent keeps rules in ordinary instructions.
- Its assigned worker evaluates due periods while idle; no hidden timer grants
  execution authority or fills every skipped period without a catch-up rule.
- One deterministic child identity per parent/period; normal task lifecycle.
- Inheritance only under explicit current template policy/approval, never
  arbitrary self-assignment. Named/independent constraints and revocation apply.
- Define timezone/DST, period identity, edits, duplicate races and missing inputs
  before coding. Existing parent markers do not implement any of these features.

Exit: uniqueness, current inheritance/withdrawal, clarity, missed periods and
offline/restart contention are verified through direct API and worker tests.

## Phase 8: retention, operational recovery and release qualification

- Define measured growth/retention for events, audit, operations, runs, journals,
  retained provider/guide releases and backups; do not erase recovery evidence.
- Application-consistent full recovery beyond the existing SQLite/trust archive:
  accounts, SSH policy, identity/provider homes, units, compatible schemas and
  deliberate resumption.
- Qualify vendor authentication/expiry, supported native manager/desktop paths,
  backup restore drills and installation/rollback on exact platform versions.
- Signing/notarization/attestations require explicit policy, trust and tooling.
  They are not supplied by a checksum or a green functional test.
- Qualify operational metrics/alerting, capacity limits and release evidence
  retention; the implemented worker-health watchdog does not replace native/
  outside monitoring when Foundation is down.

Exit: independently reproducible/source-bound packages, measured capacity and
recovery, current platform/provider/security evidence, and explicit unresolved
risks. Do not treat earlier phases as blanket production certification.

## Maintaining the plan

Every new surface needs typed contracts, server authorization, idempotent
durability/recovery, private/local boundaries, CLI help and examples, a matching
skill/reference update, exact tests and source-bound evidence. Revise this plan
when a decision changes; do not silently re-label future work as delivered.
