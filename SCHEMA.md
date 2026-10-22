# Foundation data and authority contracts

Current API and application schema version: **2**. This document describes the
Go implementation contract, not a direct-database client protocol. Read
[COMMANDS.md](COMMANDS.md) for wire inputs and [DEVELOPMENT.md](DEVELOPMENT.md)
for migrations.
Server SQLite migration version is **3**; local-worker SQLite remains **1**.
Deploy the server first. Version 2 requires account-bound public-key admission
and mutual TLS; legacy bearer credentials/invitations cannot authenticate or
create new runtime identities. Native multi-UID qualification is separate from
source-bound software tests and has not been performed.

## Persistence and transaction boundaries

Foundation owns a private SQLite database, normally
`/var/lib/fdn/foundation.sqlite`. Agents use the inner HTTPS API over pinned SSH
stdio/Unix ingress; they cannot submit
arbitrary SQL, raw documents, bulk writes or replication traffic. The Go SQLite
driver uses WAL, `synchronous(FULL)`, foreign keys, a 10-second busy timeout,
immediate write transactions, and at most eight connections. Writes serialize;
SQLite is not distributed multi-master storage.

Each runner has an independent private `local.sqlite` under `state_dir`.
The distinct identity service owns its own protected key/profile state, not that
runner database. Application databases, configurations, locks, state directories
and their owners are checked. Default private directory/file permissions are
0700/0600; the deliberately shared IPC path is described below.
`PRAGMA user_version` is local to each database. Future versions fail closed.

### Server tables

| Table | Contract |
| --- | --- |
| `agents` | Immutable identity, display name, Founder/active/banned state, sponsor, timestamps, mode/revision and Founder mode-request attribution; legacy credential hashes are retained but cannot authenticate v2 |
| `invitations` | Retained legacy records only; no bearer invitation/bootstrap redemption in v2 |
| `agent_identities` | Instance/agent/request binding, unique dedicated SSH user/UID, approved public keys/fingerprints, key and owner generations, active/banned state and software assurance |
| `identity_accounts` | Durable one-agent/one-UID/one-SSH-user reservation; no reuse by another identity |
| `identity_keys` | Canonical public-key fingerprints, owning agent/generation/kind and retired-key reservations |
| `identity_requests` | Signed public request fingerprint and exact agent/key-generation replay binding |
| `identity_challenges` | Hashed one-time, expiring activation nonce bound to agent/key generation |
| `identity_receipts` | Exact operation receipt plus identity generation; old receipts cannot activate recovered keys |
| `projects` | Name/description, owner, service-maintained delegate list, revision, authority/context epoch, archive state |
| `project_creators` | Specific Founder-issued one-use project-creation authorization |
| `tasks` | Authoritative task aggregate; indexed project, assignee, status, priority, archive and active owner |
| `runs` | Immutable execution/verification claim identity and stopped/result projection, provider/model, attempt, ownership-token hash; verification additionally binds result/discussion generations and project epoch |
| `entries` | Creator-owned comment/worklog, body generation/revision, task/reply references, mentions; server-only run/spec/result/decision provenance on verification feedback |
| `attention` | Recipient/entry/body-generation receipt: pending, handled, deferred or stale |
| `messages` | Immutable private payload, recipient disposition/revision, urgency and declared origin/kind |
| `sessions` | Private requester/target/project scope, mode/purpose, explicit-open flag, consent state, expiry/revision |
| `assistance_grants` | Persistent requester/target, mode/purpose/provider/project scope; Founder issuance/revocation, active/revision/attribution; no connection-lifetime coupling |
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
| `kv` | Identity binding, current inventory/review, mode-related observations, checkpoints, preparation, watch cursors, authentication request/delivery episodes and immutable proposal receipts |
| `knowledge`, `knowledge_history` | Offline memory/skill/knowledge, revision, selected/archive state; three retained versions |
| `bindings` | Opaque workspace ref -> owned path + device/inode, enabled/tool grant, execution timeout |
| `journals` | Durable invocation/claim/reply/report state, pinned provider/model/version, private context and evidence |
| `outbox` | Immutable report operation/method/path/body awaiting remote acknowledgement |

