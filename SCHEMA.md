# Foundation data and authority contracts

Current API and application schema version: **2**. This document describes the
Go implementation contract, not a direct-database client protocol. Read
[COMMANDS.md](COMMANDS.md) for wire inputs and [DEVELOPMENT.md](DEVELOPMENT.md)
for migrations.
Native message protocol version is **2**; server SQLite migration version is
**8**; local-worker SQLite remains **1**. Existing SQL5/SQL6/SQL7 databases migrate
transactionally through SQL8; earlier server-state formats remain unsupported.
Deploy the server first. Version 2 requires account-bound public-key admission
and mutual TLS; legacy bearer credentials/invitations cannot authenticate or
create new runtime identities. Native multi-UID qualification is separate from
source-bound software tests and has not been performed.

## Persistence and transaction boundaries

Foundation owns a private SQLite database, normally
`/var/lib/fdn/foundation.sqlite`. Agents use the inner HTTPS API over pinned SSH
stdio/Unix ingress, carrying bounded protobuf frames rather than HTTP; they cannot submit
arbitrary SQL, raw documents, bulk writes or replication traffic. The Go SQLite
driver uses WAL, `synchronous(FULL)`, foreign keys, a 10-second busy timeout,
immediate write transactions, and at most eight connections. Writes serialize;
SQLite is not distributed multi-master storage.

Each runner has an independent private `local.sqlite` under `state_dir`.
The identity service and runner use distinct state directories but the same
existing installing user's UID/HOME. That user owns the keys, profiles, provider
credentials and runner database. Application databases, configurations, locks,
state directories and owners are checked. Private directories/files and IPC
use 0700/0600; the user's existing HOME permissions are not changed.
`PRAGMA user_version` is local to each database. Server SQL5 is the minimum
existing database version; older and future unsupported versions fail closed.
Fresh empty databases use the immutable ordered initialization statements.
SQL6 appends shared notes and scoped-update triggers. SQL7 preserves identity
JSON and existing records while rebuilding account/challenge foreign keys for
multiple Founder device bindings, adding device-scoped request/key/receipt
metadata and independent Founder owner records. Shipped SQL1-SQL6 are unchanged.
SQL8 adds only history/observer indexes; shipped SQL1-SQL7 and private message
bytes, insertion order, receipts, identity/work records and history retention
remain unchanged. Older binaries cannot open the SQL8 database.

New user defaults are grouped under `$HOME/.foundation`: private `config`,
runner `state`, key `identity`, socket `run`, owned `workspaces`, managed `bin`
and `guidance`. Explicit configuration/native service paths are not rewritten.
Changing this filesystem layout needs no API, native message or SQL version
change and never re-enrolls an identity. Other layouts are not automatically
discovered; explicit current-format configuration paths remain valid.
External tool directories/environment settings and platform discovery/service
locations retain their own contracts.

Founder `agent add` is a client-side, current-user SSH installation saga, not
a new authority or schema. It uses existing transactional signed registration.
Its private local journal binds the original Founder/operation, exact target,
server instance/release, signed public request/profile and acknowledged native
stages. Unknown native effects retain a fence; receipt replay never installs a
different source or rotates keys. `agent add-status` reads only that local
evidence. `assistance status` reads the exact session retained by an assistance
open operation, with ordinary current party authorization; it never creates,
opens or retries a session. Neither command changes SQL/API/native versions.

Screens require explicit current profiles with verified geometry and a matching
local backend. Per-worker `vnc_address`/`vnc_verified` are unsupported; only their
empty/false serialized slots are accepted when reading current baseline
configurations, and new writes omit them. No display environment implicitly
creates a whole-desktop profile or screen authorization.

### Server tables

| Table | Contract |
| --- | --- |
| `agents` | Immutable identity, display name, Founder/active/banned state, sponsor, timestamps, mode/revision and Founder mode-request attribution; the historical credential-hash column is unused by authentication |
| `invitations` | Historical SQL storage retained by immutable schema definitions; no input type, runtime operation or receipt adapter exists |
| `agent_identities` | Original agent binding plus Founder-only additional `device_id` bindings, each with independent request, SSH user/UID, approved keys, generations and state; generated `agent_id` retains the owning actor foreign key |
| `identity_accounts` | Durable one-enrollment/one-UID/one-SSH-user reservation; historical `agent_id` column identifies the original agent binding or additional device binding, never another actor |
| `identity_keys` | Globally unique canonical public-key fingerprints, owning agent/device/generation/kind and retired-key reservations |
| `identity_requests` | Signed public request fingerprint and exact agent/device/key-generation replay binding |
| `identity_challenges` | Hashed one-time expiring activation nonce per enrollment binding/key generation |
| `identity_receipts` | Exact operation receipt plus device/key generation; old receipts cannot activate recovered or revoked keys |
| `identity_owners` | Independent Founder owner keys/generations per computer; live transport admission is additionally required; non-Founders retain the single-owner fence |
| `projects` | Name/description, owner, service-maintained delegate list, revision, authority/context epoch, archive state |
| `project_creators` | Specific Founder-issued one-use project-creation authorization |
| `notes` | One revisioned team-readable Markdown document per company/project/agent scope, explicit author/time provenance; latest three snapshots in `histories` |
| `tasks` | Authoritative task aggregate; indexed project, assignee, status, priority, archive and active owner |
| `runs` | Immutable execution/verification claim identity and stopped/result projection, provider/model, attempt, ownership-token hash; verification additionally binds result/discussion generations and project epoch |
| `entries` | Creator-owned comment/worklog, body generation/revision, task/reply references, mentions; server-only run/spec/result/decision provenance on verification feedback |
| `attention` | Recipient/entry/body-generation receipt: pending, handled, deferred or stale |
| `messages` | Immutable private payload, explicit recipient `read_at`, independent disposition/revision, urgency and declared origin/kind; protected server-only `agent-operation` controls/results are excluded from ordinary inbox/read/disposition mutation |
| `sessions` | Private requester/target/project scope, mode/purpose, explicit-open flag, consent state, expiry/revision |
| `assistance_grants` | Persistent requester/target, mode/purpose/provider/project scope; Founder issuance/revocation, active/revision/attribution; no connection-lifetime coupling |
| `histories` | Latest three snapshots per mutable source, global sequence, authenticated actor, action, reason, time |
| `events` | Shared change sequence and topic/source/task/project references; no private message payloads |
| `update_feed` | Durable bodyless change sequence for scoped push; meaningful mutations only |
| `update_recipients` | Actor/key-generation/sequence recipient index; private references stay party-scoped |
| `update_state` | Singleton persistent feed ID, cursor-signing key and retained sequence floor |
| `update_changes` | Transaction-local changed-source accumulation, drained before commit |
| `operations` | Actor + operation ID, exact request fingerprint, bounded result reference/receipt |
| `audit` | Durable accepted/denied operation outcomes, actor, action and time |
| `settings` | Reserved service-owned settings, including bounded per-agent latest worker-health report/incident and concise public status/pending publication; no client authority projection |

