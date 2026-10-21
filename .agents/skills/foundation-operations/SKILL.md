---
name: foundation-operations
description: "Use when checking Foundation or agent health, diagnosing a blocked worker, pausing automation, reconciling interrupted runs or sessions, managing private knowledge, backing up/restoring SQLite and trust, renewing TLS, or planning a state-preserving native upgrade."
---

# Operate and recover without hiding uncertainty

Read `guide show DEPLOYMENT.md`, `guide show SCHEMA.md` and
`guide show COMMANDS.md` through the installed local binary.

1. Resolve actual role/account/HOME/configuration/version/identity/authority.
   Start read-only: `doctor`, `status`, `service status`, `mode show`,
   `work status`, relevant task/discussion/run/session and native manager state.
2. Classify the blocker: transport/trust, login, current approval/specification,
   workspace permission/identity, occupied slot, retry/backoff, journal/outbox,
   maintenance or unproven interactive cleanup. Do not replace it with a default
   success/empty-work explanation.
3. Preserve private state, original operation/proposal, exact generations,
   provider version and local evidence. Service startup/registration accepted
   does not prove final health. A singleton conflict is an owner to inspect,
   never a lock file to remove.
4. Only an authenticated Founder request may pause/resume a non-Founder agent:
   use `agent mode AGENT --input FILE` with explicit mode/reason. The autonomous
   target mirrors the request and cancels/joins inference; it never waits for a
   local person or self-sets manual. Delegates may be AI agents, not humans.
   Founder can use `mode set` for its own mode only. Inspect
   pending reports/session fences before OS stop/restart. Do not reboot active
   work just to make a diagnostic pass.
5. Reconcile lost acknowledgements with the same operation and original input.
   Prepared reports replay without repeated inference. Unknown side effects
   stay fenced; no timeout takeover or fresh run ID resets retry authority.
6. After confirming all owned processes stopped, use exact
   `work recover --journal INVOCATION --acknowledge-stopped` or the exact
   `--session SESSION` variant. Retain rejected/stale evidence; recovery does
   not grant a new attempt or revive an old approval.
7. Manage private notes through `knowledge put/list/show/history`; use current
   revision, explicit selection/archive and useful consolidation. `inventory
   refresh` is native read-only observation, not a network scan or proof of OS/
   provider permission. Do not automatically publish any of it.
8. For backup, generate/store the age private identity off-host, retain its
   public recipient, and use `fdn-service backup --output FILE --recipient
   RECIPIENT`. Restore only to a fresh directory with the private identity
   supplied through a protected file. Verify full archive/integrity/compatible
   schema. This restores SQLite/trust, not OS/provider homes or full-host state.
9. For TLS renewal, stop Foundation, use `tls-renew` under the same CA and finish
   any exact retained partial renewal before serving. Expired/compromised trust
   needs explicit coordinated recovery; never turn off verification.
10. For upgrades, follow foundation-install: verify a source-bound package,
     compatible schema and recovery point; stop/join actual work; preserve mode,
     identity, auth, knowledge, bindings, workspaces, journals/outbox and HOME
     instructions; start and verify exact binary and guide versions. No generic
     force/upgrade/reset command is implemented.
11. On suspected exposure, preserve redacted evidence and obtain current
     authority for revocation/rotation. Founder can revoke agents; parties can
     close/revoke sessions. Previously downloaded material cannot be recalled.
     Persistent assistance grants survive closed/expired sessions and restarts;
     Founder revokes the grant itself to remove future access. Reconnection
     under a still-valid grant never requires repeated authorization.

Read `guide show CAPACITY.md` before a scale claim. Existing evidence is 1,000
tasks/24 concurrent writers, not 1,000 agents. Do not add infrastructure, weaken
durability or run paid benchmarks without a measured plan and authorization.

Report actual health, current state/blockers, operation/identity references,
private evidence location without contents, recovery done versus pending, and
the smallest remaining authorized action.
