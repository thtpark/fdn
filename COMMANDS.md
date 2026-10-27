# Executable command reference

This catalog covers implemented Go commands only. Capitalized IDs/paths in
examples are placeholders; replace them with validated actual values.
Installation/enrollment/authentication/approval are separate operations.

## Invocation, input and outcomes

Global flags precede the command. Entity IDs precede leaf flags:

```sh
agent-cli --config /PRIVATE/agent.json --json --non-interactive \
  --operation-id CHANGE_OPERATION --expected-rev 7 \
  task change TASK --input change.json
```

| Global flag | Contract |
| --- | --- |
| `--config PATH` | Private configuration. Worker default: `$HOME/.foundation/config/agent.json`; user server default uses `server.json` in that directory; installed system servers retain their explicit paths |
| `--json` | Structured output; ordinary commands already use JSON. Disables protected terminal UI |
| `--non-interactive` | No protected terminal UI/hidden credential prompt |
| `--timeout SECONDS` | 1-300 seconds, default 60; does not extend task/session authority |
| `--operation-id ID` | Stable write/replay key; generated and returned if omitted |
| `--expected-rev N` | Explicit optimistic revision for applicable mutations |
| `--version` | Version, commit, API/schema contract versions |
| `help`, `--help`, leaf `--help` | Offline command discovery; no initialization/network |

User-owned Foundation files share `$HOME/.foundation`: `config`, runner `state`,
key `identity`, IPC `run`, `workspaces`, managed `bin` and `guidance`.
`config init` defaults to this state/workspace root and `run/identity.sock`;
explicit path flags remain supported. Existing legacy defaults require an
explicit stopped migration or `--config` pointing to the retained file, never
an automatic unenrolled reset. External tool environment settings are not
overridden; platform service/discovery files keep their standard locations.

JSON inputs use `--input FILE` or explicit `--input -` for bounded UTF-8 stdin.
Unknown fields and bodies over 1 MiB are rejected. Inputs may include
`operation_id`/`expected_revision`; conflicting global flags fail. Most readable
entity mutations let the CLI fetch/pin a missing revision once. Comment edits
require an explicit current revision. Preserve the original operation/proposal
after uncertainty; do not change its expected revision and replay blindly.

```json
{"ok":true,"data":{},"operation_id":"op_example","warnings":[]}
```

Errors have `ok:false` and `error:{code,message,retryable}`. Exit codes:
0 accepted, 2 invalid, 3 not found, 4 denied/trust failure, 5 conflict,
6 prerequisite, 7 unavailable, 8 provider failure, 9 internal/incompatible,
130 cancelled. Inspect the reported state; accepted/queued is not remote
completion. There is no force/skip-approval flag.

## Offline guide

Available in `agent-cli`, `fdn-service` and `agent-service` without
configuration, enrollment or network. The separate `agent-identity` binary has
its own offline `--help`; it does not expose `guide` commands:

- `agent-cli guide list`, `fdn-service guide list`, `agent-service guide list`
- `agent-cli guide show PATH`, `fdn-service guide show PATH`, `agent-service guide show PATH`
- `agent-cli guide install [--home DIR]`, `fdn-service guide install [--home DIR]`, `agent-service guide install [--home DIR]`

`list` returns the embedded file/skill catalog and source/version hashes.
`show` accepts catalog paths such as `DEPLOYMENT.md` or
`.agents/skills/foundation-work/SKILL.md`; it returns intentional documentation,
not arbitrary host files. `install` defaults to the actual account's HOME,
preserves unrelated instructions/skills, and creates discovery links for Codex/
portable agents and Copilot. Run it as the account that owns that home, not root
over another user's files. It never enrolls/authenticates or enables work.

## Foundation server and daemon

| Command | Required leaf arguments / effect |
| --- | --- |
| `fdn-service init` | `--state ABS --founder-request PUBLIC --ingress-socket ABS --join-socket ABS --ingress-group GID --account-socket ABS`; signed Founder preparation, private recoverable initialization, no TCP/bearer bootstrap |
| `fdn-service serve` | Unprivileged inner-TLS API/join Unix listeners plus private root-local maintenance; one server lock |
| `fdn-service doctor` | Read-only SQLite integrity/TLS pair/configuration checks |
| `fdn-service accounts serve` | `--input ROOT_CONFIG`; narrow root Linux Unix account provisioner, only the pinned server UID may call |
| `fdn-service gateway` | `--input ROOT_CONFIG`; forced SSH raw stdio selector, accepts only `SSH_ORIGINAL_COMMAND=fdn-api` or `fdn-join`; no arbitrary commands, PTY or forwarding |
| `fdn-service recover-founder` | `--input NEW_PUBLIC --reason REASON`; root-only private Unix maintenance; fresh keys for the sole existing legacy/restored/banned Founder retaining ID/history, no target/rotate flag and never an agent gateway/API route |
| `fdn-service tls-renew` | Stop server first; replace leaf/key under existing CA, resume an interrupted exact pair |
| `fdn-service backup-key` | `--output FILE`; create private age identity and return public recipient |
| `fdn-service backup` | `--output FILE --recipient age1...`; encrypted consistent SQLite/trust snapshot |
| `fdn-service restore` | `--input FILE --identity-file FILE --destination NEW_DIR`; verify complete archive, manifest schema 1/2 and supported SQL migration; quarantine all restored identity/owner/key epochs before publication, never overwrite or revive old credentials |
| `fdn-service service plan` | `--input OPTIONS`; render native Foundation definition without registering |
| `fdn-service service install` | `--input OPTIONS --apply`; explicit OS-administrator registration/start |
| `agent-service serve` | Native enrolled worker, guidance installation, one daemon, private state/tunnels/controller/session supervision |
| `agent-service status` | Read-only local daemon/work/journal/fence status and timestamped `health` observations |

## Worker setup and native services

| Command | Arguments / scope |
| --- | --- |
| `agent-cli config init` | `--identity-socket ABS [--state DIR --provider codex|copilot --workspace-root DIR --allow-workspace-tools]`; runner setup only, refuses existing configuration and installs own-account guidance |
| `agent-cli config show` | Non-secret runner configuration; never credential contents |
| `agent-cli config set` | `--input FILE`; documented patch below; credentials/identity are not editable |
| `agent-cli connect` | `[--migrate]`; attach the already activated local private identity proxy, check the matching agent and retain only public AgentID; migration needs prior Founder binding |
| `agent-cli status` | Authenticated identity plus local daemon/work/pending/fence state |
| `agent-cli doctor` | Read-only configuration, TLS/API authentication and service version |
| `agent-cli mode show` | Offline local mirror of authoritative `ai`/`manual`; work status includes the last Founder control reference |
| `agent-cli mode set` | `ai` or `manual`; authenticated Founder changes its own mode only; non-Founder callers are denied |
| `agent-cli preferences show` | Preferred provider/model |
| `agent-cli preferences set` | `--input FILE`, optional `provider`/`model`; provider change clears old model |
| `agent-cli service plan` | `--input OPTIONS`; render native worker definition |
| `agent-cli service install` | `--input OPTIONS --apply`; own-account native registration/start |
| `agent-cli service status` | Same local status, including `health`, without requiring network |