The authoritative definitions are in `internal/foundation/store.go` and the DTOs
in `internal/domain/`. Source records, runs, events and audit receipts are not
automatically bounded by the three-snapshot rule; see [CAPACITY.md](CAPACITY.md).

### Shared scoped notes and task context

`Note` contains `id`, `scope`, optional `target_id`, Markdown `content`,
`revision`, and creation/update actor IDs and timestamps. Company uses
`note_company` with no target; project/agent documents use `note_` plus the
stable target ID. An unset document is explicit empty content at revision 0.
Each body is bounded to 65,536 UTF-8 bytes without NUL. `NoteRequest` requires
content (empty explicitly clears), an intentional reason and exact optimistic
revision (0 on create); unknown/protected input fields are rejected.

Founder alone sets company notes. Founder or a current delegate sets an active
project's notes; Founder can also maintain archived-project notes. Founder/self
can set an active agent's notes. Another editor must be a current delegate of
an active project with an unarchived nonterminal task assigned to that agent.
Delegates cannot edit Founder's agent notes. The write transaction rechecks
identity, current assignments/delegation, revision and target existence; stale,
revoked or unrelated authority cannot create or change a document. Scope is not
read privacy: shared notes and their three retained histories are team-readable.

Writes, bounded source-reference receipts, history, bodyless events and scoped
push invalidations commit together. Company invalidations reach active agents;
project invalidations reach project parties/dependents; agent invalidations
reach the target and Founder. No note bytes enter those invalidation records.
Notes changes do not mutate agent/project/task revisions, project epochs,
approvals, active claims, mode or retry state.

The controller fetches one atomic company/current-project/own-agent snapshot
before every newly prepared provider invocation, including task preparation,
execution and independent verification. Taskless calls omit project notes.
Verification uses the reviewer's own agent notes. All applicable scopes are
cumulative context, not authority or an operating-contract override; material
conflicts require clarification. Missing, malformed or unavailable context
blocks inference. Selected private knowledge remains separate and local.
The complete prompt remains bounded; there is no silent truncation.

An in-flight or retained invocation keeps its original snapshot in private
`Launch.shared_notes` journal context and prompt hash. Edits apply to the next
new invocation, without cancelling current work, replaying inference, resetting
text-response/retry fences or manufacturing approval. Markdown may contain
custom instructions, skills, missions, visions, goals and file/website/GitHub
references, but no automatic fetching, cloning, attachment storage, skill
installation or publication of private local knowledge is implied.

### Concise team-visible agent activity

`Agent.status` is a read projection of the current generation's bounded latest
publication, not an authoritative agent edit/revision. All active registered
viewers can see its concise non-secret text/source/state, server publication and
observation timestamps, and stale flag. General diagnostic and message privacy
remain unchanged. Self-only `POST /v1/agents/{id}/status` accepts command metadata
and required one-line `text` up to160 Unicode characters; there is no Founder
impersonation, source/priority/time/mode override or inferred authority.

Controller summaries come from allowlisted activity labels and state, never
raw/private provider/context content. Optional `WorkerHealth.activity` preserves
actual scope/acceptance/execution/verification/message intent; legacy empty
activity remains compatible with coarse summaries. Local health updates request
prompt existing heartbeat publication for meaningful critical changes, without
extra inference. Bookkeeping phase changes do not create status churn.

Server-owned per-agent status state retains the current generation, latest
meaningful automatic summary, optional activity-scoped authored note, latest
pending candidate, and publication budget. Normal publication is once per60
seconds, unchanged text is suppressed, and intervening changes coalesce last-wins.
Meaningful blocked/paused/recovery transitions may bypass that ordinary interval.
The existing bounded monitor flushes due publications; read endpoints do not
mutate or flush state. A note cannot outlive its semantic activity to conceal a
pause/block/completion. Identity recovery fences old status/note/pending data.

Status changes do not advance authoritative agent mode/revision, business progress
or task completion, and cannot grant work/read/session/spending authority. Missing
or stale observations are not healthy status. No SQL migration is required;
normal immutable operation/audit retention remains separate from bounded status
state. Authored status is explicit team publication and must contain no private
paths, credentials, message content/references or unpublished knowledge.

### Explicit private-message reading

`Message.read_at` is absent until the recipient deliberately submits a read
acknowledgement. `POST /v1/messages/{id}/read` accepts command metadata only and
checks the current authenticated recipient transactionally. The first accepted
server UTC Unix timestamp is immutable across new read operations or exact
operation replay. Sender/recipient reads and message diagnostics expose it; a
sender, unrelated agent or declared service origin cannot set it for someone else.

No GET, inbox polling, model context selection, response or handled/deferred/stale
disposition automatically sets this field. Optional model `message_read:true` is
an explicit direct-message choice, not a processing side effect; omission/false
leaves it unset. Read acknowledgement is independent of message state/revision,
so concurrent disposition receipts commute without losing either observation.
It never means task approval, comprehension, verified response or session consent.
Expired notices retain their expiry/state. Protected agent operations are not
ordinary messages for this endpoint. Existing JSON rows decode as no explicit
receipt; no SQL migration or historical backfill occurs.

### Worker tables

| Table | Local-only contract |
| --- | --- |
| `kv` | Identity binding, current inventory/review, mode-related observations, checkpoints, preparation, watch cursors, authentication request/delivery episodes and immutable proposal receipts |
| `knowledge`, `knowledge_history` | Offline memory/skill/knowledge, revision, selected/archive state; three retained versions |
| `bindings` | Opaque workspace ref -> owned path + device/inode, enabled/tool grant, execution timeout |
| `journals` | Durable invocation/claim/reply/report state, pinned provider/model/version, private context and evidence |
| `outbox` | Immutable report operation/method/path/body awaiting remote acknowledgement |

A fully returned tool-disabled reply that violates its phase outcome contract is
stored as `reply-rejected`; its original context, reply and prompt hash remain
private and unchanged. This terminal admission state is committed atomically
with a `kv` text-response fence keyed by phase and semantic requested inputs,
not a caller's new operation ID. It does not occupy the global pending-invocation
slot or silently authorize another model call. Unrelated inbox/attention items
can continue. An exact local `work recover --journal ... --acknowledge-stopped`
may atomically mark that original journal abandoned and its same-action fence
retry-authorized; it cannot clear a later rejection or grant execution retry
approval. Current specification/discussion/attention/project context distinguishes
legitimately new work. Unknown/partly applied/uncertain state is not discarded.
No SQL migration is needed: local SQL remains 1. Older binaries do not understand
the new pending-state exclusion and must fail closed rather than reset evidence.

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

