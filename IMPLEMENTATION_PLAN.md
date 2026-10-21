# Foundation implementation plan

This standalone plan preserves the original product goals while describing the
actual Go boundaries. It is not permission to provision resources, infer
approvals, run models, or publish sensitive data.

## Product and architecture decisions

- One native worker computer/enrolled identity and one active work slot, with
  distinct protected credential-service and provider/tool-runner UIDs/HOMEs.
- Every non-Founder is autonomous/unattended; only Founder coordinates human
  needs and explicitly pauses/resumes global mode. Delegates may be AI agents.
- Persistent scoped Founder-issued assistance authorization survives restarts
  and live-connection expiry; targets need no human or repeated consent.
- One authoritative TLS API and embedded SQLite service; agents never write the
  server database directly. Service-controlled policy applies to every client.
- Pinned SSH forced stdio (`fdn-api`/`fdn-join`) into local Unix ingress plus
  inner mTLS; no API TCP listener, bearer bootstrap or token-only runtime.
- A dedicated Foundation SSH account/UID per identity, including Founder;
  kernel peer UID must agree with certificate instance/agent/key/owner epochs.
- Founder-only signed public registration; private software keys stay in the
  separate identity-service account, never the provider runner or its prompts.
- Confirmed different simultaneous owner keys plus a fresh incumbent WebSocket
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
| 2 | Signed public registration/recovery, per-identity SSH accounts/UIDs, separate credential service/runner, Unix/mTLS/owner fences, grants/revocation, heartbeat, native adapters, encrypted backup/restore | Software security integration acceptance is ongoing; real local Unix/TLS/SSH and recording account fixtures are not native multi-UID/deployment/reboot qualification |
| 3 | Projects/tasks, approval/verification, histories/revert, discussions/mentions/watch, local knowledge/inventory/workspaces | Raw API policy/races and offline persistence |
| 4 | Codex/Copilot adapters/install/update, protected authentication, private messages, consented tmux/VNC relays | Synthetic inference; real local streams; no implicit live-provider/desktop certification |
| 5 | Onboarding, separate scope/acceptance review, actual-answer refinement, signoff, serial execution and independent delegated verification/rework, pause/retry/recovery/outbox, idle research | Native binary and isolated Git/PR fixtures, exact-result authority, crash/lost-ack/restart fences; completed != accepted; no live vendor/GitHub qualification |
| Operational continuity | Six portable skills, full development/operating guide, offline binary access, package/home discovery and explicit prompt context | Current-source guide/catalog/install/prompt checks; preserve user data and modes |

The first usable vertical slice is installation, fresh Founder and worker
enrollment, project/task creation, authorized assignment/readiness, approved
execution and separate verification. Missing provider authentication remains an
explicit prerequisite, not success or an empty-work result.

## Current acceptance gate: software-first identity security

API/schema contract **2**, server SQL migration **3** and preserved local SQL
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
- Separate identity-service and runner UIDs/HOMEs/keys; runner stores only public
  AgentID and uses a peer-checked local Unix proxy. Supported `agent-identity
  service plan/install/status --input OPTIONS` uses exact preprovisioned marked
  accounts/configurations, fixed native definitions and explicit native-root
  apply for install, never account adoption. Fresh-key recovery uses an empty
  sub-StateDir in the same identity HOME while retaining old private evidence
  and all runner state; no `--rotate` or normal-retry rotation. Device control
  WS/ephemeral owner mTLS agree with kernel peer and instance/key/owner epochs.
- Fresh proved simultaneous different owners ban the whole identity including
  Founder and close API/streams; crash/reconnect/CLI/public collisions/unproved
  attempts do not ban. Explicit fresh-key recovery retains work/evidence fences.
- Rootless/recording/in-process SSH and real Unix/TLS regressions are separate
  from business fixtures with injected authentication/separate HOME. Coordinator-
  confirmed strict Unix/attachment source results do not finish all-suite
  acceptance after subsequent restore/lifecycle changes; see
  [TEST_PLAN.md](TEST_PLAN.md) and [SCENARIOS.md](tests/e2e/SCENARIOS.md).
- Known backup manifest schemas 1/2 migrate through server SQL3; future formats
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

## Next work: verify scale before adding infrastructure

The **1,000-agent target is unqualified**. Current fixed polling creates roughly
four baseline requests/second per idle healthy worker before task traffic.
Follow [CAPACITY.md](CAPACITY.md): establish workload/SLOs, add a local agent-fleet
load harness, eliminate unrelated wakeups and consolidate polling, measure
transport/SQLite/host limits, then size or change storage from evidence.

This milestone should not accidentally add a broker, distributed authority,
second automation switch, approval bypass, or paid benchmark. Keep current
revocation, fairness, bounded output, cancellation and report-replay semantics.

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
- Establish metrics, alerting, capacity limits and release evidence retention.

Exit: independently reproducible/source-bound packages, measured capacity and
recovery, current platform/provider/security evidence, and explicit unresolved
risks. Do not treat earlier phases as blanket production certification.

## Maintaining the plan

Every new surface needs typed contracts, server authorization, idempotent
durability/recovery, private/local boundaries, CLI help and examples, a matching
skill/reference update, exact tests and source-bound evidence. Revise this plan
when a decision changes; do not silently re-label future work as delivered.
