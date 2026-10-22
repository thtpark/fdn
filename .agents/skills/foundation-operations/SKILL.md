---
name: foundation-operations
description: "Use when checking Foundation or identity-proxy health, diagnosing blocked workers or duplicate-owner bans, recovering fresh identity keys, pausing automation, reconciling runs/sessions, managing private knowledge, backing up SQLite/trust, renewing TLS, or planning a state-preserving upgrade."
---

# Operate and recover without hiding uncertainty

Read `guide show DEPLOYMENT.md`, `guide show SCHEMA.md` and
`guide show COMMANDS.md` through the installed local binary.

1. Resolve actual role/account/HOME/configuration/version/identity/authority.
   Start read-only: `doctor`, `status`, `agent identity`, `service status`,
   `mode show`, `work status`, relevant task/discussion/run/session, protected
   identity-service health and native manager state.
2. Classify the blocker: transport/trust, login, current approval/specification,
   workspace permission/identity, occupied slot, retry/backoff, journal/outbox,
   maintenance or unproven interactive cleanup. Do not replace it with a default
   success/empty-work explanation. A new requested task/reply/mention/manual
   action must escalate sign-in/permission blockers to active Founder and current
   relevant delegates, even when initial onboarding or an older notice expired.
   Inspect authentication_requests/permission_requests and real delivery state.
   Sign-in requests expire after 15 minutes and do not renew on unchanged
   polling/restart. Founder can use `agent request-sign-in AGENT
   --input FILE` with provider/reason and optional matching project_id to request
   a fresh check/notice, even while the target remains paused. The control also
   expires in 15 minutes; preserve/reuse its exact operation after uncertainty.
   No login, inference, assistance grant or mode change follows from the request.
   Once explicitly authorized/opened, prefer supported headless/device login;
   showing its sign-in screen/public verification link to the authorized human
   is permitted, not itself a privacy violation. Keep credentials, one-time codes
   and secret-bearing URLs in the protected human flow, not model/shared logs.
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
   schema. Manifest schema 1/2 is supported through SQL3 migration; future
   manifest/SQL versions fail closed. Restore quarantines ALL identity/owner/key
   epochs before publication, so old tokens/keys/certificates never reactivate.
   Root-private fresh-key Founder recovery retains ID/history, then Founder
   explicitly reapproves regulars with fresh signed keys; no automatic mode
   change or task resumption. Keep all uncertain work/evidence fenced. This
   restores SQLite/trust, not root account/SSH policy, identity/provider homes,
   services or full-host state; enhanced restore still needs final integration.
9. For TLS renewal, stop Foundation, use `tls-renew` under the same CA and finish
   any exact retained partial renewal before serving. Expired/compromised trust
   needs explicit coordinated recovery; never turn off verification.
10. For upgrades, follow foundation-install: verify a source-bound package,
     compatible schema and recovery point; stop/join actual work; preserve mode,
     identity, auth, knowledge, bindings, workspaces, journals/outbox and HOME
     instructions; start and verify exact binary and guide versions. No generic
     force/upgrade/reset command is implemented.
11. Inspect `agent identity` and the protected identity-service/Unix proxy
    status for transport or account/key/owner-generation errors. Providers and
    runner files must not contain Foundation long-term keys or certificates.
    Legacy bearer files, TCP ingress, shared SSH accounts or another owner
    process are never recovery shortcuts. Root account reconciliation is also
    generation-fenced: stale operations or same-generation changed keys must
    fail before OS changes; a retained disable tombstone must not be bypassed.
    Preserve root inventory and inspect public identity/request/key-generation
    references instead of editing it, replaying old keys or guessing an epoch.
12. A proved second different live owner key plus a fresh successful incumbent
    WebSocket ping bans the whole identity, including the original and Founder,
    and closes API/streams. A stale/crashed/same-key reconnect, ordinary CLI
    traffic, public-key collision or unproved attempt is not ban evidence.
    Preserve the audit and work fences; do not delete state or auto-unban on
    restart. There is no `--rotate`: safely stop identity/affected work, retain
    the old private StateDir and ALL runner state/journals, and choose a new
    empty identity-owned 0700 StateDir inside the SAME identity HOME. Preserve
    stable private config file/IPC and every runner path; update only the intended
    state_dir binding in config/service OPTIONS and run `prepare` there. Normal
    retries never auto-rotate. Obtain intentional recovery: Founder uses
    `agent recover AGENT
    --input NEW_PUBLIC --reason REASON [--output PUBLIC_PROFILE]` for non-Founder;
    Founder requires authorized root-local `fdn-service recover-founder --input
    NEW_PUBLIC --reason REASON` through private Unix maintenance, not an agent
    gateway/API route. Connect the approved new profile, then restart the same
    native identity/runner services without changing runner state. Native-root
    `agent-identity service status --input OPTIONS` inspects the exact paired
    installation (no `--config`/`--apply`); runner doctor/status checks are
    separate. New generations do not revive old certificates/runs.
13. On suspected exposure, preserve redacted evidence and obtain current
    authority for revocation/rotation. Founder can revoke non-Founder agents;
    parties can close/revoke sessions. Downloaded material cannot be recalled.
    Persistent assistance survives closed/expired sessions and restarts;
    Founder revokes the grant itself to remove future access. Reconnection
    under a still-valid grant never requires repeated authorization. Full
    software/RAM cloning or sequential use remains possible; no hardware
    signer, attestation or perfect physical-clone prevention is claimed.

Read `guide show CAPACITY.md` before a scale claim. Existing evidence is 1,000
tasks/24 concurrent writers, not 1,000 agents. Do not add infrastructure, weaken
durability or run paid benchmarks without a measured plan and authorization.

Report actual health, current state/blockers, operation/identity references,
private evidence location without contents, recovery done versus pending, and
the smallest remaining authorized action.