### Autonomous interactive-session cleanup

The target session manager first retries cleanup of the exact native endpoint
it opened, without inference, another login, a new grant or a human recovery
request. Each attempt is bounded; pending cleanup follows the existing
one-second exponential backoff capped at 30 seconds. A failed `Close` is not
cached forever. Known no-process tmux preparation failures retire their session
fence automatically, while missing authentication remains a separate blocker.

New tmux attempts retain private version-1 ownership records under the worker's
`StateDir/sessions`: exact target/session/fence generation, unique private socket,
kernel process start identity, UID, process group and cleanup stages. These
records are local evidence, not domain DTOs or native-wire fields. A restart can
retry a closing or server-confirmed terminal attempt and replay already-proven
cleanup. Active attempts, changed generations/targets, replaced sockets and
reused PIDs are never adopted or killed. No process-name search or passive
absence assertion substitutes for ownership.

The shared `work.lock` excludes concurrent inference or interactive admission.
Native cleanup proof is committed against the exact fence fingerprint; fence
retirement and a `session-cleanup-result:` local receipt commit atomically.
Network or storage uncertainty retries the same cleanup/retirement, not
inference or the login. The existing worker safety watcher then reconsiders
already-authorized `ai` work. Mode, task approvals, budgets, retry limits,
journals/outbox and unrelated fences are unchanged; Founder pause stays paused.
The watchdog gives a newly observed session fence a 30-second server-timed
recovery grace before alert delivery. Prompt autonomous cleanup sends no human
alert; persistent unprovable cleanup still produces one deduplicated fallback.
These cleanup operations require no additional migration or wire change
(local SQL1, current server SQL7, API/schema2).

Legacy/unknown endpoints without durable ownership, uncertain launches that
never captured a process identity, unavailable authority and unprovable cleanup
remain explicit operator-required cases. Existing exact Founder repair and
local stopped-process recovery remain last resorts, not the normal closure path.
An explicit Founder runtime reset additionally inspects and cleans recognized
terminal legacy tmux endpoints using fresh kernel evidence as described below;
ordinary autonomous cleanup does not adopt them.

## Observed worker health and reply diagnostics

`POST /v1/heartbeat` accepts `HeartbeatRequest` with optional version-1
`WorkerHealth` and returns the same `Agent` as before. Workers piggyback reports
on the existing 30-second heartbeat; old command-only bodies remain accepted.
Latest report and incident are bounded per-agent service-owned `settings`, not
new tables or client-writable authority. Health needs no additional SQLite
migration; current API/schema stays **2**, server SQL **7**, local SQL **1**.

Reports carry their version/sequence/observation time, effective mode,
provider/version/model, configured `preferred_provider`, authentication readiness
and its own observation time, phase/action/start/deadline, separate loop-step/
progress clocks, pending report count, safe blocker and component problem codes
with first/last/resolved times. Effective task/control provider can differ from
the preferred reply provider; one provider's login does not make the other ready.
Sequence counts phase transitions. A fresh `observed_at` may advance with the
same sequence and changed component problems; it is not a progress claim.
Sequence 0 before the first controller step is valid unknown-phase diagnostic
data. Repeated idle steps never advance `last_progress_at`.

Safe provider versions match `^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,127}$` or are
empty; workers reduce unsafe observed versions to unknown/empty. Safe models
match `^[a-zA-Z0-9][a-zA-Z0-9._:/+-]{0,255}$` or are empty; workers clear unsafe
model labels. Report validation rejects labels outside these forms. These bounded
labels must never smuggle raw provider output or private paths.
An idle heartbeat/poll advances liveness, not task progress. A login command's
success is not an actual authentication observation. Local work/service status
includes a live `health` snapshot; server diagnostics describe the last received
report, its age and any uncertainty, not a fresh remote provider probe.

The service-owned background watchdog runs without AI/worker cooperation,
including for manual Founder. Default missing-heartbeat/report thresholds are
90 seconds; phase deadlines have 30 seconds of grace; an eligible unexplained
queued requested reply has 120 seconds. Manual pause, no requested response,
normal idle and a long-running action still inside its deadline are not stuck
failures. A recent heartbeat does not make a stale report current. Unknown,
missing or stale observations remain explicit with a safe reason and next
inspection action. Foundation cannot guarantee detection of its own outage;
native/outside monitoring remains necessary.

`GET /v1/agents/{id}/diagnostics` returns `AgentDiagnostics` to Founder or self
only. A delegate does not acquire general agent diagnosis.
`GET /v1/messages/{id}/diagnostics` returns `MessageDiagnostics` strictly to that
message's sender/recipient, with no Founder bypass. Its `state` is observed
lifecycle: `not-requested`, `queued` (including shared-slot waiting), `generating`,
`blocked`, `awaiting-delivery`, `handled` and retained receipt dispositions.
`stage` is the lower-level pipeline phase: `receipt`, `recipient`, `reporting`,
`scheduling` or the active reply's safe phase. For example, `state:"generating"`
with `stage:"invoking"` can be visible while the provider invocation is still
pending; `state:"blocked"` can have `stage:"recipient"`. These observations are
not authoritative receipt or AI completion evidence. A handled receipt alone
does not prove an AI reply. General diagnostics must not reveal unrelated private
message references, private journals, raw errors, paths or credentials.

Diagnostic GETs do not sweep incidents or write server state; monitoring is the
background watchdog's responsibility. Health alerts are protected server-created
ordinary private messages with
`response_requested:false`; normal message send cannot forge them. They route
to active Founder and current task-project delegates only where scoped, dedupe
by incident/cause/action, persist across polling/restart and notify observed
recovery. Incident visibility is independent of 15-minute authentication notices
and never renews them. Diagnostics/alerts do not start login/session/inference,
retry, resume or recovery. Founder pause, open/uncertain session, uncertain
invocation, a different selected/required provider and permission/report blockers
remain visible after login; authentication resolves only its own observation.

## Founder diagnostic refresh and exact session recovery

