# Foundation implementation plan

This standalone plan preserves the original product goals while describing the
actual Go boundaries. It is not permission to provision resources, infer
approvals, run models, or publish sensitive data.

## Product and architecture decisions

- One native worker computer/identity/service and one active work slot.
- Every non-Founder is autonomous/unattended; only Founder coordinates human
  needs and explicitly pauses/resumes global mode. Delegates may be AI agents.
- Persistent scoped Founder-issued assistance authorization survives restarts
  and live-connection expiry; targets need no human or repeated consent.
- One authoritative TLS API and embedded SQLite service; agents never write the
  server database directly. Service-controlled policy applies to every client.
- Loopback listeners plus pinned SSH and TLS for remote machines.
- Dedicated account ownership, not unrestricted root or hardware attestation.
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
| 2 | Bootstrap/invitations, identity/grants/revocation, heartbeat, native service/tunnel adapters, encrypted backup/restore | Real local TLS/SSH, recording OS adapters, native deployment/reboot checks reported separately |
| 3 | Projects/tasks, approval/verification, histories/revert, discussions/mentions/watch, local knowledge/inventory/workspaces | Raw API policy/races and offline persistence |
| 4 | Codex/Copilot adapters/install/update, protected authentication, private messages, consented tmux/VNC relays | Synthetic inference; real local streams; no implicit live-provider/desktop certification |
| 5 | Onboarding, separate scope/acceptance review, actual-answer refinement, signoff, serial execution and independent delegated verification/rework, pause/retry/recovery/outbox, idle research | Native binary and isolated Git/PR fixtures, exact-result authority, crash/lost-ack/restart fences; completed != accepted; no live vendor/GitHub qualification |
| Operational continuity | Six portable skills, full development/operating guide, offline binary access, package/home discovery and explicit prompt context | Current-source guide/catalog/install/prompt checks; preserve user data and modes |

The first usable vertical slice is installation, fresh Founder and worker
enrollment, project/task creation, authorized assignment/readiness, approved
execution and separate verification. Missing provider authentication remains an
explicit prerequisite, not success or an empty-work result.

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
