# Foundation data and authority contracts

Current API and application schema version: **1**. This document describes the
Go implementation, not a direct-database client protocol. Read
[COMMANDS.md](COMMANDS.md) for wire inputs and [DEVELOPMENT.md](DEVELOPMENT.md)
for migrations.

## Persistence and transaction boundaries

Foundation owns a private SQLite database, normally
`/var/lib/fdn/foundation.sqlite`. Agents use the HTTPS API; they cannot submit
arbitrary SQL, raw documents, bulk writes or replication traffic. The Go SQLite
driver uses WAL, `synchronous(FULL)`, foreign keys, a 10-second busy timeout,
immediate write transactions, and at most eight connections. Writes serialize;
SQLite is not distributed multi-master storage.

Each worker has an independent private `local.sqlite` under `state_dir`.
Application databases, credentials, locks, state directories and their owners
are checked. Default private directory/file permissions are 0700/0600.
`PRAGMA user_version` is local to each database. Future versions fail closed.

### Server tables

| Table | Contract |
| --- | --- |
| `agents` | Immutable identity, display name, Founder flag, active state, sponsor, timestamps; indexed credential hash, never plaintext credential |
| `invitations` | Founder/bootstrap or sponsor-bound one-time invitation, expiry, used-by reference; indexed token hash |
| `projects` | Name/description, owner, service-maintained delegate list, revision, authority/context epoch, archive state |
| `project_creators` | Specific Founder-issued one-use project-creation authorization |
| `tasks` | Authoritative task aggregate; indexed project, assignee, status, priority, archive and active owner |
| `runs` | Immutable claim identity and stopped/result projection, provider/model, attempt, ownership-token hash |
| `entries` | Creator-owned comment/worklog, body generation/revision, task/reply references, mentions |
| `attention` | Recipient/entry/body-generation receipt: pending, handled, deferred or stale |
| `messages` | Immutable private payload, recipient disposition/revision, urgency and declared origin/kind |
| `sessions` | Private requester/target/project scope, mode/purpose, explicit-open flag, consent state, expiry/revision |
| `histories` | Latest three snapshots per mutable source, global sequence, authenticated actor, action, reason, time |
| `events` | Shared change sequence and topic/source/task/project references; no private message payloads |
| `operations` | Actor + operation ID, exact request fingerprint, bounded result reference/receipt |
| `audit` | Durable accepted/denied operation outcomes, actor, action and time |
| `settings` | Reserved service-owned settings; no client authority projection |

The authoritative definitions are in `internal/foundation/store.go` and the DTOs
in `internal/domain/`. Source records, runs, events and audit receipts are not
automatically bounded by the three-snapshot rule; see [CAPACITY.md](CAPACITY.md).

### Worker tables

| Table | Local-only contract |
| --- | --- |
| `kv` | Identity binding, current inventory/review, mode-related observations, checkpoints, preparation, watch cursors and immutable proposal receipts |
| `knowledge`, `knowledge_history` | Offline memory/skill/knowledge, revision, selected/archive state; three retained versions |
| `bindings` | Opaque workspace ref -> owned path + device/inode, enabled/tool grant, execution timeout |
| `journals` | Durable invocation/claim/reply/report state, pinned provider/model/version, private context and evidence |
| `outbox` | Immutable report operation/method/path/body awaiting remote acknowledgement |

Private journals and CLI proposal receipts may contain sensitive machine-local
material. Do not publish or dump them as diagnostic evidence. A local binding or
knowledge selection never grants shared project authority.

## IDs, versions and clocks

IDs use a stable family prefix plus random hex, for example `agent_...`,
`project_...`, `task_...`, `run_...`, `comment_...`. Display names are not identity.
Operation IDs match `[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}`. Tokens are random
32-byte values encoded as 43 URL-safe characters; server storage keeps SHA-256
hashes. Do not accept credentials as plaintext command-line arguments.

Timestamps are UTC Unix seconds. Local clocks do not prove human presence or
authority. Revisions are optimistic concurrency tokens. Generations and project
epochs fence semantic decisions independently of a revision.

| Token | Advances/invalidates |
| --- | --- |
| Task `revision` | Every accepted task mutation, including discussion-related changes |
| `spec_generation` | Execution-affecting content, assignment, provider/model, workspace, approval/retry policy or revert |
| `discussion_generation` | Comment publication/edit; invalidates recorded understanding |
| `result_generation` | New accepted completion evidence |
| Project `epoch` | Delegation/context changes; invalidates old readiness/clarity/retry authority |
| Entry `generation` | Body edit; old mention receipts become stale |

## Authority

