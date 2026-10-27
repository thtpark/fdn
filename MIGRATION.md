# Design continuity and migration boundaries

Foundation's prior implementation supplied the product goals, authority model,
operational workflows and phase structure adapted into this repository. The Go
documents and skills here are now the development/operating references; a second
checkout is neither required nor a runtime dependency.

## Preserved decisions

- Founder authorization, project delegation, current assignment/readiness/result
  approval and independent/named review. Enrollment is now separately restricted
  to Founder-approved signed public identity keys, not delegated invitations.
- One native computer identity and one serial manual/automatic/session work
  slot; local operation does not imply root or spending authority.
- Mandatory semantic preflight, useful multi-round clarification, actual-answer
  refinement, exact-current signoff, mid-run questions and safe checkpointing.
- `completed` versus `done`, stale-generation fencing, exact operation replay,
  bounded history and explicit uncertainty/unsafe retry.
- Pinned private SSH/TLS, protected human authentication, per-session target
  consent, explicit requester open, expiry and revocation.
- Independent private memory/skills/knowledge; deliberate future team-readable
  publication; no automatic secret/transcript/workspace synchronization.
- Non-secret worklogs, responsible human handoffs, and financial/legal
  notification to both Founder and the relevant delegates.

## Deliberate implementation changes

| Previous design concept | Current Go contract |
| --- | --- |
| Rust workspace and Rust-specific tests/toolchains | Go module, local Go suites and native process/protocol fixtures |
| Direct CouchDB documents, validators, roles and retention receipts | One authoritative TLS API with transactional SQLite policy |
| CouchDB `_rev`/partitions/replication | Explicit revision/generation/epoch and indexed relational projections |
| Foundation-host users granting direct database access | Per-agent managed SSH account/UID, including Founder, forced API/join stdio only; separate unprivileged server and narrow root-local provisioner, no database group or shell |
| Large historical CLI catalog including planned leaves | Only executable command leaves in [COMMANDS.md](COMMANDS.md); task mutations use `task change` kinds |
| VM/container-only verification | Locally isolated suites without either; actual deployment checks reported separately |
| Release/runtime builds mixed with old installation practices | CI-only merged-release packaging; deploy prebuilt verified native artifacts |
| Operational skills available only with a source checkout | Canonical portable skills, embedded offline guide, release payloads, private home discovery and explicit controller context |

There is no automatic importer for historical non-Go databases, configuration,
credentials, identities, private provider homes, workspace bindings or run journals. Fresh
enrollment creates fresh authority. Retain old disks/backups as protected
recovery material; do not borrow an old identity or copy another account's login
as a migration shortcut.

## Consolidating user Foundation files

New CLI/worker defaults use one owner-only `$HOME/.foundation` root:

| Purpose | Current user default |
| --- | --- |
| Runner configuration | `.foundation/config/agent.json` |
| Identity configuration | `.foundation/config/identity.json` (explicit for native identity commands) |
| Runner database, journals, bindings, provider installations | `.foundation/state` |
| Private identity keys, profile and owner evidence | `.foundation/identity` |
| Local identity socket | `.foundation/run/identity.sock` |
| Generated workspace parent | `.foundation/workspaces` |
| Managed commands/development builds | `.foundation/bin` |
| Versioned guide store | `.foundation/guidance` |

Development wrappers set HOME to the ignored checkout `.local` but otherwise
preserve the existing XDG, Codex, GitHub, Cloud SDK, SSH-agent and credential
environment. External tools choose their own directories; their configuration
and sign-in are not migrated with Foundation state. This is not a sandbox or
a claim that overriding HOME isolates every provider credential.

Existing explicit configurations continue to work. A legacy default
`.config/fdn/agent.json` is not silently replaced by an unenrolled default.
Development wrappers likewise refuse old `.config/fdn` or `.state/identity`
files instead of creating another identity.

For an authorized same-account filesystem migration:

1. Capture current identity/generation/mode, native definitions, work/status,
   pending reports/journals, paths and private recovery evidence. Keep the
   exact current source-compatible executables. Stop/join work and its identity
   proxy before moving SQLite, keys or socket/configuration bindings.