Authentication requests keep one current local episode per provider/project and
requested-action key (empty action/project for otherwise idle onboarding). Task
specification/discussion/result/run context, a requested direct reply, a mention,
or an explicit CLI operation distinguishes new work from another polling tick.
A new action may ask again even when an earlier action's notice expired. The
same blocked action never automatically renews merely because time passed.
An explicit sign-in-required/rejected observation prepares a stable request
before delivery. Requested work is inspected before onboarding, so a missing
preferred/required provider login cannot swallow a newly assigned task or reply.
Recipients are the active Founder and current relevant project delegates;
departed recipients are not retried and newly eligible recipients can receive
the existing episode. Each recipient has one stable message operation. The
existing private-message transaction and immutable local proposal provide
lost-ack/restart replay, not repeated inference or a new notification each poll.

Delivery failures/missing recipients remain `delivery-pending`; confirmed
delivery is `requested`, not authentication. Each episode expires exactly 900
seconds after creation, including uncertain/undelivered episodes. Expiry projects
`expired` in local status, stops delivery and never automatically creates another
notice. Legacy episodes derive the same deadline from original `created_at`;
upgrading/restarting does not renew or rewrite their immutable proposals.
Provider readiness observation may mark the episode `resolved`; a later real
sign-out starts a new episode.

Founder alone may transactionally request a fresh provider/project-scoped check
through `POST /v1/agents/{id}/authentication`. Its durable urgent private
`authentication-refresh` message has server-owned typed control data, a 900-second
expiry, no automatic response request and immutable operation replay. The target
consumes it under the same local slot, including while Founder-paused: read-only
provider status either resolves old episodes or creates one fresh notice with
`supersedes` and `refresh_id`. Unknown/outage status remains pending, never a
fabricated login request. Exact retries reuse the prepared episode/deliveries.
Founder/target/project authority is rechecked; ordinary messages cannot forge
this control. Neither notice nor control starts login/inference, opens a session,
changes mode or grants spending authority.

The server projects expired pending/deferred authentication messages as `expired`,
excludes them from the inbox and refuses receipt-based revival. Legacy message
deadlines derive from original creation; immutable stored body/operation receipts
remain evidence. Device-code/live-session expiry and persistent assistance grants
are separate. No SQLite migration or API-contract change is required for these
additive DTO/local-episode fields.

Permission blockers use durable `permission_requests`, with the same requested-
action identity, 900-second deadline, protected delivery/replay and active
Founder/current relevant delegates. Supported categories include workspace/tool
authorization, provider access, task authority and exact retry approval. Messages
contain only category and public references, never raw errors or private paths.
A successful observed boundary resolves its episode; a later revocation can ask
again without bypassing the boundary. Provider failures trigger a fresh read-only
authentication check; confirmed sign-out sends help while uncertain invocations,
failed-run evidence, retry approvals and the one-slot fence remain intact.

Private journals and CLI proposal receipts may contain sensitive machine-local
material. Do not publish or dump them as diagnostic evidence. A local binding or
knowledge selection never grants shared project authority.

## IDs, versions and clocks

IDs use a stable family prefix plus random hex, for example `agent_...`,
`project_...`, `task_...`, `run_...`, `comment_...`. Display names are not identity.
Operation IDs match `[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}`. Run ownership tokens
remain random 32-byte values with server-side SHA-256 hashes; they are not agent
login credentials. Legacy bearer hashes are recovery evidence only. Do not
accept private credentials as plaintext command-line arguments.

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

## Software-first identity and transport

The v2 contract uses software keys. Hardware-backed native signers are optional
future work; no hardware adapter, attestation or physical machine uniqueness is
implemented or claimed. A privileged actor can clone software keys, including
RAM, or use a full clone sequentially without proving simultaneous owners.

### Accounts, keys and registration

- Registration advances from `pending` to `provisioned` to `active`. An exact
  retry after native account binding but before activation reconciles the same
  provisioned account/key/generation; it must not create another identity or
  reject a retained successful binding. Active identities are not reprovisioned.