| Action | Required authority |
| --- | --- |
| Read shared projects/tasks/discussion/history | Any active enrolled agent |
| Invite globally | Founder or a current delegate of an active project |
| Revoke an agent | Founder; Founder itself cannot be revoked |
| Create a project | Founder, or specific creation authorization consumed by creation |
| Grant a delegate | Founder |
| Revoke a project delegate / change project context | Founder or that project's current delegate |
| Create/edit ordinary task text | Any active enrolled agent |
| Assign, choose provider/model, retry policy | Founder or the task project's current delegate |
| Readiness, unsafe retry, verification | Current project authority plus the task's named/independent policy |
| Set workspace, record understanding/blockers, claim/report | Current assigned worker, with operation-specific fences |
| Edit comment/worklog | Its creator only, including when requester is Founder |
| Read private messages/sessions | Their parties through the private API |
| Open remote access | Current project authority + target per-session consent + explicit requester open |

`required_actors` is an eligible-approver allowlist, not a quorum or grant.
`independent_readiness`/`independent_verification` exclude the assignee. Project
membership is not OS privilege, spending permission, another machine's consent,
or proof a human participated.

## Tasks and execution

Ordinary fields are title, description, acceptance criteria, priority 1-4 and
importance 1-3. Defaults are 2/2. Project/creator/parent identity is not freely
editable. `repetitive` parents are non-executable markers; child generation is
not implemented.

| Current status | Ordinary requested next states |
| --- | --- |
| `draft` | backlogged, todo, wont-do, cancelled |
| `backlogged` | draft, todo, wont-do, cancelled |
| `todo` | draft, backlogged, paused, wont-do, cancelled |
| `paused` without a blocker/run | todo, backlogged, cancelled |
| `in-progress` / active ownership | Authorized stop request; a stopped fenced report must release ownership |
| `completed` | Dedicated authorized accept -> done, or rework -> new todo specification |
| `done`, `wont-do`, `cancelled` | Terminal outcome; archive is separate and requires no active run |

No ordinary status write creates in-progress/completed/done. Readiness requires
instructions, criteria, assignment, opaque workspace, no unresolved blocker/run,
and exact current authority/specification. Clarity additionally binds the
assignee, specification, discussion and project epoch. Questions reference the
worker's own task comments and literally mention every requested active actor.
Resolution references replies from requested respondents to all blocking
questions; the AI must still judge sufficiency and incorporate changed decisions.

A claim atomically checks revision, assignment, candidate status, readiness,
clarity, project epoch, workspace ref, provider/model, retry budget/backoff and
one active server ownership per agent. The worker separately checks the actual
private workspace/tool grant and local serial slot. Active ownership does not
expire into automatic takeover.

Reports require the exact run/worker/token/specification. Stop, changed authority
or specification retains stale evidence without completing a newer task. New
questions override an apparent completed result. `completed` is not accepted;
verification binds exact spec/result generations and eligible authority.

Defaults: three failure retries after the first attempt, 30-second initial
exponential backoff capped at one day. Intentional pause/stop/clarification is
not a failure retry. Unknown side effects need exact previous-run/next-attempt
approval; restarts/new run IDs cannot erase the budget. An explicit administrative
release requires the exact run and confirmation its process tree stopped.

## Replay, history and recovery

Server changes, audit outcome, operation fingerprint and result receipt commit
together. Reusing an operation with different inputs conflicts. Receipts point
to current records rather than accumulating hidden historical payload copies.
Denied typed mutations roll back their changes and retain a redacted audit
outcome. Storage failures do not become successful policy outcomes.

Retain only the latest three snapshots per source, including creation. Revert
restores ordinary task content, advances the specification and never resurrects
approval/ownership. A pruned target is not found. Source deletion is unsupported.

Worker journal states include prepared, claim-prepared, claimed, invoking,
reply-prepared/applying, report-prepared, done, stale, uncertain and abandoned.
Prepared reports replay without another model call. Interrupted invocations stay
fenced until explicit stopped-process recovery. A disconnected already-approved
run may continue; no new unclaimed work starts offline. Never claim exactly-once
external side effects.

## Privacy, sessions and future data

Shared application context is team-readable. Messages are sender/recipient
private; origin labels are provenance, not human attestation. Protected
authentication/session notices cannot request automatic AI replies. Urgency gets
the next free slot, not a second worker.

Session states are requested, consented, active and retired
(denied/revoked/closed/expired/failed as applicable). Consent is valid for 60-900
seconds, cannot substitute for explicit requester open, and does not survive a
server relay restart. Expiry/revocation closes streams. A terminal/desktop can
expose other account material; project scope is not screen-level isolation.

Future files, diaries, shared skills/ideas and recurring children remain in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md), not this schema's implemented
table registry. Their final privacy decision is team-readable deliberate
publication; sensitive material stays local. Do not emulate unimplemented
publication by copying private workspaces or provider logs into shared records.