2. Retain owner-only backups of configurations and native options; move only
   proven Foundation-owned directories into an empty private `.foundation`.
   Preserve every key, certificate, journal, outbox, binding and workspace.
   Do not copy provider authentication or alter HOME permissions. Existing
   workspace paths/Git worktrees need their supported relocation, not blind
   string replacement or direct SQLite edits.
3. Update intended absolute paths in configuration and native options. Preserve
   the original account, AgentID, key generation, profile/CA/host pins and mode.
   Binary activation links and managed shell/guide links need their supported
   installers; never replace unrelated files or symlinks.
4. Restart only processes that were running before the move. Verify matching
   identity/generation, private IPC, doctor/status, pending work/fences and
   actual process owners. Keep a user-managed stopped worker stopped. This is
   not fresh-key recovery, a mode change, task retry or software/protocol upgrade.

The guide installer can repoint exact owned legacy discovery links to its new
store while preserving the previous store and unrelated HOME instructions.
Required `AGENTS.md`, `.agents/skills`, `.copilot/skills`, login profiles and
systemd/LaunchAgent definitions remain at their ecosystem discovery locations.
Older installed binaries may retain legacy hard-coded guide/launcher locations;
keep their compatibility files until a separately authorized matching upgrade.
System server paths such as `/var/lib/fdn` and `/etc/fdn` remain unchanged.

## Queued agent request/result conversations

Operational requests now use durable private message descriptors and explicit
target decisions/results. Ordinary text is context, not executable authority.
Existing authority remains current: delegates retain scoped work permissions,
Founder-only native host upgrades remain Founder-only, and central pause/
revocation/task records are still enforced immediately. Targets may accept,
defer or decline with intentional reasons; an insistence is a new linked
request, never force, new permissions or a retry-budget reset.

Upgrade apply queues messages and returns queued, not installed. Existing
direct native operator primitives remain internal recovery/administration,
not the sender's normal remote execution path. A separately provisioned
`request-worker --policy FILE` receiver must be independently supervised;
never put it in a runner/identity/Foundation/account unit it needs to stop.
Foundation's receiver uses the root-private maintenance inbox. Agents use
their own authenticated mailbox. Root policy provisioning, native process
independence and matching transport versions remain explicit prerequisites.

SQL5 rebuilds only the message table to admit the exact Foundation maintenance
mailbox without a synthetic enrolled agent. It preserves message bytes/insertion
order, both indexes, update triggers, operation receipts, identities, modes,
scoped feed/cursors and local journals. Ordinary parties still require enrolled
agents; instance messages are restricted to native requests/service replies.
There is no SQL reset. Native payload additions change the locked schema and
require a compatible coordinated deployment. SQL4 binaries reject the newer
database; an uncertain install must not automatically roll back the database.
An existing 0.3.3 installation is not upgraded by editing this checkout.
Do not renew a stale timed proposal or replay uncertain installation; inspect
the exact message/native receipts and obtain explicit current authority.

## Current Go security upgrade

API/schema contract **2** uses server SQLite migration **5**; local worker SQL
remains **1**. Ordered migrations retain existing agents, tasks, mode/assistance
attribution and operation receipts. This is not an importer for historical
non-Go state. Old hashes/invitations may remain as records but cannot authorize
v2 bearer enrollment or runtime requests.

Upgrade the server first without resetting state. Root-local Founder recovery
adopts the sole designated existing keyless Founder with fresh signed keys while
retaining its AgentID/history; it does not create a second Founder or change mode.
The private maintenance request has no extra target/rotation flag and fails
closed if the existing Founder is absent or ambiguous. Active Founder then
explicitly registers the designated original non-Founder request using
`agent register --input PUBLIC --existing-agent AGENT [--output PROFILE]`.
That worker's protected identity service uses its own approved public profile;
its same-user runner uses `connect --migrate` to replace only the matching old
credential with public AgentID. Knowledge, bindings, journals, outbox, work
fences and mode remain retained; no credentials or HOME are copied from another
identity. New workers fail closed against missing authority/identity metadata.