Configuration patches accept only `poll_seconds` (1-3600),
`invocation_timeout_seconds` (1-86400), `research_interval_hours` (12 or 24),
`automatic_provider_updates`, `allow_workspace_tools`, `provider_paths`,
`vnc_address`, `vnc_verified`, and `ssh`. Provider paths are absolute and only
name codex/copilot. Transport/provider-path changes require service restart.

Runner transport comes from `identity_socket`, not editable bearer credentials
or copied SSH keys. Legacy URL/CA/SSH configuration is not a v2 runtime fallback.
The protected identity service derives its pinned SSH account/host and TLS trust
from the approved public profile; its SSH commands select only Unix API/join
stdio targets, never local/remote TCP port forwarding.
See [DEPLOYMENT.md](DEPLOYMENT.md).

### Identity service (existing installing user)

| Command | Arguments / boundary |
| --- | --- |
| `agent-identity prepare` | Global `--config PRIVATE_CONFIG`, then `--name NAME --output PUBLIC_FILE`; retain private software SSH/authentication keypairs and export a signed public-only request; exact retry reuses the request |
| `agent-identity connect` | Global `--config PRIVATE_CONFIG`, then `--profile APPROVED_PUBLIC_PROFILE`; validate/store the provisioned public profile against this account's prepared keys |
| `agent-identity serve` | Global `--config PRIVATE_CONFIG`; maintain pinned SSH, activation, device-authenticated owner control and peer-checked local runner proxy |
| `agent-identity service plan` | `--input OPTIONS`; read-only current-user/two-process native-definition plan, no configuration, enrollment or root execution required |
| `agent-identity service install` | `--input OPTIONS --apply`; register both processes as the existing installing user, without sudo or new users/groups |
| `agent-identity service status` | `--input OPTIONS`; inspect that current user's exact two-service installation, no root or `--apply` |

The three `service` leaves take an integrity-protected options **file**, not stdin
or `--config`. Their `OPTIONS` is `platform.IdentityServiceOptions`, distinct
from the single-role options below:

- Selection: `os` (`linux`/`darwin`), `name`.
- Existing user: optional `user`, `uid`, `gid`, `home`, defaulting to the actual
  OS account, not `$USER`/`$HOME` claims or the enrolled agent name. Installation
  and status require these to match the current non-root user. No account/group
  creation, special group name, managed marker or no-login shell is required.
- Absolute paths: `identity_binary`, `worker_binary`, `identity_config`,
  `worker_config`, `state_dir`, `worker_state_dir`,
  `socket_dir`, `identity_definition`, `worker_definition`.

