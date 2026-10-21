# Design continuity and migration boundaries

Foundation's prior implementation supplied the product goals, authority model,
operational workflows and phase structure adapted into this repository. The Go
documents and skills here are now the development/operating references; a second
checkout is neither required nor a runtime dependency.

## Preserved decisions

- Founder authorization, project delegation, global invitation sponsorship,
  current assignment/readiness/result approval and independent/named review.
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
| Per-agent Foundation-host OS provisioning for direct data access | Application identity plus separately operator-provisioned restricted SSH transport |
| Large historical CLI catalog including planned leaves | Only executable command leaves in [COMMANDS.md](COMMANDS.md); task mutations use `task change` kinds |
| VM/container-only verification | Locally isolated suites without either; actual deployment checks reported separately |
| Release/runtime builds mixed with old installation practices | CI-only merged-release packaging; deploy prebuilt verified native artifacts |
| Operational skills available only with a source checkout | Canonical portable skills, embedded offline guide, release payloads, private home discovery and explicit controller context |

There is no automatic importer for earlier databases, configuration, credentials,
identities, private provider homes, workspace bindings or run journals. Fresh
enrollment creates fresh authority. Retain old disks/backups as protected
recovery material; do not borrow an old identity or copy another account's login
as a migration shortcut.

## Evidence that must not be inherited

Historical Rust/CouchDB phase acceptance, test counts, coverage, package hashes,
security findings, signing claims and native restore/provider observations are
not certification of Go source. Current suites and release/deployment receipts
qualify their own revision and explicitly state synthetic/native/live limits.

The current implementation does not deliver future shared files/diaries/skills/
ideas, recurring children, complete host recovery or a 1,000-agent capacity
guarantee. Their preserved goals and open decisions are maintained in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) and [CAPACITY.md](CAPACITY.md).
