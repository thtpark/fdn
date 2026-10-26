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

## Current Go security upgrade

API/schema contract **2** uses server SQLite migration **4**; local worker SQL
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
its distinct runner uses `connect --migrate` to replace only the matching old
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

SSH accounts and kernel UID establish the principal, not unrestricted project
authority. Current mTLS/key/owner-generation proofs remain mandatory: an OS-only
replacement has not been natively qualified against stale SSH-channel admission.
Deployment/wipe requires an explicitly resolved machine, role and exact paths;
source changes do not authorize deleting unidentified live state.

## Backup compatibility and quarantined authority

Backup-manifest schema 1 and current schema 2 are recognized independently of
SQLite `user_version`: known old server SQL migrates through version 4, while
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
in the same identity HOME with stable config/IPC. Do not reimport old credentials
or auto-provision restored old/empty keys. Root account inventory/SSH policy,
HOMEs and native services remain separate recovery work; enhanced restore and
final all-suite acceptance are still pending, native root/group qualification
unperformed.

## Evidence that must not be inherited

Historical Rust/CouchDB phase acceptance, test counts, coverage, package hashes,
security findings, signing claims and native restore/provider observations are
not certification of Go source. Current suites and release/deployment receipts
qualify their own revision and explicitly state synthetic/native/live limits.

The current implementation does not deliver future shared files/diaries/skills/
ideas, recurring children, complete host recovery or a 1,000-agent capacity
guarantee. Their preserved goals and open decisions are maintained in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) and [CAPACITY.md](CAPACITY.md).