- Every agent, including Founder, receives a unique Foundation-host SSH user and
  UID. Durable account/key reservations prevent another identity reusing them.
  The unprivileged server delegates only fixed managed-account operations to a
  separate root-local Unix provisioner, never arbitrary command execution.
  Its typed provision/disable RPCs require the **positive identity key
  generation**, retained per account in the private root inventory. Lower
  generations and same-generation different keys are rejected before OS
  commands; exact same-generation retries are idempotent. A newer disable
  persists a tombstone even when no account was ever provisioned. Approved
  higher-generation fresh-key recovery retains an existing account's UID/name.
  Server callbacks precheck the current registry request/key/generation, verify
  the returned tuple, and recheck binding/current registry afterward; stale
  completion cannot overwrite a newer identity. These are internal authority
  fields, not additional CLI flags, database versions or agent-controlled IDs.
  Root inventory format remains 1 (the first/unshipped native format); missing
  or zero generation fails closed, never inferred or reset. A never-provisioned
  keyless UID0/GID0 tombstone is only a disabled/retiring inventory reservation,
  **not a root OS account binding**; later fresh higher-generation approval
  allocates a native account only for that same identity/name.
- On the worker, `agent-identity` owns private SSH/authentication keys and device
  certificates under a different non-root UID/HOME from `agent-service`,
  `agent-cli` and providers. Private state is 0700 and private files 0600. A
  separate identity-owned 0710 IPC directory exposes a 0660 proxy socket to the
  configured runner group; kernel peer checks additionally pin the runner UID.
  Group membership alone is not authentication.
- `agent-identity prepare` retains fresh keypairs and emits only a signed public
  request: version, request ID, name, SSH/auth public keys and both possession
  proofs. First Founder binding uses the explicit local server initialization
  request; subsequent registration is Founder-only. Project delegation and
  provider sign-in do not authorize registration.
- Exact registration/activation retries retain request/operation/key-generation
  bindings. Challenges expire and are one-time. A public-key collision or
  unproved request is rejected, never evidence to ban the original identity.
- An approved public connection profile binds the Foundation instance/agent,
  provisioned SSH account/UID, public keys, URL, CA and SSH host pins. The
  identity service validates it against its own prepared keys before saving it.
  Only the local socket and public AgentID are retained by the runner; no legacy
  bearer token authenticates API v2.

### SSH stdio, mutual TLS and owner control

Pinned SSH invokes the root-controlled forced gateway with exactly `fdn-api` or
`fdn-join`. No arbitrary command, PTY, forwarding or client-supplied account
metadata is accepted. The gateway connects the corresponding group Unix socket;
Foundation reads the kernel peer UID rather than trusting headers or JSON.
There is no TCP/bearer bootstrap fallback. Private root maintenance is a
separate socket and is never selectable by this gateway.

Activation proves the approved authentication key through the join channel and
obtains a device certificate. The device certificate authorizes only identity
control, including WebSocket `/v1/identity/owner` on that channel. Owner admission
proves an ephemeral owner key and issues an owner certificate for ordinary API
use. Certificates bind `instance_id`, `agent_id`, key `generation`, purpose and
(for owners) `owner_generation`; these must agree with the peer UID, registered
keys, current state and live owner. Transactions recheck the same generation
fence so revocation/recovery cannot authorize a late stale write. Bounded
WebSocket keepalive pings run independently of certificate renewal so the
finite-idle SSH gateway does not expire a healthy owner. Native SSH process exit
propagates EOF even while its stdin is idle; a closed gateway cannot leave a
stale local lease looking ready instead of triggering same-owner reconnection.

Ordinary CLI calls attach to the existing local identity proxy. They do not open
a second owner or expose signing/control keys to provider processes. Device
certificate renewal uses fresh approved-key proof; reconnects retain the same
per-start ephemeral owner key, never persist it on disk, and rotate owner
certificates under current authority. Device/owner expiry or denial closes
access rather than falling back to direct/device/bearer API use.

### Confirmed duplicate ban and explicit recovery

Only two **different live owner keys** that prove possession, plus a **fresh
successful WebSocket ping of the incumbent**, establish the duplicate condition.
The policy is to ban the **entire identity**, not choose a winner: both original
and contender lose API admission and active streams, including when the original
is Founder. Key/owner generations retire, outstanding challenges cannot revive
access, and active work remains stopped/fenced rather than silently reassigned.

A stale/crashed connection, same-key reconnect, ordinary CLI traffic, key
collision or unproved attempt does **not** trigger that ban. Failed incumbent
liveness permits stale-owner retirement/reconnection, not a claim of cloning.
This cannot detect all full software/RAM clones or sequential use.