Normal signed registration and recovery reserve unique SSH accounts/UIDs and
retire old keys/owner generations. Confirmed different live owner keys plus a
fresh incumbent authenticated control ping ban the entire identity, including Founder; only
explicit fresh-key recovery restores access. Founder recovery uses private
root-local Unix maintenance, never an agent gateway/API exception. Neither
restart nor database compatibility proves a safe binary rollback. Real native
multi-UID/sshd/service and full-host recovery qualification remains separate and
unperformed; software/RAM clones or sequential use cannot be ruled out.

## Clean binary transport cutover

### Agent-based account names and literal mentions

New identities use an OS-safe lowercase form of the agent name for their
Foundation SSH account (for example `Agent1` -> `agent1`). The worker uses its
existing installing user (for example `alice`) for identity, runner and providers;
the local username is independent of the enrolled name. Existing Foundation
accounts, including ID-derived `fdna_...`
names, retain their reserved name/UID during upgrade, reconcile and recovery.
No OS account is implicitly adopted or renamed. Invalid normalized names and
collisions fail explicitly, without a truncated/hash-based substitute.

The existing typed root provision/disable messages add the approved `username`
field with appended protobuf field numbers. Update the helper with the server
before creating named accounts. Missing username preserves legacy ID-derived
requests, not a fallback for a conflicting retained name. Root inventory format
1, API/schema 2, native protocol 2 and local SQL1 remain unchanged by named
accounts; the current request mailbox separately requires server SQL5.

New/edited bodies accept `@{Agent1}` as well as `@{AGENT_ID}`. Name resolution
is case-insensitive, active-only and transactional; ambiguous legacy names
require IDs. Bodies stay unchanged and stored recipients/receipts remain IDs.
No historical body, mention, operation or attention receipt is rewritten.

### Transport compatibility

Native protocol **2** uses framed protobuf over existing pinned SSH/inner mTLS
and Unix IPC. HTTP command/query, owner-control, relay and root-helper transport
is not a compatibility option for native processes. Update all four binaries
and their source-matched guide; older native transports fail closed rather than
silently downgrading. Browser and external vendor/download protocols are separate.

SQL4 adds durable scoped updates without resetting identities or private worker
state. Existing logical operation IDs, JSON proposals/journals/outbox, task
generations and cleanup evidence remain meaningful; wire bytes do not replace
their logical fingerprints. No data wipe is necessary merely to change encoding.
Normal cutover does not rotate keys, auto-resume uncertain work or clear fences.

### Migrating legacy split-account workers

New worker installations create no OS users/groups. Both processes use the
existing installing user's HOME and user manager: Linux `systemctl --user`,
macOS two LaunchAgents. The user owns its identity keys and provider state;
there is no same-user key isolation claim. Windows native adapters remain
unimplemented, not qualified by this ownership change.

Old paired service OPTIONS with identity/worker account selectors fail with a
prerequisite error before native writes. Explicit private `worker_uid`/`worker_gid`
values naming another user also fail; omission/zero now means current user.
Do not fix either failure by resetting state or preparing another identity.

1. Inventory the exact original worker, enrolled AgentID, signed profile,
   fingerprints, source/version, accounts, HOMEs, service definitions, keys,
   runner database, journals/outbox, provider state and retained work fences.
2. Stop/join affected runner work and the old identity owner through the
   authorized native managers. Never run old and new identity owners together.
   Retain the old definitions and private evidence for rollback/inspection.
3. With explicit local administrator authority only where old ownership
   requires it, transfer the SAME computer's original identity state/profile
   and runner state into distinct private directories of the chosen existing
   user's HOME. Preserve file contents, public binding, journals, outbox,
   knowledge, provider configuration and workspace references. Establish the
   new owner's 0700/0600 permissions; never copy another computer's identity
   or expose key bytes. Do not delete the protected original evidence.
4. Use current-user service OPTIONS and matching private configuration/socket
   paths. Omit private `worker_uid`/`worker_gid` or set the current user's actual
   values. Register both per-user services explicitly with `--apply`, without
   sudo; the old system daemon must remain stopped/disabled by its authorized
   operator. No automatic account deletion, chown, service takeover or rotation
   is part of installation.
