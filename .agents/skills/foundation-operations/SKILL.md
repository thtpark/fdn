---
name: foundation-operations
description: "Use when checking Foundation or identity-proxy health, inspecting automatic native session cleanup/recovery, requesting fresh diagnostics, recovering proven session fences or enrolled identity keys, diagnosing blocked workers/replies, pausing automation, reconciling runs/sessions, managing knowledge/backups/TLS, or inspecting Founder-authorized software upgrades and native receipts."
---

# Operate and recover without hiding uncertainty

Read `guide show DEPLOYMENT.md`, `guide show SCHEMA.md` and
`guide show COMMANDS.md` through the installed local binary.

1. Resolve actual role/account/HOME/configuration/version/identity/authority.
   Start read-only: `doctor`, `status`, `agent identity`, `service status`,
   `mode show`, `work status`, relevant task/discussion/run/session, protected
   identity-service health and native manager state. Use `agent diagnose AGENT_ID`
   for Founder/self only, and `message diagnose MESSAGE_ID` only as that message's
   sender/recipient; delegation alone grants no general diagnostics. These commands
   fetch saved observations, not provider probes/actions. Inspect local `health`,
   server receipt/observation age, effective provider/model versus preferred reply
   provider, authentication observation time, phase/deadline and separate loop/
   progress clocks. Idle polls/heartbeats are not task progress; missing/stale data
   is not healthy. Defaults: heartbeat/report missing 90s, deadline grace 30s,
   eligible unexplained queued reply 120s. Pause, ordinary idle, no requested
   response and in-deadline work are not stuck failures. Handled receipt is not
   proof of an AI reply. Foundation-down detection needs native/outside monitoring.
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
   Protected health alerts/recovery notices are server-created private messages,
   response_requested=false, scoped to active Founder/current task delegates and
   deduped durably by incident/cause/action. They never renew sign-in notices or
   start login/session/inference/retry/resume/recovery. After login, preserve and
   inspect Founder pause, open/uncertain session, uncertain invocation, different
   selected/required provider and permission/report blockers; auth resolves only
   its own observation. Never publish unrelated message references, private
   journals, raw errors, paths or credentials as diagnostic evidence.
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
6. Routine interactive cleanup is automatic first: the target retries its exact
   owned endpoint with bounded calls and 1-30-second backoff, using durable
   generation-bound native socket/process-start evidence after restart. Proven
   no-process tmux preparation failures retire their fence without operator input.
   The server gives session cleanup a 30-second server-timed grace before human
   alert delivery; authentication and other essential blockers retain their own
   rules. Do not interrupt this recovery by resetting state or reopening login.
   Unknown legacy ownership, PID/socket replacement, live sessions and unproved
   cleanup remain real safety blockers, not a reason to guess or force clear.
   For Founder remote diagnostics/session recovery, follow the exact workflow
   below; never send a caller stopped-process assertion remotely. Local recovery
   remains separate: after confirming all owned processes stopped, use exact
   `work recover --journal INVOCATION --acknowledge-stopped` or the exact
   `--session SESSION` variant. Retain rejected/stale evidence; neither path
   grants a new execution attempt or revives an old approval. A fully returned
   tool-disabled reply with the wrong phase outcome is `reply-rejected`, exposed
   by `work status` `text_response_blocks` and a protected `provider-response`
   notice. Scope/acceptance/preflight require clear/blocked/paused, not reviewed.
   The exact semantic action stays fenced without blocking unrelated inbox work;
   polling, restart and a new work-run operation never authorize another model
   call. Exact native journal recovery can authorize a fresh text review after
   stopped-process confirmation. Never coerce the retained reply or clear an
   uncertain/possibly applied decision under this rule.
7. Manage private notes through `knowledge put/list/show/history`; use current
   revision, explicit selection/archive and useful consolidation. `inventory
   refresh` is native read-only observation, not a network scan or proof of OS/
   provider permission. Do not automatically publish any of it.
   Shared notes are separate: inspect `note context --project PROJECT` or
   `note show company|project PROJECT|agent [AGENT]`, then use `note set --input
   FILE` with exact revision/content/reason and current scope authority.
   Founder alone edits company guidance; project delegates cannot edit unrelated
   agents or Founder. Notes are team-readable context, not permissions; no
   automatic resource fetching or promotion of private knowledge occurs.
   Missing note context blocks new inference; verify the SQL6-capable server
   before workers, preserving in-flight snapshots and retry/recovery fences.