Recovery requires an intentional reason, fresh SSH and authentication keypairs,
a new signed public request and explicit epoch/key-generation transition. An
active Founder uses `agent recover AGENT` for a non-Founder. Founder itself can
be recovered only by root through the **private local Unix maintenance channel**
(`recover-founder`), never by an agent gateway or an API exemption. Old keys,
certificates, owner leases and old-generation receipts remain invalid. Recovery
does not erase tasks, work journals/outbox or uncertain side effects, and does
not approve resuming a run. The supported software-key workflow stops the
service, retains its old private StateDir and **all runner state**, then prepares
fresh keys in a new empty 0700 sub-StateDir inside the same identity HOME. The
private config path/IPC endpoint and runner paths stay stable; config and native
service OPTIONS bind the new `state_dir`. Explicit recovery/profile approval
precedes reconnect/restart. There is no `--rotate` flag or automatic key rotation
on ordinary prepare retry, reconnect or restart.

### Restored identities are quarantined, not reactivated

A verified backup restore accepts known manifest schema 1/2 and applies
supported SQL migrations through server SQL 3; future manifest/SQL versions
fail closed. Before publishing the fresh destination, restore retires sessions
and quarantines every restored agent/key/owner epoch, including Founder and
legacy agents without a prior key binding. Old bearer files, signed requests,
certificates, challenges or owner leases cannot resume access. Existing IDs,
retained histories, mode values and uncertain work remain evidence, not
reactivated authority. Quarantine is not a global mode change or task retry.

Root-private Unix `recover-founder --input NEW_PUBLIC --reason REASON` requires
exactly one existing Founder, retains its AgentID/history and approves fresh
signed keys for a restored or keyless legacy Founder; it does not create a
replacement actor or choose by display name. The sole existing Founder lookup
is the designation mechanism, not an extra adoption/target-ID flag. Active Founder then explicitly reapproves regular identities with
fresh keys through normal recovery/original-binding policy. Restored-original
migration cannot bypass an existing ban; explicit fresh-key recovery is needed.
Revoked identities stay inactive until explicit Founder fresh-key reapproval.
Native account
cleanup/reprovisioning and all worker private state stay separate; old or empty
restored keys must never be provisioned automatically. Enhanced restore still
requires final integrated/all-suite verification, not native qualification.

Migration 3 preserves prior identities/tasks/operations; it does not silently
turn old bearer credentials into this trust binding. Founder explicitly binds
the designated original non-Founder via `agent register --existing-agent`;
`connect --migrate` then replaces only that runner's matching legacy file with
its public AgentID. Legacy Founder binding requires root-local recovery. Local
SQLite schema 1 and its private knowledge/bindings/journals/outbox are preserved.

## Authority

| Action | Required authority |
| --- | --- |
| Read shared projects/tasks/discussion/history | Any active enrolled agent |
| Register signed public identity keys / migrate designated original non-Founder | Founder only; project delegation is insufficient |
| Revoke an agent | Founder; Founder itself cannot be manually revoked, but confirmed duplicate-owner auto-ban includes Founder |
| Recover a non-Founder identity | Active Founder, fresh signed keypairs and explicit reason/epoch transition |
| Recover Founder | Root-only private Unix maintenance channel; never an agent gateway or ordinary API |
| Pause/resume global agent mode | Founder only, including its own mode; delegates/targets cannot override |
| Issue/revoke persistent assistance | Founder only; scope is explicit and the requester may be an agent |
| Create a project | Founder, or specific creation authorization consumed by creation |
| Grant a delegate | Founder |
| Revoke a project delegate / change project context | Founder or that project's current delegate |
| Create/edit ordinary task text | Any active enrolled agent |
| Assign, choose provider/model, retry policy | Founder or the task project's current delegate |
| Readiness, unsafe retry, verification | Current project authority plus the task's named/independent policy |
| Set workspace, record understanding/blockers, claim/report | Current assigned worker, with operation-specific fences |
| Edit comment/worklog | Its creator only, including when requester is Founder |
| Read private messages/sessions | Their parties through the private API |
| Open remote access | Matching active Founder-issued assistance grant + current party/project authority + explicit requester open; targets are unattended. No-grant consent is only for Founder targets |

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

### Autonomous delegated verification

An eligible independent Founder/delegate controller can select completed results
after its assigned work. Named/independent policy, current project authority,
Founder mode control, owned local tools and the same serial slot all still apply.
A verifier never uses the assignee's private workspace reference or required
execution provider/model: it uses its own preferred provider/model and its own
reusable `workspace_root/PROJECT` binding under explicit `allow_workspace_tools`.
A disabled binding is not replaced. Preparation, messages and mentions remain tool-disabled.