`AgentOperation` version **1** is a durable non-inference control for
`diagnostics`, `session-recovery` or `runtime-reset`. Founder alone creates it through
`POST /v1/agents/{id}/diagnostics` (`DiagnosticRefreshRequest`) or
`POST /v1/agents/{id}/repairs` (`SessionRecoveryRequest`), or
`POST /v1/agents/{id}/reset` (`AgentResetRequest`). Reset uses an alias of the
existing reason-only envelope, preserving the locked native payload fingerprint.
Creation command
metadata requires a stable `operation_id` and no optimistic revision. The
server binds immutable requester/target key generations, reason and creation
plus exactly 900 seconds of lifespan; a repair also binds exact `session_id` and
canonical lowercase SHA256 `expected_fence`. It cannot extend authentication
notices, provider credentials, session consent, task approvals or grants.

`created_at` and immutable `expires_at` are server-clock values. Every projected
operation includes dynamic `server_observed_at` in the same clock; this value is
excluded from the immutable operation fingerprint and never renews the control.
Results retain `captured_at`, a conservative server-time capture bound derived
from a fresh authenticated server response plus monotonic request-start/round-trip
timing, independently of target UTC. On-time outcomes use the server receipt's
`finished_at`; late results require prior `checking` and `captured_at` at or after
`started_at` and strictly before `expires_at`. Target `AgentInspection.observed_at`,
provider `observed_at` and health timestamps retain their own UTC observations,
including skew. They are not compared with the server deadline for authority;
never subtract target UTC from it or normalize evidence to make a result current.
Immutable report/result replay retains the original capture bound and automation
eligibility; the read projection may suppress `automation_may_resume` for current
manual/expired state without rewriting that retained report.

Protected server-only `agent-operation` message data has
`response_requested:false`; ordinary message creation cannot forge it, ordinary
inbox consumption cannot turn it into a prompt, and receipt writes cannot settle
it. The requester Founder and exact target may read only their own operation via
`GET /v1/agent-operations/{id}`. This is not a Founder bypass for unrelated private
messages/sessions. The target's dedicated list
`GET /v1/agent-operations?limit=N&cursor=CURSOR` returns only its nonexpired
queued/checking controls; only that target may report safe typed results to
`POST /v1/agent-operations/{id}/result`. Current identities/generations and
operation expiry are rechecked. Exact submission/report retries retain their
original request fingerprints and receipts.

States are `queued`, `checking`, `refreshed`, `recovered`, `reset`, `rejected`,
`operator-required` and `expired`. A queued offline/dead daemon is not fresh
success; the server cannot restart it through this API. Results carry only safe
`code`, server-anchored `captured_at`, `mode_at_check`, `automation_may_resume`
and `AgentInspection` observation:
health, separately timestamped provider observation, exact session-fence
observation and slot state. They never publish raw stdout/errors, process
arguments, credentials, transcripts or workspace paths. A provider value of
`credentials-present-unverified` does not prove vendor usability; `ready` remains
an observation, not an inference or completed work. Health/phase and provider
observations keep independent timestamps rather than overwriting an in-flight
work phase.

Runtime reset explicitly permits cancellation of the target daemon's owned work
and interactive session, including Founder self-reset, without changing mode.
It gates new in-process admission, joins owned controller/session contexts,
retries exact owned cleanup and checks the shared `work.lock`. The 30-second
cancellation/join wait is also bounded by remaining server-clock authority;
native proof retirement retains its existing bounded finalization procedure.
A free runtime can report `reset`/`runtime-reset`; a proven terminal fence is
retired atomically with that receipt. The session supervisor and work scheduling
are reinitialized without restarting native services or the identity owner.
Uncertain journals, prepared work/results, pending outbox reports, maintenance,
another process's occupied slot and genuinely unprovable ownership are retained as
`operator-required`, not reset success. No identity/provider/knowledge/workspace/
task data, approvals, retry budget or mode is erased or renewed. The private
`agent-reset-start:` receipt prevents an interrupted reset from cancelling newer
work on replay; such a missing final result becomes `reset-interrupted`.
These reset operations require no additional migration or wire change
(local SQL1, current server SQL7, API/schema2, native protocol2).

For terminal legacy tmux sessions, explicit reset does native cleanup rather
than rejecting a missing historical ownership journal. The old-format per-session
socket must be owner-only, real and inode-stable. Refused/absent connections also
require independent kernel Unix endpoint absence. Live cleanup binds the actual
Unix peer PID/UID, kernel process start/group, dedicated Foundation tmux session
and pane-parent relationship; attached clients must exit after server shutdown,
not be signalled from self-reported PIDs. A private `*.legacy-reset.json` record
binds target/session/generation/raw-fence fingerprint, socket identity and native
process identities. Native stopped proof and the retired socket inode remain
private under `StateDir/sessions`; no legacy bytes are fabricated as pre-existing
ownership. Exact local proof precedes the atomic reset receipt/fence retirement.
Replaced resources, another native generation or a missing current-format record
cannot fall back to legacy adoption. This is separate from unchanged autonomous
recovery and exact `agent repair` requirements.

The independent worker control loop handles controls while paused, fenced or
busy, without provider inference or ordinary model/slot admission. Refresh is
bounded read-only collection, not sign-in/help renewal. Repair mutations require
the shared `work.lock`, exact server-confirmed terminal session, exact current
raw-fence fingerprint and existing durable native `Fence.CleanupProven` proof.
New fence instances have a generation nonce; retained old fence bytes/evidence
are not rewritten to make them eligible. Target mutation and final local receipt
commit atomically. An uncertain network response replays the stored result,
never a second clearing of a newer fence.

Initial recovery refuses old/unproven cleanup as `operator-required`: a caller's
`acknowledge_stopped`/proof assertion and absence/name/PID scans are not evidence.
Changed fences, stale generations, active sessions and occupied slots fail
closed. No arbitrary process stop, journal/inference retry, identity recovery,
native restart, evidence erasure, session open, mode change or authority grant
is implied. Retained legacy Agent1 state may still need explicit authorized
native inspection and exact local stopped-process recovery. Existing approved
`ai` work may resume only when independent fences permit; Founder pause remains
paused. `recovered`/automation eligibility is not actual work progress: inspect
the common operation, then read agent diagnostics again.

## IDs, versions and clocks

IDs use a stable family prefix plus random hex, for example `agent_...`,
`project_...`, `task_...`, `run_...`, `comment_...`. Display names are not identity.
New dedicated SSH usernames derive from agent names, but account names and
mentions resolve to immutable AgentIDs; neither a name nor a matching username
authenticates an actor or grants authority.
Operation IDs match `[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}`. Run ownership tokens
remain random 32-byte values with server-side SHA-256 hashes; they are not agent
login credentials. Historical reserved database columns do not provide a
credential API. Do not
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

### Literal agent-name and ID mentions