8. For backup, generate/store the age private identity off-host, retain its
   public recipient, and use `fdn-service backup --output FILE --recipient
   RECIPIENT`. Restore only to a fresh directory with the private identity
   supplied through a protected file. Verify full archive/integrity/compatible
   schema. Manifest schema2 and supported server SQL5-or-newer are required;
   obsolete or future unknown formats fail closed. Restore quarantines ALL identity/owner/key
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
     instructions; start and verify exact binary and guide versions.
     Founder `upgrade check [--input TARGETS]` is read-only. Plan pins current
     identity/generation/policy/source, exact greater release/hash, native nonce
     and public age recipient; it does not install. `upgrade apply --input PLAN`
     also needs separately administrator-provisioned helper policy/operator SSH.
     It pauses, obtains current signed authority and queues private target-owned
     requests. Independent native receivers decide, create encrypted cold backups,
     install/verify and reply with actual results; Foundation completes before
     dependent agents. Inspect message show/thread for queued/accepted/refused/
     completed/failed states; delivery/acceptance never means installation.
     Native phases and evidence remain private. An interrupted mutation projects
     operator-required with its original phase code; inspect the same plan with
     `upgrade status --input PLAN`, even while Foundation is offline, without
     pretending a fresh authorization or repeating installation. Never reset
     retry/fence state, auto-renew permits or assume binary rollback restores SQL.
11. Inspect `agent identity` and the protected identity-service/Unix proxy
    status for transport or account/key/owner-generation errors. Identity,
    providers and runner share the existing installing user; that user owns the
    keys, with distinct private state and no key bytes in model/shared output.
    Defaults share HOME/.foundation. Explicit current-format configuration paths
    are respected without old-layout discovery. Keep external tool settings
    intact. Local username need not match agent name; only current single-user
    service options are accepted. Preserve keys, profiles, provider state,
    journals/outbox and fences; never auto-transfer
    ownership or create another identity to bypass an access failure.
    Legacy bearer files, TCP ingress, shared SSH accounts or another owner
    process are never recovery shortcuts. Root account reconciliation is also
    generation-fenced: stale operations or same-generation changed keys must
    fail before OS changes; a retained disable tombstone must not be bypassed.
    Preserve root inventory and inspect public identity/request/key-generation
    references instead of editing it, replaying old keys or guessing an epoch.
12. A proved second different live owner key plus a fresh successful incumbent
    binary control ping bans the whole identity, including the original and Founder,
    and closes API/streams. A stale/crashed/same-key reconnect, ordinary CLI
    traffic, public-key collision or unproved attempt is not ban evidence.
    Native commands/updates use protobuf protocol 2 over SSH/mTLS/Unix IPC;
    HTTPS origin settings describe trust, not an HTTP service to probe or a
    fallback. Keep current generation proofs until native OS-only qualification.
    Preserve the audit and work fences; do not delete state or auto-unban on
    restart. There is no `--rotate`: safely stop identity/affected work, retain
    the old private StateDir and ALL runner state/journals, and choose a new
    empty user-owned 0700 StateDir inside the SAME existing user's HOME. Preserve
    stable private config file/IPC and every runner path; update only the intended
    state_dir binding in config/service OPTIONS and run `prepare` there. Normal
    retries never auto-rotate. Obtain intentional recovery: Founder uses
    `agent recover AGENT
    --input NEW_PUBLIC --reason REASON [--output PUBLIC_PROFILE]` for non-Founder;
    Founder requires authorized root-local `fdn-service recover-founder --input
    NEW_PUBLIC --reason REASON` through private Unix maintenance, not an agent
    gateway/API route. Connect the approved new profile, then restart the same
    native identity/runner services without changing runner state. Current-user
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

## Founder fresh diagnostics and exact closed-session repair