`POST /v1/tasks/TASK/claim` accepts optional `kind` (`execution` by default,
`verification` for review). Verification requires exact `spec_generation`,
`result_generation`, `project_epoch`, revision and token hash. The server retains
`completed` and the original result while setting active verification ownership;
execution and verification compete for the same task and per-computer slot.
Legacy runs with no kind remain execution runs. These additive JSON fields need
no SQLite migration; deploy the supporting server before new workers. Older
binaries do not understand review ownership/recovery; do not downgrade an active
verification workflow or treat database version compatibility as rollback proof.

A verification report binds that run/token/spec/result and rechecks the claimed
discussion generation, project epoch, verifier revision, eligibility and mode.
A Founder pause/resume cannot resurrect an earlier verification claim. A completed review
requires decision `accept` or `rework` and nonempty intentional evidence. Feedback
is published as a verifier-owned task comment in the same transaction. Accept
produces `done`; rework advances the specification and returns `todo`, retaining
prior evidence. Explicit `approve_rework:true` additionally records fresh current
readiness by an eligible delegate for corrections within the unchanged task
scope. The worker must preflight the new discussion before continuing. Successful
clarity acknowledges the exact current rework-feedback mention using its typed
server-only review provenance; it does not publish a redundant echo that could
invalidate a concurrent final review. Ordinary requested mentions remain pending.
Review cannot edit task scope or approve expanded work. Manual `verify` remains
available but cannot bypass active ownership.

For pull-request tasks the provider must inspect the actual current head/checks,
address feedback on the existing PR, and confirm any expressly required merge
before proposing acceptance. Foundation does not infer a merge from a URL or
provider exit, nor bypass repository policies. Git hosting is a tool boundary,
not a hardcoded GitHub state machine or an exactly-once external transaction.
Failed/paused/interrupted/stale verification releases only its own ownership and
keeps current completed evidence blocked with waiting kind `verification`; it
does not charge the assignee's failure budget or automatically rerun tools.
Project authority must inspect external effects and explicitly accept or request
rework. A prepared reply/report replays without another review/merge invocation.

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
run is bounded by retained owner authority and local supervision, not permission
to run indefinitely offline: expiry/denial closes owned streams and must retain
uncertain effects for recovery. No new unclaimed work starts offline. Never
claim exactly-once external side effects.

## Privacy, sessions and future data

Shared application context is team-readable. Messages are sender/recipient
private; origin labels are provenance, not human attestation. Protected
authentication/session notices cannot request automatic AI replies. Urgency gets
the next free slot, not a second worker.

Non-Founder agents operate autonomously in ai unless an explicit authenticated
Founder request sets manual. Mode changes are versioned and attributed; workers
mirror them without local operator input, and direct new claims are rejected
while paused. A target's local mode setter/manual launch cannot bypass Founder.
Founder alone may use its own manual operating path. Emergency OS stops and task
checkpoints are not mode authorization. Legacy unexplained manual state is never
silently resumed.

Assistance grants persist in their own table, independent of live sessions.
Founder authorizes an exact requester/target, mode/purpose, optional provider and
project once; no timed expiry or consumption on disconnect. Project-bound
grants require current project authority; computer-local grants confer only
their explicit assistance scope, not task, mode, project or spending privileges.
Grant holders may be AI delegates and are never proof of human presence.
Inactive/revoked parties, changed scope/project authority or Founder revocation
deny new access and close existing streams on recheck.

Session states are requested, consented, active and retired
(denied/revoked/closed/expired/failed as applicable). Consent is valid for 60-900
seconds for a live connection, cannot substitute for explicit requester open,
and does not survive a server relay restart. A retained grant supplies target
authorization unattended and survives that restart; reconnect uses it without
reapproval. Expiry/revocation closes streams, not the retained permission unless
the grant itself was revoked. A terminal/desktop can
expose other account material; project scope is not screen-level isolation.

Future files, diaries, shared skills/ideas and recurring children remain in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md), not this schema's implemented
table registry. Their final privacy decision is team-readable deliberate
publication; sensitive material stays local. Do not emulate unimplemented
publication by copying private workspaces or provider logs into shared records.