Comment/worklog bodies accept `@{Agent1}` or `@{AGENT_ID}` with the actual name
or complete ID substituted. Braces also support names containing spaces.
Name lookup is case-insensitive against active registered agents and happens
inside the entry's write transaction. Unknown/inactive names fail as not-found;
ambiguous names fail as conflict with an instruction to use the exact AgentID.
Legacy duplicate display names are retained, not silently renamed or guessed.
Explicit ID mentions retain their previous behavior, including inactive or
unknown IDs that cannot receive active attention.

`Entry.body` remains exactly as authored; `Entry.mentions` stores sorted,
deduplicated AgentIDs, excluding the author even when mentioned by name.
Only comments create attention. Worklogs retain mention metadata without
notifications. Exact operation replay returns the original recipient IDs,
not a new name lookup; body edits resolve anew and stale the old generation's
receipts. Waiting/clarification and feedback policy use those canonical IDs.
Automatic replies use the mentioned recipient's preferred provider/model,
independent of the task assignee's execution requirements. The same choice
governs mention-scoped sign-in help before onboarding; task/project references
remain available for protected Founder/delegate routing. A successful
tool-disabled response is published as a reply to that comment before its exact
body generation is acknowledged as handled. Existing pause, recovery and serial
work-priority rules still apply.
Mentions never assign work or confer project, identity or OS authority.
No database migration or mention backfill is performed.

## Founder-authorized software maintenance

Software upgrade discovery and native mutation are separate from task execution,
ordinary messages, provider updates, identity recovery and financial authority.
`GET /v1/upgrades/context` is bodyless/queryless and Founder-only. It reports the
actual Foundation source/version, instance ID and registry. Public release
discovery uses the stable versioned `thtpark/fdn` GitHub release channel; optional
native inventory is read-only and requires separately configured operator SSH.
No missing/unknown observation becomes "up to date".

`POST /v1/upgrades/authorize` checks current active paused Founder/key generation
inside the authoritative transaction. The exact proposal binds a native nonce/
ID, target Foundation/agent identity, key generation, current version/commit,
native policy hash, platform, new strictly greater VERSION/commit, exact official
bundle URL/digest/size, public age backup recipient and reason. A Foundation
target names this service instance and requires every active agent paused.
Active execution/verification ownership or a live assistance session blocks
authorization. No display name, message acknowledgement or OS UID creates it.

The server stores the immutable signed public permit under its service-owned
`settings` key `upgrade-permit:SHA256(signature)`; operation/audit receipts retain
only bounded references, not mutable snapshots or repeated signatures. Exact
operation replay returns the original 900-second permit without renewal.
Permit retention follows existing operation/audit evidence policy, not the
three-snapshot rule. Permit storage needs no SQL backfill. The request mailbox
uses server SQL5; local SQL1, API/schema2 and native protocol2 remain unchanged.
Payload fields are additive and old clients cannot infer upgrade support.

Immediately before stopping, the native updater requests a fresh unpredictable
nonce proof through `POST /v1/upgrades/lease`. Only the exact current issuer or
target can request it; Foundation's root-only private maintenance socket can
request only its own previously approved service target. The server rechecks
current Founder/target generations, authoritative pause, task ownership and
assistance. The authenticated requester's certificate/owner fence binds only
that requester; the other permit parties are independently checked as current
identity subjects, never impersonated using the requester's certificate.
Its domain-separated CA signature binds the permit fingerprint,
nonce and 30-second validity. The native requester also bounds the request using
monotonic elapsed time. This is a current-authority permission proof, not release
code signing/attestation, identity enrollment or an arbitrary root capability.

Native OS permission is separately preprovisioned in a protected updater policy,
operator SSH account and pinned CA. The one-shot `upgrade-helper` accepts only
bounded protobuf inspect/challenge/prepare/apply/status. Worker start proofs use
that worker's same-user identity proxy via the internal bounded-protobuf
`upgrade-lease` CLI, never another owner or copied credentials. The account
provisioner's root surface and ordinary forced SSH gateway are not expanded.

The helper's private durable challenge/job binds the exact policy and accepted
permit. It verifies release ZIP metadata and every contained checksum before
stopping; pending/uncertain local invocations/reports deny admission. Under the
existing application work/server lock it writes an age-encrypted cold snapshot
to separate updater state; only the public recipient reaches targets.
The Founder's private plan and off-host age key are owner-only.
Admitted stopped maintenance completes from its exact receipt and original fixed
permit expiry; it does not create ongoing authority or a new automation mode.

Plans queue private exact-permit maintenance request messages, not direct remote
mutation. Independently supervised target receivers accept/refuse and stop/back
up their own services; Foundation's completed result precedes dependent agent
requests. Installed source, actual services, current identity generation,
authoritative/local pause, pending work and retained session fence are checked.
Keys, provider auth, workspaces, knowledge, journals/outbox and unrelated HOME
instructions are not reset. No automatic task/inference retry, fence clearing,
provider sign-in, provider update, release publication, cloud action or resumption
is implied. Native receipts distinguish stopped/prepared/installing/verified;
interrupted mutating phases become operator-required, not success.
Read-only status can inspect retained original-Founder evidence over the separate
operator channel while Foundation is offline. Repeating apply or automatically
rolling back SQL after an uncertain install is forbidden.

### Agent-owned request conversations

Operational requests use `Message.request` with a bounded `action` (`work` or
exact-permit `upgrade`) and server-captured sender/recipient key generations,
project epoch and task specification/result generations. They begin `queued`.
Only the exact current target may report accepted/deferred/declined, executing,
completed or failed with a nonempty intentional reason and matching revision.
Each transaction persists the request state and a private `request-update`
reply in the same conversation. Delivery/read/acceptance is not completion or
task verification. Native requests do not receive model tools from their body.

`thread_id`, `reply_to_id` and `insistence` preserve private follow-up context.
Only the original requester may insist with a new independently authorized
request; the earlier decision remains final. Current sender authority, project
epoch, identity generations, OS policy, spending and independent work/session
fences still gate execution. Central authority/safety updates remain immediately
enforced. Protected auth/screen/tmux/diagnostic controls retain their contracts.
Project delegates may request scoped work but gain no native host-upgrade grant.
Foundation requests address the service instance and use its deterministic
root-private maintenance inbox, never a synthetic enrolled AI or shared identity.
SQL5 preserves message bytes, insertion order, immutable operation receipts and
the scoped update feed while allowing the exact Foundation maintenance mailbox
as a party. Database triggers retain registered-agent constraints for ordinary
messages and allow the instance only for upgrade requests/service result replies;
Foundation does not become an enrolled agent. Current identity/authority checks
remain transactional. Native payload additions remain locked/generated and
require compatible binaries.

