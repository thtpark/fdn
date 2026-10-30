# Supported baseline and state preservation

## Supported baseline

Foundation 0.3.5 is the source baseline: application/API schema 2, native message
protocol 2, server SQLite 5 and local-worker SQLite 1. Backup manifests must use
the current schema 2. Older formats and future unknown formats fail explicitly;
there is no bearer enrollment, token migration, split-account installer,
ID-derived account-name fallback or automatic old-layout discovery.

Existing server databases below SQL5 are rejected without changing their
records. A new empty database is initialized through the immutable ordered SQL
statements. Those statements remain historical schema definitions, not an
importer or permission to adopt an older installation. Later schema changes
must append a new migration and preserve current-format state.

The current source appends SQL6 for shared company/project/agent Markdown notes
and their scoped update triggers. SQL5 databases upgrade transactionally without
rewriting existing records, messages, receipts or identity/work fences; local
SQL1, API/schema2, native protocol2 and backup manifest2 remain unchanged.
Upgrade Foundation before note-aware workers. A SQL5 binary cannot open a SQL6
database: binary rollback is not database rollback. Notes are deliberate shared
publication, not migration or automatic upload of private local knowledge.

SQL7 adds multiple independently keyed Founder computers under one actor and
independent live Founder owner generations. It preserves existing identity JSON,
keys, UID/name reservations, requests/receipts, private messages and work fences.
Account/challenge foreign keys now reference enrollment bindings; original
bindings retain their AgentID, and additional bindings use server-issued
device IDs. Founder alone approves fresh signed device requests and can revoke
one computer without replacing another's keys. Non-Founder computer/owner
exclusivity and the single work slot per agent are unchanged.

Deploy compatible Foundation/account-helper binaries before enrolling extra
devices, and use matching device-aware clients. Optional device certificate and
proof bindings do not weaken existing primary-client admission or introduce a
protocol downgrade. API/schema2, native protocol2, local SQL1 and backup
manifest2 remain unchanged. A SQL6 binary cannot open SQL7; preserve a compatible
recovery point rather than rolling back the database. Root-local whole-Founder
recovery retires every old device, and restore quarantines all device bindings.

Removing compatibility does not authorize deleting installations, credentials,
backups, journals, identity/account reservations or operation evidence.

## Explicit configuration and current paths

User defaults are under `$HOME/.foundation`: `config`, runner `state`,
`identity`, IPC `run`, `workspaces`, managed `bin` and `guidance`.
The development wrappers set HOME to the checkout's ignored `.local` and use
only that layout; they do not scan or import other locations.
Existing XDG, provider, GitHub, Cloud SDK and credential environment settings
remain inherited.

An explicit `--config` may name any valid owned current-format configuration.
Paths inside it are respected, not rewritten merely because they differ from
the defaults. The current native manager and skill-discovery locations keep
their OS-specific paths. Guide installation updates only its current managed
links and instruction block; foreign links/files are conflicts, not migration
candidates.

The existing installing OS user owns identity, runner and provider state.
Native service plans use one existing user and current `user`/`uid`/`gid`/`home`
fields. They never create accounts, transfer keys, change HOME permissions or
silently convert another user's services.

## State-preserving upgrades

Resolve the exact computer, account, identity, configuration and source before
changes. Stop/reconcile active work, retain its checkpoints and exact proposals,
and verify an authenticated encrypted recovery point. Do not copy identity keys
to another computer or reset SQLite to make an upgrade succeed.

Operational execution uses durable private requests and target-owned decisions
and results. Upgrade plans bind an exact greater immutable release, public
backup recipient, native policy, target generation and fixed expiry. Submission
queues messages; separately provisioned independent native receivers apply the
existing authority, pause, work and OS checks. Foundation completes before
dependent agents. Acceptance is not installation or task verification.

A protocol/payload change requires coordinated compatible binaries and guides.
An older installed receiver cannot acquire new capabilities through a source
edit. Bootstrap requires separate explicit operator authorization.
Preserve uncertain native receipts and pending result delivery; never repeat
installation, renew an expired proposal or roll back SQL automatically.

## Backup compatibility and quarantined authority

Restore accepts only manifest schema 2 and a supported server database at or
above SQL5. It authenticates the complete archive and checks hashes and SQLite
integrity before publishing a fresh destination. Existing destinations are
never overwritten.

Every restored enrolled identity has its device/owner/key epochs quarantined.
Missing identity bindings fail closed rather than manufacturing keyless actors.
Old certificates, owner leases and signed requests cannot reactivate access.
Root-private `recover-founder` recovers the sole enrolled Founder with fresh
keys while preserving its ID/history; that active Founder then uses ordinary
fresh-key identity recovery for regular agents. Current mode, work fences,
retry and spending authority remain separate and unchanged.

SQLite/trust recovery is not a full-host restore. Root account inventory,
SSH/groups, provider/identity homes, workspaces and native services require
their own explicitly authorized reconciliation.