5. Verify the original AgentID, profile/key fingerprints, server-side account/
   generations and private state, then owner connectivity and worker health.
   This ownership migration needs no new registration or fresh keys unless
   separately recovering a compromised/banned identity. It grants no task retry,
   mode change or permission to clear an uncertain fence.

The existing-user layout itself needs no SQL migration. Current API/schema 2,
native protocol 2 and local SQL1 remain unchanged; requests use server SQL5.
Fixture tests exercise same-user admission, migration refusal and retained state,
not administrator cross-UID transfer or a completed native host migration.
Older binaries may still enforce split-account ownership and cannot be assumed
to run this layout. Native/configuration rollback needs explicit review; retained
old copies are evidence, not permission to start another owner or resume stale
journals.

SSH accounts and kernel UID establish the principal, not unrestricted project
authority. Current mTLS/key/owner-generation proofs remain mandatory: an OS-only
replacement has not been natively qualified against stale SSH-channel admission.
Deployment/wipe requires an explicitly resolved machine, role and exact paths;
source changes do not authorize deleting unidentified live state.

## Backup compatibility and quarantined authority

Backup-manifest schema 1 and current schema 2 are recognized independently of
SQLite `user_version`: known old server SQL migrates through version 5, while
future manifest versions above 2 and future SQL are rejected. Restore validates
the encrypted archive/database before publishing a fresh destination and
quarantines **all** restored agent/key/owner epochs, including Founder and legacy
rows. Old tokens/certificates/keys must never reactivate from a backup; retained
IDs/history/mode and work evidence are not current authority. No mode is
silently changed and uncertain work never resumes merely from restore.

An authorized root operator explicitly configures native ingress/account/group
bindings, then uses the private Unix `recover-founder --input NEW_PUBLIC
--reason REASON` route for the sole existing Founder, retaining its ID/history.
This also supports the designated first-upgrade keyless legacy Founder without
an extra target flag or creating a second Founder. Regular identities require
active Founder's explicit fresh signed-key reapproval. Preserve old identity
private state and all runner state; fresh preparation uses an empty sub-StateDir
in the same existing user's HOME with stable config/IPC. Do not reimport old credentials
or auto-provision restored old/empty keys. Root account inventory/SSH policy,
HOMEs and native services remain separate recovery work; enhanced restore and
final all-suite acceptance are still pending, native root/group qualification
unperformed.

## Upgrade-channel bootstrap compatibility

New source runners keep test identities and default configuration in ignored
`.local`, not the installing user's normal HOME. They do not move a live Founder
or reuse the cloud identity; explicit fresh-key rebinding remains necessary.
Development-stamped executables cannot be installed by the release updater.

The merged-release workflow now also publishes durable native ZIP bundles to
immutable `vVERSION` GitHub releases after all package jobs and exact-source
pre-merge acceptance. Existing Actions artifacts remain usable for explicit
operator bootstrap but are not the native upgrade-discovery channel.
Earlier release artifacts are not retroactively called a published upgrade.

Install upgrade-capable Foundation/worker service binaries and the explicitly
reviewed native helper/CA/operator policies once before asking Founder to roll
out a subsequent release. Older servers lack the new context/authorization/lease
routes and fail explicitly; there is no success-shaped fallback, generic remote
root route or automatic policy/sudoers installation. These additive payloads and
service-owned authorization settings do not change API/schema2, protocol2 or
local SQL1. The queued Foundation maintenance mailbox separately requires SQL5.

Preserve private plans, age recovery keys, native receipts/backups, original keys,
user state and work fences. `upgrade status` works from the original Founder's
retained local evidence even when Foundation is down. An uncertain installer
requires inspection; binary/database rollback and automatic task resumption
are not inferred. No live migration or deployment is performed by source tests.

## Evidence that must not be inherited

Historical Rust/CouchDB phase acceptance, test counts, coverage, package hashes,
security findings, signing claims and native restore/provider observations are
not certification of Go source. Current suites and release/deployment receipts
qualify their own revision and explicitly state synthetic/native/live limits.

The current implementation does not deliver future shared files/diaries/skills/
ideas, recurring children, complete host recovery or a 1,000-agent capacity
guarantee. Their preserved goals and open decisions are maintained in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) and [CAPACITY.md](CAPACITY.md).