Task acceptance rechecks the current assignment/specification and readiness in
the server transaction; executing requires an actual matching execution run.
Only a newer completed task result from its latest execution can finish a linked
request. Stopped/blocked execution produces a failed request reply, not completion
or an automatic task retry. A declined request does not cancel a separately
authorized task assignment.

Native inboxes expose failed/deferred/declined dependencies and expired requests
so recipients can reply with a reason rather than strand the rollout silently.
Before each decision/result send, the native receiver durably retains the exact
operation, revision, state and reason. Transport uncertainty is not refusal:
replay that proposal before other work, without repeating native effects.

Initial installation of upgrade-capable binaries and administrator policy is
explicit; an older deployment cannot upgrade itself into this permission.
Source fixture tests do not qualify native privileged installs, live vendor
readiness, host restore/reboot, Windows or a larger fleet.

## Software-first identity and transport

The v2 contract uses software keys. Hardware-backed native signers are optional
future work; no hardware adapter, attestation or physical machine uniqueness is
implemented or claimed. A privileged actor can clone software keys, including
RAM, or use a full clone sequentially without proving simultaneous owners.

### Accounts, keys and registration

The worker computer is the trust boundary, not a provider subprocess. Install
and run `agent-identity`, `agent-service`, `agent-cli` and providers as the
existing installing user. No worker OS account, group, hidden/no-login user or
separate HOME is created. The local username need not match the enrolled agent
name or Foundation SSH login: the approved signed profile, keys and mTLS
identify the agent. Name alone is not authentication.

The user can access its own private keys. Process separation and separate state
directories organize lifecycle, not isolation from that user's tools. Keep keys
out of model prompts, public output, ordinary IPC replies and shared records.
Compromising same-user tools can compromise the identity; administrator/root
control was never prevented by a second UID. Foundation-side account separation,
project authority, generation fences and confirmed duplicate-owner policy remain.
Linux/macOS use current-user IPC and user service managers. Windows still needs
native service/IPC adapters and qualification; this ownership contract does not
claim a Windows runtime port.

- Registration advances from `pending` to `provisioned` to `active`. An exact
  retry after native account binding but before activation reconciles the same
  provisioned account/key/generation; it must not create another identity or
  reject a retained successful binding. Active identities are not reprovisioned.
- Every enrolled computer receives a unique Foundation-host SSH user and UID.
  Non-Founder agents have one computer; Founder may enroll additional devices
  with distinct keys/accounts under its existing AgentID. Durable account/key
  reservations prevent another enrollment reusing them.
  For new identities the username is the lowercase agent name with runs outside
  ASCII `a-z`, `0-9`, `_` and `-` replaced by `-`, then outer `-` removed.
  The result must match `[a-z_][a-z0-9_-]{0,31}` and cannot be `root`; names
  must be single-line. Invalid results are rejected, never truncated or replaced
  by a hidden ID/hash fallback. For example, `Agent1` uses `agent1` and
  `Agent One` uses `agent-one`. Collisions fail; unmanaged accounts are never
  adopted. Existing ID-derived usernames and all reserved UID/name bindings stay
  unchanged across upgrades, reconciliation and fresh-key recovery.
  The worker uses its existing OS user, with an independent local UID and any
  local username. There is no required `agent1` or `fdn-worker` local account.
  Equal names on different
  hosts do not make the accounts interchangeable or grant a Foundation shell.
  The unprivileged server delegates only fixed managed-account operations to a
  separate root-local Unix provisioner, never arbitrary command execution.
  Its typed provision/disable RPCs carry the registry-approved `username` and
  require the **positive identity key generation**, retained per account in the
  private root inventory. Usernames are required; every request names the exact
  registry-reserved account without an ID-derived fallback.
  A changed name is rejected even with a newer generation. Lower
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
  certificates as the same existing non-root user running `agent-service`,
  `agent-cli` and providers. Private state/configuration is 0700/0600. A
  separate user-owned 0700 IPC directory exposes a 0600 proxy socket; kernel
  peer checks pin this user's UID/GID. Omitted/zero private configuration
  `worker_uid`/`worker_gid` defaults to the actual current user; explicit foreign
  values fail before native serving or key transfer.
  No special group membership is required. Service plans accept only the
  current single-user fields; keys, profiles, journals and account ownership are never moved,
  rotated or reset automatically.
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

### Founder devices and concurrent connections

Only authenticated Founder can use `device register --input SIGNED_PUBLIC
[--output PROFILE]`, `device list` and `device revoke BINDING_ID --input FILE`.
Additional computers prepare their own independent keys and signed public
requests; registration never copies keys or creates another Founder actor.
Their `AgentIdentity.device_id` and `device_name` identify the enrollment and
label. The original computer retains an omitted device ID and uses its AgentID
as the binding ID; existing profiles, keys and account names stay valid.
New device usernames derive from the signed device name using the same account
normalization and collision policy.

Activation challenges, possession proofs, signed certificate identities, local
profiles and exact receipts bind the device as well as actor/key generation.
Owner certificates additionally select an independent live owner generation
within that device. Multiple different Founder owner keys remain valid together;
renewing, closing or reconnecting one cannot replace another. Same-key reconnect
replaces only that logical owner transport. Non-Founder additional-device
registration is denied transactionally, including for project delegates.

Revocation requires a reason and another authenticated Founder computer; it
cannot remove the last active computer. It increments only the selected binding
generations, retains key/account tombstones, deletes its pending challenges and
owner records, and cancels that device's live owners/streams. Other device keys,
the Founder actor/mode, task journals and occupied work slot are unchanged.
Application access is denied even if native account cleanup remains pending.
Replaying registration cannot reactivate a revoked device; prepare fresh keys
and explicitly enroll a new device instead.

The original binding counter remains the actor-scoped control/discovery epoch.
Revoking the original computer can stale existing cursors/queued controls, but
does not invalidate other devices' credentials. Whole-Founder root-local
fresh-key recovery retires all additional devices; backup restore quarantines
every binding, not just the original. No old certificate or registration receipt
can restore authority.

Devices are access credentials, not additional agents, work slots or automation
modes. Claims still serialize by AgentID. Health, task/session controls and
maintenance remain agent-scoped, not a new per-device worker-management API.
Device enrollment alone implies no mobile/browser-client or native multi-UID
qualification.

### Foreground mobile host

The development `mobile` facade reuses CLI dispatch, private exact proposals,
identity activation/owner renewal, mTLS and native protobuf in one app package.
Its in-process SSH boundary accepts only pinned `fdn-api`/`fdn-join` sessions,
with public-key authentication and no shell, terminal, agent or forwarding.
Its private attachment admits only connections minted by that app-owned
capability and still passes the identity proxy's existing method/path/principal
checks. It is not a forged Unix peer or a configurable native-IPC downgrade.