Prepare both 0600 configurations and distinct 0700 identity/runner state under
the existing user's HOME. IPC is a separate 0700 directory with a 0600 socket.
The existing HOME mode, groups, shell and account are not changed. Legacy
`identity_user`/`identity_uid`/`identity_gid`/`identity_home` and corresponding
`worker_*` account selectors are rejected with explicit migration guidance.
The installer never moves keys or state across users automatically.
Paired binaries must be protected `agent-identity`/`agent-service` in the same
binary directory. Native definition paths and manager details are in
[DEPLOYMENT.md](DEPLOYMENT.md#native-identity-and-runner-services).

`agent-identity --help` is offline. There is no identity `config init` leaf:
authorized setup supplies a user-owned 0600 configuration, separate 0700 key
state and an absolute socket in a separate user-owned 0700 IPC directory.
Private configuration `worker_uid`/`worker_gid` may be omitted/zero to use the
current user; explicit foreign values require migration. Native execution
rejects root or a different runner UID/GID, not the same user. Stop the
identity service before offline `prepare`/`connect`. Its proxy does not export
private keys, signing operations, owner control or certificates over IPC.
The user nevertheless owns those private files; process separation is not a
security boundary against same-user tools.
The public profile contains `identity`, `url`, `ca`, `ssh_host`, `ssh_port` and
`known_hosts`; approve its integrity even though its contents are not secret.

New identity registration derives the Foundation SSH username from the agent
name: lowercase, replace runs outside ASCII `a-z`, `0-9`, `_`, `-` with `-`,
and remove outer `-`. The single-line name must yield a non-root username
matching `[a-z_][a-z0-9_-]{0,31}`; invalid results and collisions fail rather
than truncate, hash or adopt an existing account. `Agent1` uses `agent1` on
Foundation even when the existing local user is `alice`. The profile's
`identity.ssh_user` reports the reserved login. Existing account names/UIDs
and fresh-key recovery bindings do not change. OS access remains independent
of the display name and Foundation project authority.

There is **no `--rotate` flag**. For explicit fresh-key recovery, stop the
service, preserve its old private StateDir and **all runner state/journals**,
then choose a new empty user-owned 0700 StateDir inside the **same user's HOME**.
Keep the private config file path/IPC and all runner paths stable;
update only the intended `state_dir` binding in that config and service OPTIONS.
Run private `prepare` in the new state, obtain Founder/root recovery, connect
its newly approved public profile and restart the named native services. Normal
prepare retry/reconnect/restart reuses the retained identity, never auto-rotates
keys. See [DEPLOYMENT.md](DEPLOYMENT.md#diagnose-identity-state-and-recover).

### Existing single-role native service options

The `fdn-service service` and `agent-cli service` commands retain their separate
`platform.Options` contract; do not pass paired identity-service options here.
Fields: `os` (linux/darwin), `role` (foundation/agent), absolute
`binary`, `config_file`, `state_dir`, `home`, `definition`, and `user` (existing
installing user for workers; dedicated unprivileged server user for Foundation).
Native Linux Foundation installation also requires `ingress_group` (the approved
ingress-only group name) and `ingress_dirs` (the one/two actual API/join parent
directories). They render `SupplementaryGroups` and the additional
`ReadWritePaths` needed by `ProtectSystem=strict`; init/serve require actual
current supplementary membership, distinct from the primary database group.
User-service registration needs the correct Linux user manager/bus environment.
Native manager stop/restart/uninstall is an explicit OS operation, not an
unimplemented `agent-cli service stop` command.

### Retired bearer enrollment paths

`agent-cli invitation create` and `fdn-service bootstrap` are not supported
enrollment paths and cannot issue bearer enrollment material. Legacy
`connect --invitation-file` / `connect --name`, and server initialization with
`--listen` / `--bootstrap-file`, cannot establish a v2 identity. Use signed
public-key registration and the protected local proxy, not compatibility flags
or an older server/client fallback.

## Identity and projects

| Command | Arguments/input |
| --- | --- |
| `agent-cli agent list` | Active identity checks; shared registry including concise public status |
| `agent-cli agent show` | `AGENT`; current role, authoritative mode/revision, Founder request attribution and public status |
| `agent-cli agent status` | `--input FILE`: required `text` (single line, max160 characters), intentional non-secret self-publication; explicit empty text restores automatic activity; no target/priority/time/mode override |
| `agent-cli agent diagnose` | `AGENT_ID [--refresh]`; default is saved read-only `AgentDiagnostics` for Founder/self; `--refresh` submits a durable Founder-only diagnostic `AgentOperation` |
| `agent-cli agent repair` | `AGENT_ID --input FILE`; Founder-only `SessionRecoveryRequest` for one exact closed session and fresh observed fence SHA256; returns `AgentOperation`, never accepts caller cleanup proof |
| `agent-cli agent operation` | Exactly `REQUEST_ID` from `data.id`; bodyless GET of the common diagnostic-refresh/session-recovery state and safe result, visible only to its requester Founder or target |
| `agent-cli agent mode` | `AGENT --input FILE`: mode ai/manual, reason and revision; Founder alone pauses/resumes any target |
| `agent-cli agent request-sign-in` | `AGENT --input FILE`: `provider` (codex/copilot), required `reason`, optional `project_id`; Founder-only protected refresh, valid for 15 minutes; no login, inference or mode change |
| `agent-cli agent revoke` | `AGENT`; Founder only, not Founder itself; duplicate-owner auto-ban still includes Founder |
| `agent-cli agent register` | `--input SIGNED_PUBLIC [--existing-agent AGENT] [--output PUBLIC_PROFILE]`; Founder-only new registration or explicit binding of the designated original existing non-Founder |
| `agent-cli agent recover` | `AGENT --input NEW_SIGNED_PUBLIC --reason REASON [--output PUBLIC_PROFILE]`; Founder-only non-Founder recovery with fresh keypairs/generation, not task-run recovery |
| `agent-cli agent identity` | Current caller's public identity/account/key-generation/owner state; no private material |
| `agent-cli project list` | Shared registry |
| `agent-cli project show` | `PROJECT` |
| `agent-cli project create` | `--input FILE`: `name`, `description`; consumes specific creation grant for non-Founder |
| `agent-cli project change` | `PROJECT --input FILE`: reason + optional name/description/archived; current authority, advances epoch |
| `agent-cli project delegate` | `PROJECT --input FILE`: `agent_id`, `revoke`; grant Founder-only, revoke project authority |
| `agent-cli project authorize` | `--input FILE`: `agent_id`, `revoke`; Founder-issued specific creation authorization |

## Founder software upgrades

| Command | Arguments / effect |
| --- | --- |
| `agent-cli upgrade check` | `[--platform linux-amd64\|linux-arm64\|darwin-arm64] [--input TARGETS]`; Founder-only latest published release and optional actual native inventory; no mode/package/service changes |
| `agent-cli upgrade plan` | `--input TARGETS --output PLAN`; private exact target/source/nonce/hash plan and off-host `PLAN.agekey`; no installation; output must be new |
| `agent-cli upgrade apply` | `--input PLAN`; explicit Founder authorization, pause, encrypted cold backups, Foundation-first native rollout and actual verification; leaves workers manual |
| `agent-cli upgrade status` | `--input PLAN`; original Founder's retained local evidence and native receipts, including when Foundation is offline; no installation/renewed authority |
| `agent-cli upgrade-lease` | Internal native-helper command, bounded protobuf stdin/stdout only; exact issuer/target requests a fresh server nonce proof; no installer/inference |
| `agent-service upgrade-helper` | `--policy FILE`; administrator-provisioned worker updater, bounded protobuf stdin/stdout only |
| `fdn-service upgrade-helper` | `--policy FILE`; administrator-provisioned Foundation updater, bounded protobuf stdin/stdout only; not the ordinary SSH agent gateway |

`check` uses only the pinned public `thtpark/fdn` stable GitHub release channel,
not the current working tree, branch HEAD, provider updates or expiring Actions
artifacts. A missing release, unavailable platform/digest, API/rate-limit failure,
same-version/different-commit or development/unknown-source installation is an
explicit error, not "up to date". Discovery does not grant upgrade permission.
Without TARGETS, `check` reports the latest native bundle, Foundation's actual
version/source and the local CLI development stamp. TARGETS adds actual remote
helper observations and per-target `upgrade_available`.

TARGETS is a private JSON file with `targets` (1-32 exact entries): `role`
(`foundation`/`agent`), `target_id` (Foundation instance ID or stable agent ID),
`platform`, `ssh` (host, user, port, identity_file, known_hosts_file), fixed
`helper` binary, `policy_file`, and `sudo` boolean. Paths are absolute.
This is separate, administrator-approved operator SSH access, not an SSH
command sent through `fdn-api`, a new root project permission, or a transferred
provider/agent key. For IAP, use explicitly opened loopback-to-SSH tunnels;
native operator clients ignore SSH config, agents, ProxyCommand/ProxyJump and
all inherited forwarding, and require independent host pins.

Planning stores complete version/commit/platform/ZIP digest, native policy hash,
current target key generation/source, fresh nonce, fixed challenge expiry and
public age recipient. Keep both PLAN and `PLAN.agekey` owner-only and off targets.
The key file is never transmitted. Plan/apply never choose another version or
renew a challenge after expiry: prepare a new plan before any native admission.
An already-current target needs no upgrade and must be omitted from a rollout.
All targets in one plan must resolve to one release version and source commit.

`apply` authenticates Founder on the server, pauses affected agents (all agents
for a Foundation upgrade), signs immutable 15-minute exact permits and persists
submission before native effects. Each helper independently validates its pinned
Foundation CA, exact current challenge/policy/source and a fresh 30-second
nonce-bound server start lease. Recovery/revocation, changed pause/generation,
active ownership or assistance before native start denies the lease.
Once the exact maintenance job is admitted and services are stopped, completion
uses its durable receipt and original fixed permit expiry; it is not a new
revocable session or permission to resume work.

Workers prepare/stop before Foundation; package installation/start/verification
is Foundation first, then workers. Pending/uncertain invocations/reports block
admission; existing session/text/work fences are retained, not force-cleared.
Native phases are `planned`, `stopping`, `prepared`, `installing`, `verified`;
an interrupted mutating phase without a live helper is `operator-required`
with its original phase in `code`. Repeating `apply` is rejected; use `status`
and inspect actual effects. A binary rollback is not a database rollback.
No automatic inference, provider login/update, key rotation, disk wipe, release
publication, native policy/sudoers installation or cloud operation follows.

First install an upgrade-capable Foundation and target service binary through
an authorized operator, and provision each native policy once. Existing releases
without these routes/helpers cannot bootstrap themselves. Detailed policy,
operator-target and service prerequisites are in
[DEPLOYMENT.md](DEPLOYMENT.md#founder-controlled-native-upgrades).

## Concise public agent status

Every active agent can see the non-secret `status` on `agent list`/`agent show`
and its own `status`. It contains `text`, `source` (controller/agent), `state`,
server publication `updated_at`, observation `observed_at`, and explicit `stale`.
Examples: “Reviewing task scope”, “Replying to a message”, “Waiting for
clarification”, “Paused by Founder”. Automatic wording uses only bounded
controller activity, never private message text, task titles, paths or transcripts,
and requires no model call. Local `work status` `activity_text` is the current
safe controller description; public publication can intentionally lag it.

Normal changes publish at most once per **60 seconds**. Identical statuses do not
bump publication time; rapid changes coalesce to the latest pending candidate.
Meaningful blocked/paused/recovery transitions can publish immediately. Bookkeeping
polls and idle heartbeats are not activity changes or business progress. Stale
status remains explicitly stale, never default healthy.

During authorized tool-enabled work an agent may intentionally publish its own
concise, non-secret description of the current activity:

```json
{"text":"Running parser tests"}
```

```sh
agent-cli --operation-id current-activity-001 agent status --input FILE
```

`POST /v1/agents/{self}/status` accepts required `text` plus command metadata only.
Only the same active enrolled agent may write; even Founder cannot impersonate
another agent's status. Empty `text` clears the authored note. Notes are scoped
to current meaningful activity and yield to automatic pause/block/completion
changes, so an old “Testing...” note cannot hide recovery or a pause. The result
contains public `status`, `pending`, and `next_update_at` for coalesced updates.
Reuse the same operation/input after uncertainty; do not repeatedly publish
unchanged text or start inference merely to refresh status. Only current latest
status/pending state is retained, not unbounded status history or event spam.

Public status is deliberate publication, not private knowledge, a verified health
claim, work completion, read acknowledgement, approval, consent or mode authority.
No credentials, private paths, raw provider output, private message references or
unpublished knowledge belong in authored text. General diagnostics remain
Founder/self-only despite this intentionally shared status surface.

## Read-only worker and reply diagnostics

```sh
agent-cli --config /PRIVATE/agent.json agent diagnose AGENT_ID
agent-cli --config /PRIVATE/agent.json message diagnose MESSAGE_ID
```

Without `--refresh`, agent diagnosis accepts exactly one ID and no input; message
diagnosis accepts exactly one ID and no leaf flags/input. These bodyless GETs use
the existing authenticated identity proxy and JSON/error envelope; they retrieve
saved observations, not a fresh provider probe, login, session, inference, retry,
resume or recovery. The explicit Founder refresh below is a separate durable write.
General agent diagnosis is Founder/self only; project delegation alone does not
grant it. Message diagnosis is always sender/recipient-private, without a Founder
bypass. Unrelated private message references, journals, raw errors, paths and
credentials must not appear in diagnostics.

`MessageDiagnostics.state` is the observed reply lifecycle; `stage` is its
lower-level phase. For example, `state:"generating"` with `stage:"invoking"`
means an invocation is still pending, not an authoritative receipt or completed
AI reply. `state:"blocked"` can have `stage:"recipient"`. Inspect actual private
message/receipt evidence separately; do not treat a phase as lifecycle completion.

`work status`, `service status` and `agent-service status` include a local `health`
snapshot: effective mode/provider/model, configured `preferred_provider` and
timestamped authentication observation, phase/action/start/deadline, separate
loop/progress clocks, pending report count, safe blockers and timestamped
component problem codes. Idle polling/heartbeats are not task progress.
Unknown/missing/stale data includes a reason and next inspection step, not a
healthy fallback. A successful login exit is not authentication readiness.
A worker can publish `invoking` with the matching action/deadline before the
provider invocation returns; that heartbeat does not assert AI completion.

Workers optionally send version-1 health on the existing 30-second heartbeat.
The server watchdog needs no AI or worker cooperation and also monitors manual
Founder. Defaults are 90 seconds without heartbeat/report, 30 seconds beyond a
phase deadline, and 120 seconds for an eligible unexplained queued reply.
Manual pause, no requested response, ordinary idle and work inside its declared
deadline are not stuck failures. A Foundation outage still needs native/outside
monitoring. See [SCHEMA.md](SCHEMA.md#observed-worker-health-and-reply-diagnostics)
and [DEPLOYMENT.md](DEPLOYMENT.md#read-only-worker-and-reply-diagnostics).

## Founder diagnostic refresh and exact closed-session recovery

Only the authenticated Founder may submit these controls. `agent recover` remains
fresh-key identity recovery; `agent repair` is not identity recovery, journal or
inference retry, a native service restart, or arbitrary process control.

```sh
agent-cli --config /PRIVATE/founder-agent.json \
  --operation-id diagnostic-refresh-001 agent diagnose AGENT_ID --refresh
agent-cli --config /PRIVATE/founder-agent.json agent operation REFRESH_REQUEST_ID
```

`--refresh` sends `DiagnosticRefreshRequest` through the normal durable CLI write
proposal to `POST /v1/agents/{id}/diagnostics`, with command metadata and fixed
nonsecret reason `Founder requested fresh diagnostics`. The returned operation's
`id` (the envelope's `data.id`) is `REFRESH_REQUEST_ID`; it is
different from the stable submission `operation_id`. Reuse the original operation
ID and exact command/input after an uncertain write. There is no `--wait`,
provider, inference or shell option, nor a separate inspect command.

`agent operation REQUEST_ID` uses a bodyless
`GET /v1/agent-operations/{id}` for both kinds of operation:

| State | Meaning |
| --- | --- |
| `queued` | Durably accepted, not yet checked; no fresh observation or recovery claim |
| `checking` | Target has started checking; not successful completion |
| `refreshed` | A bounded read-only inspection was recorded; examine server-anchored `captured_at`, separate target observation times and the result code |
| `recovered` | The exact eligible session fence was retired with a durable local receipt; not proof work progressed or finished |
| `rejected` | Current identity/generation, session, busy-slot or fence precondition failed; no requested recovery |
| `operator-required` | Proof is absent/ambiguous or native action is needed; retained evidence must not be forced clear |
| `expired` | The fixed 900-second operation lifespan elapsed; no new check/recovery is authorized |

A queued offline/dead target is not fresh success. A dedicated non-inference
worker control loop can inspect while paused, busy or fenced, but cannot revive
a dead daemon. The fixed 15 minutes is the operation's immutable lifespan, not
an extension of sign-in notices, provider authentication or live sessions.

Operation `created_at`/`expires_at` use the server clock. `server_observed_at` is
the server's current-time projection on lookup; reading it does not renew expiry
or change the immutable operation fingerprint. `result.captured_at` is a retained
conservative server-time capture bound, derived from a fresh authenticated server
response and monotonic request-start/round-trip timing, not target UTC. Use these
server-clock fields for operation lifespan/capture freshness, not the target's
`result.observation.observed_at`, provider `observed_at` or health timestamps.
Those target-clock observations can be ahead/behind the server and remain visible
as reported; they cannot extend the deadline or turn an expired operation fresh.

Provider observations remain separate from the current health/phase snapshot.
`credentials-present-unverified` is not vendor usability; even `ready` is a
read-only observation, not a successful paid inference or a promise of progress.

After a `refreshed` result, inspect
`data.result.observation.session_fence`. Copy its exact `session_id` and
`fingerprint` into an owner-restricted repair input as `session_id` and
`expected_fence`. Replace the placeholders below with the reported values;
`expected_fence` must be the canonical lowercase 64-character SHA256, not a
PID/name/path or a guessed token. If the fence is absent, changed, active or
unproven, do not manufacture proof.

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

Repair sends `SessionRecoveryRequest` (command metadata plus those three fields)
to `POST /v1/agents/{id}/repairs`. No `--acknowledge-stopped` flag or JSON
`cleanup_proven`, proof, PID/process arguments, workspace path, credentials,
result or protected identity fields are accepted. The target independently
requires the exact server-confirmed terminal session and exact current raw-fence
SHA256 under the shared `work.lock`, plus its existing durable native
`CleanupProven` evidence. Mutation and final local receipt are atomic; lost
network acknowledgements replay the same result without clearing a second fence.

The initial implementation refuses old `CleanupProven:false`/legacy fences as
`operator-required`; absence of a process name/PID is not cleanup proof. It never
stops arbitrary processes or rewrites/clears retained evidence to manufacture
eligibility. The original Agent1 fence may still need explicit native operator
inspection and the existing exact local stopped-process acknowledgement.
A different generation, changed fence, active session or occupied slot cannot be
forced through. No automatic Agent1 recovery is promised.

Successful repair changes no global mode, task approval, retry/spending authority,
sign-in notice or assistance grant, and opens no session. Existing approved
`ai` work may resume when all its independent fences permit; a Founder pause
remains paused. Inspect the operation, then diagnose again for actual progress.
Neither `recovered` nor `automation_may_resume` proves work ran.

Operation version 1 binds requester/target key generations. Controls/results are
protected server-only `agent-operation` message data with
`response_requested:false`, excluded from the ordinary inbox. Ordinary message
send/receipt cannot forge or settle them. Only the requester Founder and exact
target can read their own operation, with no general access to unrelated private
messages or sessions. Results contain safe code, mode, automation eligibility,
health, provider/fence observations and slot state only: no raw stdout/errors,
process arguments, credentials, transcripts or workspace paths. The private
identity proxy narrowly forwards the exact control routes; it does not grant
authority or forward caller-supplied identity headers.

## Tasks, approvals and run projections

| Command | Arguments/input |
| --- | --- |
| `agent-cli task create` | `--input FILE`: project_id, title, description, acceptance_criteria, optional parent_id/repetitive/priority/importance |
| `agent-cli task list` | `[--project ID --assignee ID --status STATUS --limit N --cursor CURSOR --include-archived]` |
| `agent-cli task show` | `TASK`; current protected/ordinary context |
| `agent-cli task change` | `TASK --input FILE`; typed mutation kinds below, reason required |
| `agent-cli task history` | `TASK`; latest three retained snapshots with action/reason |
| `agent-cli task runs` | `TASK`; execution/verification kinds, actor/provider and evidence projections, not a claim/approval shortcut |

Every change supplies `kind`, nonempty `reason`, operation metadata, and only
fields allowed for that kind. The API also enforces all authority/preconditions.

| Kind | Allowed decision fields / gate |
| --- | --- |
| `edit` | title, description, acceptance_criteria, priority, importance; execution-affecting change invalidates old approval |
| `status` | status; cannot synthesize in-progress/completed/done |
| `assign` | agent_id, provider, model; project authority; changed worker clears workspace |
| `provider` | provider, model; explicit provider required for a required model |
| `workspace` | workspace_ref; assignee only, opaque authorized local binding reference |
| `policy` | policy object; narrowing may be proposed, weakening requires authority |
| `retry-policy` | retry_policy object; project authority, invalidates readiness |
| `approve` | spec_generation; eligible current authority and complete readiness context |
| `withdraw` | Withdraw current decisions; stop-request active work rather than stealing ownership |
| `verify` | spec_generation, result_generation, decision accept/rework; current completed evidence and eligible verifier |
| `retry-approve` | spec_generation, run_id, attempt; exact prior run and next otherwise-unsafe attempt |
| `clarity` | spec_generation, answer_ids; assignee's current understanding after recorded blockers resolved |
| `waiting` | spec_generation, waiting object; stopped assignee with published questions |
| `revert` | history_sequence; restore retained ordinary content, never resurrect authority |
| `archive` | Only stopped terminal outcomes; preserve status/history |
| `stop` | Worker/project authority requests current operations stop |
| `release` | run_id, stopped_acknowledged=true; project authority's explicit stopped-process confirmation, never timeout takeover |

Policy object: `required_actors` (eligible allowlist, not quorum),
`independent_readiness`, `independent_verification`.
Retry object: `max_retries` (0-10), `backoff_seconds` (1-86400).
Waiting object: `kind:"clarification"`, reason, requested_ids, question_ids;
server stamps since. All requested respondents must be literally mentioned in
the worker's referenced comment bodies.

Example ordinary edit:

```json
{"kind":"edit","reason":"Incorporated the agreed scope","description":"Precise updated instructions","acceptance_criteria":"Measurable checks"}
```

Example current signoff (substitute the actual generation):

```json
{"kind":"approve","reason":"Reviewed the exact current specification","spec_generation":4}
```

## Discussion, attention and watch

| Command | Arguments/input |
| --- | --- |
| `agent-cli comment add` | `TASK --input FILE`: body, optional parent_id for a same-task comment reply |
| `agent-cli comment edit` | `COMMENT --input FILE`: body + explicit current revision; creator only |
| `agent-cli worklog add` | `TASK --input FILE`: intentional body; local/private logs are not auto-published |
| `agent-cli discussion list` | `TASK [--cursor CURSOR --limit N]`; comments and worklogs |
| `agent-cli mention list` | `[--cursor CURSOR --limit N]`; current recipient's pending mentions |
| `agent-cli mention acknowledge` | `COMMENT --input FILE`: exact generation, state handled/deferred |
| `agent-cli watch` | `[--project ID --task ID --since CURSOR --limit N --once]`; bounded pages or NDJSON stream/checkpoint |

Literal mentions accept `@{Agent1}` by agent name (case-insensitive), or
`@{AGENT_ID}` with the complete actual ID substituted. Names with spaces work
inside the braces. Unknown/inactive names fail as not-found and ambiguous names
fail as conflict; use the exact ID to disambiguate legacy duplicate names.
The body stays as authored; returned `mentions` contains sorted, deduplicated
AgentIDs, excluding self. Exact operation replay retains the original recipients.
Comments create attention; worklogs retain mention metadata without attention.
Removed/edited mentions stale old-generation receipts. Mentions never confer
authority or assignment. Automatic mention replies use the recipient's preferred
provider/model, not the task assignee's required execution provider/model.
Replies remain tool-disabled, attach to the source comment, and mark only its
exact current body generation handled after publication. Assigned work and
verification retain priority; Founder pause and recovery fences still block
inference. Cursors are actor/query-bound; pages are
limited to 1-200. Watch delivery is at least once; inspect sequence/cursor rather
than assuming exactly-once processing.

## Displays, browser VNC and human-help requests

`agent-cli screen list` reports the current runner's available verified screens,
not cloud hardware flags or screenshots. Headless agents publish an empty screen
inventory and cannot request/open VNC. Configure only actual physical/virtual
screens and a verified authenticated loopback backend. Each screen has a stable
public ID and scope `screen` (only that screen) or `desktop` (whole host fallback),
geometry and configured application IDs; private paths/addresses remain local.

`agent-cli screen launch SCREEN --application APP` explicitly launches the
configured application under the runner account on that screen, held in the
foreground until app exit or the command timeout/cancellation; cleanup is joined
before its work slot is released. It accepts an application ID, not arbitrary
shell commands, and respects existing invocation/session fences. For VNC help,
prefer binding application_id in the grant/session so app preparation and cleanup
share the live session's lifetime instead of running a separate CLI launcher. Use an app per screen when supported;
unsupported placement must not be represented as screen isolation.

`agent-cli assistance request --input FILE` is the target agent asking for help.
Input: mode tmux/vnc, purpose authentication/terminal, reason, optional provider,
project_id/task_id, and for VNC screen_id/application_id plus desktop_fallback.
The server sends protected private notices to active Founder and current relevant
delegates. This does not create a grant, open a viewer, launch an app or start
login/inference. General human desktop help uses purpose terminal; headless
provider sign-in normally uses tmux/device-code instead of VNC.

Founder issues the matching `assistance grant` with screen_id, application_id
and explicit desktop_fallback if the inventory reports whole-desktop scope.
Grant/session checks prevent screen/app swaps or silent broadening; all current
identity, project, expiry and revocation checks still apply.

VNC `assistance open`/`session open` default to `--viewer browser`; `--viewer
native` retains the trusted native viewer. The browser helper needs an absolute
trusted installed noVNC assets directory (`--assets-dir`, default
`/usr/share/novnc`), not a CDN or downloaded remote script. It binds only literal
loopback (`--listen`, default `127.0.0.1:0`). On a headless requester, choose a
fixed loopback port and SSH-forward that same port to the human's localhost;
open the helper's human-terminal URL locally. The target RFB stream still uses
the existing authenticated SSH/mTLS relay; no public VNC/API listener is needed.
The helper's token-bearing bootstrap URL is session access material, not a public
provider verification link: keep it in the authorized human terminal, never a
Foundation message/model log. No clipboard transfer is permitted.

## Persistent assistance and protected sessions

Every non-Founder target is unattended. Founder issues authorization once;
delegates may be AI agents, not substitute humans. All new sessions to a
non-Founder target require a matching persistent assistance grant.

| Command | Arguments/input |
| --- | --- |
| `agent-cli assistance grant` | `--input FILE`: requester_id, target_id, mode tmux/vnc, purpose authentication/terminal, optional provider/project_id, reason; Founder-only issuance |
| `agent-cli assistance list` | Up to 200 active grants visible to the parties or Founder |
| `agent-cli assistance show` | `GRANT`; immutable scope, active state, revision and issuance attribution |
| `agent-cli assistance revoke` | `GRANT --input FILE`: reason/current revision; Founder-only, closes live access on recheck |
| `agent-cli assistance open` | `GRANT [--provider NAME --ttl 60..900]`; explicit requester launch in a protected terminal under retained authorization |

Grants have no timed expiry and survive process restarts/disconnects until
revoked; closing/expiring a live session does not consume the grant. Global
grants are explicit Founder authority for that requester/target only, including
bootstrap sign-in before any project exists. Project-scoped grants additionally
require the requester's current project authority. A blank provider restriction
covers supported authentication providers; it is not arbitrary terminal access.
Only Founder issues/revokes grants; assistance never grants mode/task/spending
authority. The target daemon prepares the bounded connection unattended after
explicit requester open. A reconnect uses the same grant without reapproval.
Delegates can coordinate with typed APIs. Provider sign-in screens and public
verification links may be displayed to the authorized human; public generic URLs
and non-secret instructions may be relayed. Password/token/cookie inputs, one-time
codes and secret-bearing sign-in state must not enter an AI context.

| Command | Arguments/input |
| --- | --- |
| `agent-cli message send` | `--input FILE`: recipient_id, body; optional origin/kind/urgent/response_requested |
| `agent-cli message show` | `MESSAGE`; sender/recipient only; peek does not mark read |
| `agent-cli message read` | Exactly `MESSAGE`; recipient explicitly acknowledges finished reading; first server UTC `read_at` is immutable; no timestamp/leaf flags or disposition change |
| `agent-cli message diagnose` | Exactly `MESSAGE_ID`; read-only `MessageDiagnostics` from `GET /v1/messages/{id}/diagnostics`, private to sender/recipient; `state` is observed lifecycle (not-requested, queued/shared-slot waiting, generating, blocked, awaiting-delivery, handled), `stage` is lower-level phase; not proof of authoritative receipt or AI completion |
| `agent-cli message inbox` | `[--cursor CURSOR --limit N --urgent]`; recipient's pending messages |
| `agent-cli message receipt` | `MESSAGE --input FILE`: state handled/deferred/stale, or pending only from deferred; does not acknowledge reading |

Reading is an **explicit recipient assertion**, not inferred from delivery,
listing, peeking with `message show`, diagnostics, processing, a reply or a
`handled` disposition. To deliberately acknowledge finished reading:

```sh
agent-cli --operation-id message-read-001 message read MESSAGE_ID
```

This sends `POST /v1/messages/{id}/read` with only command metadata. The server
stamps the first accepted acknowledgement in `Message.read_at` (UTC Unix seconds);
clients cannot supply/backdate/reset it. Replay the same operation after a lost
acknowledgement. Re-reading with another operation keeps the first timestamp.
Only the recipient may write it; sender/recipient can inspect it with `message
show` or `message diagnose`. An absent timestamp means **no explicit receipt**,
not proof the message was unseen. No existing handled messages are backfilled.

Read receipts are independent of disposition and its revision: marking read
leaves a requested response pending, changes no task/approval/mode/session or
control, and does not invalidate an already prepared `message receipt`. Reading
an expired notice does not reopen it or renew its expiry. Protected runtime
operations still require their dedicated protocol, not ordinary message reads.

In a controller-managed direct-message text call the recipient model may
explicitly choose optional `message_read:true` after it finishes reading that
specific message. Omission/false is a peek without acknowledgement, even if a
reply is sent. Only that explicit validated choice emits the durable read
operation; it is forbidden in other phases. A pending read acknowledgement
replays the prepared receipt/reply without another inference. Upgrade Foundation
and the matching identity proxy before enabling new workers that use this API.
| `agent-cli session request` | `--input FILE`: grant_id, target_id, matching project_id/scope, optional task_id, mode, purpose, provider, reason, expires_at |
| `agent-cli session list` | Current party's unexpired pending/consented/active sessions |
| `agent-cli session show` | `SESSION`; party-only redacted state |
| `agent-cli session change` | `SESSION --input FILE`: action consent/deny/open/active/revoke/close under exact party/current revision rules |
| `agent-cli session open` | `SESSION`; explicit requester action + protected human terminal attachment, no `--json`/`--non-interactive` |

Message origin defaults to human (a declared label, not attestation); kind
defaults to direct. Kinds authentication/permission/session cannot request automatic
responses. Server-created protected health alerts/recovery notices also have
`response_requested:false`; ordinary `message send` cannot forge them. Their
durable incident/cause/action deduplication and recovery notifications are
independent of 15-minute authentication notices and never renew sign-in notices
or start login/session/inference/retry/resume/recovery. A protected handoff body
may contain public generic verification
URLs, non-secret instructions and references/actions/expiry. It must not contain
a token, one-time code, private/secret-bearing authentication URL or an unredacted
credential transcript/screenshot. Authorized human-visible sign-in screens/links
are not themselves a privacy violation.

Autonomous targets need a current matching Founder-issued grant, not a local
human consent decision, plus explicit requester open. Founder targets may still
use the existing requested/consent flow when no grant is supplied. Mode is
tmux/vnc; purpose terminal/authentication. Authentication
requires codex/copilot. Set `expires_at` to an actual UTC Unix deadline 60-900
seconds ahead; never reuse a stale example deadline. For task-scoped sessions,
the task must connect project and target assignee.

Tmux authentication launches the selected login command. VNC requires a verified
authenticated loopback backend and trusted native `vncviewer`; clipboard
send/receive are disabled. Neither path unlocks a desktop or transfers the
requester's Foundation identity. Ctrl-] closes terminal attachment.

## Offline knowledge, inventory and workspace authorization

| Command | Arguments/input |
| --- | --- |
| `agent-cli knowledge put` | `--input FILE`: kind memory/skill/knowledge, safe name, content, expected_revision (0 on create), archived, selected |
| `agent-cli knowledge list` | `[--include-archived]` |
| `agent-cli knowledge show` | `KIND:NAME` |
| `agent-cli knowledge history` | `KIND:NAME`; last three versions |
| `agent-cli inventory refresh` | Native read-only observations; no scanning networks or AI inference |
| `agent-cli inventory show` | Last private inventory; unknown != absent/permission denied |
| `agent-cli workspace bind` | `--path OWNED_DIR --allow-tools [--timeout SECONDS]`; explicit local permission |
| `agent-cli workspace list` | Private paths/device/inode refs; do not publish |
| `agent-cli workspace enable` | `REF`; explicit local re-enable, never a new ref to bypass disablement |
| `agent-cli workspace disable` | `REF`; prevents future/bounded running admission |

Archived knowledge cannot be selected. Selected private knowledge has a 64 KiB
prompt budget and is not automatically published. Bindings require ownership,
no group/other write access, stable directory identity, and exclusion of private
Foundation state/configuration. Assignment alone cannot grant tools.

## Providers and shared controller

| Command | Arguments / boundary |
| --- | --- |
| `agent-cli provider status` | `[--provider codex|copilot]`; bounded version/capability/auth probes without inference |
| `agent-cli provider install` | `--provider NAME --version EXACT`; staged integrity/signature/capability checks under the shared local slot |
| `agent-cli provider update` | `[--provider NAME]`; explicit latest resolution, retains previous version on staging failure |
| `agent-cli provider login` | `[--provider NAME]`; actual human TTY only; probes login help and prefers supported device-code/headless flow, otherwise native login; no structured/captured credentials |
| `agent-cli provider link` | Repair/install this account's stable codex/copilot launchers and login PATH without downloading, authenticating or running a provider |
| `agent-cli provider exec` | `--provider NAME -- NATIVE_ARGUMENTS`; resolve the active managed version, preserve native output/exit status; interactive commands require a human terminal and the shared local slot |
| `agent-cli work prompt` | Shared prompt and version-matched operating guidance; no task claim or inference |
| `agent-cli work status` | Read-only local state, live `health` phase/deadline/progress/authentication observations and safe timestamped component diagnostics, blockers, pending invocations/reports, session fence and durable authentication request/delivery references |
| `agent-cli work run` | One bounded manual controller step; same prompt/authority/slot as service |
| `agent-cli work recover` | Exactly one `--journal INVOCATION` or `--session SESSION`, plus `--acknowledge-stopped` after confirming owned processes stopped; an exact rejected text journal authorizes a fresh review, never an execution retry |

Tool-disabled scope, acceptance and preflight replies require `clear`, `blocked`
or `paused`; `reviewed` is not a preparation outcome. Each prompt pins the current
phase and its allowed outcomes. A fully returned but phase-invalid text reply is
retained as `reply-rejected` with an atomic semantic-action fence, rather than
replayed forever at global admission. `work status` exposes bounded
`text_response_blocks` summaries with the exact journal/phase/task references.
Unrelated messages and work can continue, but the rejected action never starts
another model call merely because of polling, restart or a new `work run`
operation ID. Changed specification/discussion/attention context is distinct
work; do not edit context just to bypass a response fence.

A protected `provider-response` permission notice reaches Founder and relevant
current delegates without inference. After inspecting the private retained reply
and confirming its owned processes stopped, an authorized native operator may use
`work recover --journal EXACT_INVOCATION --acknowledge-stopped` to atomically
retain the old reply and authorize a fresh text review. The controller never
coerces `reviewed` into `clear`, invents approval, resets execution retries or
remotely accepts a stopped-process assertion. Uncertain invocations and possibly
partially applied decisions remain fenced under their existing recovery rules.

When an ai-mode or explicitly invoked controller observes `login-required` or
`credentials-rejected`, it sends a protected `authentication` message to the
active Founder and, for task-scoped work, current project delegates. This
deterministic handoff works before onboarding and requires no provider inference.
Founder can read it with `message inbox`/`message show MESSAGE` even in manual
mode. It requests no automatic AI response and creates/opens/consents to no
remote session.

`work status` exposes `authentication_requests`: request/provider/scope IDs,
creation/expiry/state, recipient/operation/message references, confirmed delivery
or pending error, refresh/supersession references, and resolution time. States are
`delivery-pending`, `requested`, `expired` and `resolved`. Requests expire exactly
15 minutes after creation, including pending deliveries, and do not automatically
renew on polling alone. New requested work has its own durable `action_key`:
assigned task context, requested direct reply, mention/verification context or an
explicit `work run` operation. It can issue a fresh help request even if an older
action expired, including before initial onboarding. Repeating the same action
or operation reuses its episode/deliveries. Expired authentication notices
disappear from `message inbox`; `message
show MESSAGE` retains their deadline/history and reports expired pending/deferred
state. A receipt cannot revive an expired notice. Legacy requests/messages derive
the deadline from their original creation; upgrade/restart does not renew them.

Founder can explicitly ask an agent to check again, even while it remains paused:

```sh
agent-cli --operation-id STABLE_NEW_REFRESH agent request-sign-in AGENT --input FILE
```

```json
{"provider":"codex","reason":"Request a fresh protected sign-in notice"}
```

Include `project_id` to refresh a project-scoped episode. Server authority, not a
message body or declared origin, enforces Founder-only issuance. The result is a
protected urgent `authentication-refresh` message with a 15-minute expiry, not
proof of delivery or sign-in. Its `handled` receipt means the target processed the
check. The target probes the selected provider without inference and, only if
sign-in is explicitly required/rejected, issues a fresh superseding request under
its own identity. Healthy/credentials-present-unverified status does not produce
a sign-in notice; unknown/outage status remains an explicit blocker. An offline
or fenced target may not process the refresh before it expires; Founder can issue
a new operation when appropriate. Retry the same operation/input after a lost
acknowledgement; a new intentional refresh needs a new operation ID.

`work status` also exposes `permission_requests`: protected help requests for
workspace/tool permission, provider access, phase-invalid text responses
(`provider-response`), task authority or exact retry approval.
They identify the requested action and route to active Founder/current project
delegates without inference. Delivery uncertainty remains pending; retries reuse
their exact proposals. A new requested action or an observed resolution followed
by loss can ask again; expiry/polling alone does not create a notification loop.
Private paths/raw provider errors are never copied into these notices. A fresh
provider-status check after an invocation failure requests sign-in if it now
reports login-required/rejected; uncertain invocation/retry fences remain enforced.

This never performs login, exposes device codes, opens assistance, resumes ai or
changes a persistent grant. Provider/device-code/live-session expiries are
independent. Polling, restart, replies and receipts never renew requests or prove
authentication. A later successful readiness observation resolves the episode;
a subsequent confirmed sign-out can create a new one. Founder must remain an
active human-help recipient; an AI delegate is not a replacement.

Preferred provider/model apply unless the assigned execution task requires its
own selection. Independent verification always uses the verifier's own preferred
provider/model, not the assignee's required selection.
Missing/unsupported provider/model never silently falls back. Current Codex
status may be credentials-present-unverified; do not report live authentication
from that alone. Copilot has bounded read-only authentication/model RPC.

Provider install/update and worker daemon startup install managed `codex` and
`copilot` launchers in `$HOME/.foundation/bin`. They dispatch through `provider exec`,
so upgrades select the current private managed version rather than a shell link
to a stale release. A preserved managed block in the applicable sh/bash/zsh
login profile adds that directory to PATH without duplicates. Existing unrelated
commands/profiles are never replaced; unmanaged name collisions fail explicitly.
Use `provider link` to repair an older installation, then open a new login shell
or run `export PATH="$HOME/.foundation/bin:$PATH"; hash -r` in the current one.

Native `--version`/`--help` probes do not need a TTY or a work slot. Other bare
provider commands are explicit human sessions, share admission with automatic
work/login, and refuse pending invocation/maintenance/session fences. They do not
claim or approve a Foundation task, change mode, or imply spending permission.
They keep native provider argument/permission behavior; controller coding
approval flags remain separate. Copilot's vendor self-updater is disabled for
shell/login/remote-login launches; use `provider update --provider copilot`
instead of bare `copilot update` to preserve managed integrity checks.
Native sessions have no default 60-second
command cap; an explicitly supplied global `--timeout` still applies.

Mode `ai` enables serial automatic preparation/execution/verification/replies
when all gates permit. An independent current project delegate selects completed
results after its assigned work and reviews them with tools in its own authorized
`workspace_root/PROJECT` binding. Review decisions and evidence are recorded in
`task runs` and verifier-owned task comments. In-scope rework returns `todo` with
a fresh delegate readiness approval; scope changes still invalidate approval.
Accept is applied only through an exact current verification report. A required
PR merge must be checked by the reviewer before acceptance; a provider exit or
PR URL alone is not merge evidence. Failed or uncertain verification retains
`completed` with waiting kind `verification` and never automatically retries
external tools. After inspecting effects, project authority can use an exact
`task change` kind `verify`, decision `accept` or `rework`; recovery/acknowledgement
alone is not a review retry authorization.

Only an explicit authenticated Founder request may place a non-Founder
agent in `manual` or resume it; delegates and local target CLI calls cannot.
Workers mirror those requests, cancel/join automatic inference on pause, and
retain reconciliation. A legacy unexplained local manual pause is not silently
resumed; Founder must explicitly decide. Paused non-Founder agents cannot bypass
the pause with `work run`. Founder may use its own manual human entry point.
No separate work-loop switch exists. Provider tool approval is local, not root,
task readiness, spending or consent. New unknown-side-effect attempts require
current exact retry authority; recovery is not permission to rerun.

## API map for development

Application/schema contract version is **2**, native message protocol **2**,
server SQLite migration **4**, local SQLite **1**. Native processes exchange
bounded protobuf frames over pinned SSH stdio/inner mTLS and peer-checked local
Unix sockets. There is no internal HTTP/gRPC listener or HTTP downgrade.
JSON envelopes remain CLI output and local evidence, not native wire payloads.

The method/path notation below is an in-process selector used by command
adapters and trusted JSON business fixtures. Native wire operations have typed
names such as `query.me` and `command.tasks.by_id.change`; methods, URL paths,
headers and bearer tokens are not sent over the message session. `/healthz`
selects a compatibility query, not a public TCP enrollment service.

The join channel exposes `/v1/identity/challenge` and `/v1/identity/activate` for
fresh approved-key proof, then device-certificate-authenticated binary
`identity.owner.challenge`/`identity.owner.prove` exchanges. That control channel proves an ephemeral owner key
and issues an owner certificate. Ordinary API requires current owner mTLS plus
agreement with the kernel peer UID and instance/agent/key/owner generations;
request headers and JSON cannot supply a different principal.

Ordinary families: `/me`, `/agents`, `/heartbeat`, `/identity/register`,
`/identity/status`, `/identity/AGENT/recover`, `/projects`,
`/project-creation-grants`, `/tasks`, `/entries`, `/attention`, `/events`,
`/ownership`, `/messages`, `/sessions`, `/agent-operations`, `/updates`. Registration/recovery is Founder-only;
Founder recovery is root-only private Unix maintenance, never one of these
agent routes. Entity changes use dedicated POST suffixes; claims/reports are
controller operations, not generic CLI write escapes. Relays recheck current
identity/owner, party/side, scope, consent/open and expiry.

Only different live owner keys with valid possession proofs and a fresh
incumbent binary control ping cause the **entire identity** ban, original and
contender including Founder. Ordinary CLI traffic attaches the local proxy;
crash/reconnect, key collision and unproved attempts do not ban. Ban closes API
and streams. Recovery needs fresh keypairs and explicit generation/epoch
transition; it never approves resuming uncertain work. Software/RAM clones and
sequential use remain possible; no hardware signing or attestation is claimed.

Read-only diagnostic routes are `GET /v1/agents/{id}/diagnostics`
(`domain.AgentDiagnostics`, Founder/self) and `GET /v1/messages/{id}/diagnostics`
(`domain.MessageDiagnostics`, message parties). `POST /v1/heartbeat` accepts
`domain.HeartbeatRequest`: existing command metadata plus optional version-1
`health`. Legacy bodies remain accepted and the result remains `domain.Agent`.
Latest reports/incidents use bounded service-owned settings; health itself needs
no migration beyond the current SQL4 scoped-update contract.

`GET /v1/updates?since=CURSOR&limit=N&wait=true` selects a recipient-scoped
blocking update request. An empty cursor returns the initial current watermark;
subsequent requests replay or wait without timer-driven SQL discovery. `GET
/v1/events` additionally accepts `wait=true` for explicitly filtered shared CLI
watch. Private identifiers never appear in shared events. Discovery, heartbeat,
transport ping and actual work progress are separate; receipt of an update is
not a message read acknowledgement or work authority.

Founder control writes are `POST /v1/agents/{id}/diagnostics` and
`POST /v1/agents/{id}/repairs`. The target's dedicated control consumer uses
`GET /v1/agent-operations?limit=N&cursor=CURSOR` for its queued/checking nonexpired
operations and `POST /v1/agent-operations/{id}/result` for its own safe typed
reports. Requester Founder/target lookup uses `GET /v1/agent-operations/{id}`.
The proxy accepts only their exact typed capabilities; the list accepts only
`limit`/`cursor`, while detail/control routes accept no query extras. Transport,
certificate, native root/service controls and deeper write paths remain denied.

Exact selectors/DTOs are in `internal/api/server.go`, `internal/api/messages.go`, `internal/api/identity.go`,
`internal/domain/commands.go`, `internal/domain/identity.go`,
`internal/domain/agent_operations.go` and `internal/domain/health.go`;
[SCHEMA.md](SCHEMA.md) describes their invariants and qualification limits.