1. Keep default `agent diagnose AGENT_ID` as saved read-only GET. For an explicitly
   requested fresh check, use your enrolled Founder configuration and a stable
   operation ID:

   ```sh
   agent-cli --config /PRIVATE/founder-agent.json \
     --operation-id diagnostic-refresh-001 agent diagnose AGENT_ID --refresh
   agent-cli --config /PRIVATE/founder-agent.json agent operation REFRESH_REQUEST_ID
   ```

   Copy `REFRESH_REQUEST_ID` from returned `data.id`, not the envelope's submission
   `operation_id`. Queued/checking is not fresh success; offline/dead targets may
   remain queued until expired. Each operation lasts exactly 900 seconds from
   creation, distinct from sign-in notices/provider/live-session lifetimes.
   There is no wait/provider/inference/shell or native restart option.
2. Wait by explicitly reading the same common `agent operation REQUEST_ID` path,
   not by resubmitting with new IDs. `refreshed` is a bounded safe observation;
   check server-clock `server_observed_at`, immutable `expires_at` and result
   `captured_at` (a retained conservative server-time capture bound, not target
   wall time) for lifespan/capture freshness. Target inspection/provider/health
   observation times are separate UTC values and may be skewed; they cannot
   renew expiry or make an expired result fresh. The dynamic server projection
   is not part of the immutable operation fingerprint. Inspect health and
   separate provider/fence/slot data. Credentials
   present but unverified are not vendor usability; ready is observed, not a paid
   inference test. Refresh works through a dedicated non-inference control loop
   while paused/fenced/busy and does not renew sign-in help or change mode.
3. For an eligible closed-session fence only, copy the fresh operation's
   `result.observation.session_fence.session_id` and `fingerprint` into an
   owner-restricted input. Replace placeholders with exact reported values;
   `expected_fence` is the canonical lowercase 64-character SHA256:

   ```json
   {
     "session_id": "SESSION_ID_FROM_REFRESH",
     "expected_fence": "SHA256_FROM_REFRESH",
     "reason": "Recover the exact observed closed session"
   }
   ```

   ```sh
   agent-cli --config /PRIVATE/founder-agent.json \
     --operation-id closed-session-repair-001 agent repair AGENT_ID \
     --input /PRIVATE/session-recovery.json
   agent-cli --config /PRIVATE/founder-agent.json agent operation REPAIR_REQUEST_ID
   agent-cli --config /PRIVATE/founder-agent.json agent diagnose AGENT_ID
   ```

4. The target independently verifies exact terminal session/current fence/key
   generations under `work.lock` and requires existing durable native
   `CleanupProven` evidence; no remote `--acknowledge-stopped`/proof assertion is
   accepted. Initial legacy/unproven fences are `operator-required`, not forced
   clear: process-name/PID absence is not proof. Retained Agent1 state may still
   need explicit native operator inspection and exact local stopped-process
   acknowledgement. No automatic Agent1 recovery is promised.
5. Inspect `recovered`, `rejected`, `operator-required` or `expired` as an outcome,
   not task completion. Mutation/final local receipt are atomic; after network
   uncertainty reuse the exact original operation/input, never clear a new fence.
   Repair does not stop arbitrary processes, retry journals/inference, recover
   identity, restart services, open sessions, erase evidence or change task,
   spending, retry or mode authority. Approved ai work may resume when all
   independent fences permit; manual stays paused. Diagnose again for actual
   progress, not an `automation_may_resume` promise.
6. Controls/results are protected server-only data, response_requested=false;
   ordinary message send/receipt cannot forge/settle them. Requester Founder and
   exact target read only their operation, not unrelated sessions/messages.
   Publish only safe codes/mode/observations, never raw output/errors, credentials,
   process arguments or workspace paths. `agent recover` remains fresh-key
   identity recovery. See the full
   [command/input contract](../../../COMMANDS.md#founder-diagnostic-refresh-and-exact-closed-session-recovery).

Read `guide show CAPACITY.md` before a scale claim. Existing evidence is 1,000
tasks/24 concurrent writers, not 1,000 agents. Do not add infrastructure, weaken
durability or run paid benchmarks without a measured plan and authorization.

Report actual health, current state/blockers, operation/identity references,
private evidence location without contents, recovery done versus pending, and
the smallest remaining authorized action.