The phone prepares independent software keys and accepts only its approved
Founder-device profile. Mode and work slots stay agent-wide; the limited host
does not claim work, start a controller/provider, consume native controls or
publish desktop worker health. State stays owner-only in the app container,
with platform data protection/backup exclusion and exact unresolved proposals.
On iOS only, an inode-pinned owner-only app-root capability checks identity
paths down to that sandbox boundary; Apple-managed ancestors may legitimately
be group-writable. Child symlinks, outside paths, wrong owners, writable private
ancestors and a replaced root are denied. Desktop/native commands cannot select
this storage boundary and retain their full ancestor checks.
No process-global HOME mutation, remote Mac CLI or public HTTP relay is added.
Foreground/background transitions close owned connections without mode changes
or automatic mutation/inference replay. Physical lifecycle/enrollment and
distribution qualification remain separate from compilation and fixtures.

### SSH stdio, OS identity and binary owner control

Pinned SSH invokes the root-controlled forced gateway with exactly `fdn-api` or
`fdn-join`. No arbitrary command, PTY, forwarding or client-supplied account
metadata is accepted. The gateway connects the corresponding group Unix socket;
Foundation reads the kernel peer UID rather than trusting headers or JSON.
There is no TCP/bearer bootstrap fallback. Private root maintenance is a
separate socket and is never selectable by this gateway.

Native connections negotiate `fdn-message/2` inside TLS and use the versioned
protobuf session in `internal/message/`. Commands/results, queries, cancellation
and independent ping/pong frames share a bounded full-duplex stream. There is no
HTTP/gRPC ingress or automatic HTTP fallback. JSON remains the CLI, offline
public-file and retained evidence format, not the native network encoding.

Activation proves the approved authentication key through the join channel and
obtains a device certificate. The device certificate authorizes only identity
control, including typed owner challenge/proof messages on that channel. Owner admission
proves an ephemeral owner key and issues an owner certificate for ordinary API
use. Certificates bind `instance_id`, `agent_id`, optional `device_id`, key `generation`, purpose and
(for owners) `owner_generation`; these must agree with the peer UID, registered
keys, current state and live owner. Transactions recheck the same generation
fence so revocation/recovery cannot authorize a late stale write. Bounded
binary keepalive pings run independently of certificate renewal so the
finite-idle SSH gateway does not expire a healthy owner. Native SSH process exit
propagates EOF even while its stdin is idle; a closed gateway cannot leave a
stale local lease looking ready instead of triggering same-owner reconnection.

Ordinary CLI calls attach to the existing local identity proxy. They do not open
a second owner or expose signing/control keys to provider processes. Device
certificate renewal uses fresh approved-key proof; reconnects retain the same
per-start ephemeral owner key, never persist it on disk, and rotate owner
certificates under current authority. Device/owner expiry or denial closes
access rather than falling back to direct/device/bearer API use. Account
identity and socket permissions do not replace transactional project, task,
private-party, mode and generation authorization. A UID alone cannot prove the
key generation of an already authenticated SSH transport; inner mTLS remains
mandatory until an OS-only replacement is independently proved.

### Durable scoped discovery

Desktop observation adds bounded union selectors to the existing public/private
feeds. Waiting is post-commit and entity-matched; unrelated changes do not wake
filtered observers. Filtered cursors cannot be reused under another selector.
An explicit filtered public request initializes at a current watermark rather
than replaying the old public event log; legacy unfiltered watch is unchanged.
There is no new owner, authority, controller, TCP listener or message-read side
effect. Read-only stdio observer sessions keep their own in-memory read state,
never a daemon controller checkpoint. A restarted observer resnapshots.
Local/health projections without a durable producer may be sampled only for
active interests; ordinary message/task/discussion reads remain event-driven.
Read-only entry lookup (`discussion show ENTRY`, native `GET /v1/entries/{id}`,
observer kind `entry`) retains the same active-actor shared-discussion access as
listing. It returns original parent/body/generation metadata without task,
attention, receipt or mode changes, including ancestors outside a loaded page.

SQL4 records recipient-scoped updates transactionally with meaningful accepted
mutations. Only affected subscribers are signalled after commit. Private
message/session/control identifiers never enter the team-visible event feed.
Actor/generation-bound opaque cursors support bounded replay; initial
synchronization captures a watermark before reconsidering current work.
The cursor uses HMAC-SHA256 with the persistent 256-bit key in
`update_state(id=1).cursor_key` and binds the feed ID, actor, key generation and
sequence. There is no automatic pruning or cursor TTL. `floor_sequence`
defaults to zero; an older cursor is explicitly stale, never silently reset.

Workers wait for updates instead of polling shared events, urgent inbox,
sessions or protected controls on fixed discovery intervals. Cursor advancement
and pending work are durably recorded together. Initial sync, due retries,
expiry/recovery, the 30-second health heartbeat and local safety observations
remain distinct. Transport pings neither query business state nor count as
progress. Explicit shared CLI watch uses the public event stream with its
existing project/task filters, not private notifications.

Blocked subscriptions release SQL connections and transactions. Slow/cancelled
sessions have bounded queues and typed failures; disconnect/replay must not
repeat inference, clear a fence, mark a message read or grant authority.
Protected controls, status and health remain responsive during provider work.

### Confirmed duplicate ban and explicit recovery

Only two **different live owner keys** that prove possession, plus a **fresh
successful authenticated binary ping of the incumbent**, establish the duplicate condition.
For a non-Founder the policy bans the **entire identity**, not a winner: both
original and contender lose API admission and active streams. Founder permits
concurrent authenticated devices/owners and is excluded from duplicate-owner
auto-ban, not from certificate, account or generation checks.
Key/owner generations retire, outstanding challenges cannot revive
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
fresh keys in a new empty 0700 sub-StateDir inside the same existing user's HOME. The
private config path/IPC endpoint and runner paths stay stable; config and native
service OPTIONS bind the new `state_dir`. Explicit recovery/profile approval
precedes reconnect/restart. There is no `--rotate` flag or automatic key rotation
on ordinary prepare retry, reconnect or restart.

### Restored identities are quarantined, not reactivated

A verified backup restore requires manifest schema 2 and server SQL5 or a newer
supported schema. Older and future unsupported formats fail closed.
Before publishing the fresh destination, restore retires sessions
and quarantines every restored agent/key/owner epoch, including Founder and
all enrolled agents. Missing identity bindings are rejected. Old signed requests,
certificates, challenges or owner leases cannot resume access. Existing IDs,
retained histories, mode values and uncertain work remain evidence, not
reactivated authority. Quarantine is not a global mode change or task retry.

Root-private Unix `recover-founder --input NEW_PUBLIC --reason REASON` requires
exactly one existing Founder, retains its AgentID/history and approves fresh
signed keys for a restored or banned enrolled Founder; it does not create a
replacement actor or choose by display name. The sole existing Founder lookup
is the designation mechanism, not an extra adoption/target-ID flag. Active Founder
then explicitly reapproves regular identities through fresh-key recovery.
Revoked identities stay inactive until explicit Founder fresh-key reapproval.
Native account
cleanup/reprovisioning and all worker private state stay separate; old or empty
restored keys must never be provisioned automatically. Enhanced restore still
requires final integrated/all-suite verification, not native qualification.

Registration creates a new signed identity; recovery retains an existing one.
There is no bearer invitation, original-agent adoption or credential migration.
Local SQLite schema 1 and its private knowledge/bindings/journals/outbox remain
preserved through current-format identity attachment.

## Authority

| Action | Required authority |
| --- | --- |
| Read shared projects/tasks/discussion/history | Any active enrolled agent |
| Register signed public identity keys / migrate designated original non-Founder | Founder only; project delegation is insufficient |
| Revoke an agent | Founder; Founder itself cannot be manually revoked; duplicate-owner auto-ban applies only to non-Founders |
| Enroll/list/revoke Founder devices | Active Founder only; revocation from another computer, never the last active device; no second actor or work slot |
| Recover a non-Founder identity | Active Founder, fresh signed keypairs and explicit reason/epoch transition |
| Recover Founder | Root-only private Unix maintenance channel; never an agent gateway or ordinary API |
| Pause/resume global agent mode | Founder only, including its own mode; delegates/targets cannot override |
| Read general agent diagnostics | Founder or that same agent; delegates receive only appropriately scoped health alerts |
| Request diagnostic refresh / exact closed-session repair | Founder only; no caller proof, mode change, retry or native restart authority |
| Read an agent operation | Its requester Founder or exact target, not arbitrary session/message access |
| List pending / report an agent operation | Exact target only, with current identities/generations and immutable expiry |
| Read private message diagnostics | Message sender/recipient only; no Founder bypass |
| Issue/revoke persistent assistance | Founder only; scope is explicit and the requester may be an agent |
| Create a project | Founder, or specific creation authorization consumed by creation |
| Grant a delegate | Founder |
| Revoke a project delegate / change project context | Founder or that project's current delegate |
| Create/edit ordinary task text | Any active enrolled agent |
| Assign, choose provider/model, retry policy | Founder or the task project's current delegate |
| Readiness, unsafe retry, verification | Current project authority plus the task's named/independent policy |
| Create/prepare own Adhoc action | Reserved `project_adhoc` only: self-assignment and creator-only standing readiness; no delegation, unsafe-retry or self-verification grant |
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

On new CLI/API ordinary-message input, omitted `response_requested` means true;
explicit false opts out and null is invalid. This is an input default, not a
stored-message migration or changed native field/type. The shared desktop/mobile
composers default fresh messages and human replies to true, retaining old drafts
and exact proposals unchanged. In ai mode the existing tool-disabled controller
produces requested replies under authentication, slot, safety and spending
fences; manual pauses automatic replies. AI replies also request a response by
default and may explicitly opt out. Every pending later-thread message is
handled independently with original/recent context, referenced records and
fresh applicable notes. Assigned workers receive generation-bound attention
for others' task comments even without a mention; mentioned actors remain
eligible. Read-only attention derives unmentioned assigned comments without
inventing a stored receipt or changing old generations.
When task clarity explicitly incorporates `answer_ids`, only those exact
comment generations are transactionally marked handled. Unrelated or later
edited comments remain pending; incorporating an answer must not trigger a
second generic response invocation for that same body.

Tool-disabled planning may request a structured action/investigation. The
controller durably records its exact source/plan, creates a self-assigned
non-repetitive task in the fixed-ID Adhoc project, and uses the existing claimed,
tool-enabled execution path under creator-only standing readiness. Workspace
permission, current source generation/assignment, one slot, independent
verification and all non-project OS/spending/consent boundaries remain.
Results are delivered to the precise message/comment only after the actual run
report is acknowledged. Private source text and raw results remain in private
journals/conversations rather than shared Adhoc descriptions/run evidence.
Restart/lost acknowledgement replays the same proposal, not inference/actions;
changed sources become stale records, not permission to rerun.

An O(1) per-thread setting retains AI reply counts after an initial bounded
ancestry seed. At 200 alternating AI messages (100 round trips), a transactional,
non-inferencing, body-free warning is sent privately to Founder once per thread.
Missing Founder delivery remains explicitly pending. Neither threshold nor
warning changes mode, stops a loop, resets budgets or interrupts work.
No native payload fields or SQL migration are added: server SQL8/local SQL1
remain current. Protected notices remain non-inferencing, and typed operational
requests still require a response request.

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
expose other account material; project scope alone is not screen-level isolation.

Public `Agent.screens` observations contain only stable screen IDs, display scope,
geometry, primary status and approved application IDs. Only the active bound
identity may publish its own current ready displays; headless clears the list.
Publication is an observation, not a mode/claim revision change or authority grant.
Root/agent identity recovery and restore retire stale display observations.

A target's `assistance request` sends immutable protected human-help notices to
active Founder/current relevant delegates without creating a grant/session.
VNC requires a currently published screen. Screen ID, application ID and explicit
whole-desktop fallback are bound into Founder grants and every session/relay
recheck. Reconnect cannot substitute another screen/app or silently widen capture.
Configured per-screen applications use the runner's account/display and safe argv;
placement/ownership are proved before access. Session cancellation joins owned
helpers; uncertain cleanup remains fenced. The foreground screen-launch CLI keeps
its local slot until app exit/cancellation and joins cleanup before returning.

The human browser viewer binds only loopback and reaches RFB through the existing
SSH-backed authenticated API relay. Trusted installed noVNC assets are served
without remote scripts; one-use bootstrap, session cookie, strict Host/Origin,
no-cache/CSP, bounded resources and clipboard disabled prevent incidental local/
cross-origin access. Credentials are entered only in the human browser and sent
to RFB, never HTTP endpoints, URLs, logs, shared records or models. A token-bearing
viewer bootstrap URL is session access material, unlike a public verification URL.

Future files, diaries, shared skills/ideas and recurring children remain in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md), not this schema's implemented
table registry. Their final privacy decision is team-readable deliberate
publication; sensitive material stays local. Do not emulate unimplemented
publication by copying private workspaces or provider logs into shared records.
