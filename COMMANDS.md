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
| `--config PATH` | Private configuration. Worker default: `$HOME/.config/fdn/agent.json`; server default uses `server.json` in that directory |
| `--json` | Structured output; ordinary commands already use JSON. Disables protected terminal UI |
| `--non-interactive` | No protected terminal UI/hidden credential prompt |
| `--timeout SECONDS` | 1-300 seconds, default 60; does not extend task/session authority |
| `--operation-id ID` | Stable write/replay key; generated and returned if omitted |
| `--expected-rev N` | Explicit optimistic revision for applicable mutations |
| `--version` | Version, commit, API/schema contract versions |
| `help`, `--help`, leaf `--help` | Offline command discovery; no initialization/network |

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

Available in every binary without configuration, enrollment or network:

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
| `fdn-service init` | `--state DIR --bootstrap-file FILE [--listen 127.0.0.1:8443]`; private fresh/recoverable initialization, never reset |
| `fdn-service serve` | Native unprivileged TLS service; one local server lock |
| `fdn-service doctor` | Read-only SQLite integrity/TLS pair/configuration checks |
| `fdn-service bootstrap` | `--output FILE [--ttl 900]`; replacement bootstrap only before Founder enrollment, TTL 60-86400 |
| `fdn-service tls-renew` | Stop server first; replace leaf/key under existing CA, resume an interrupted exact pair |
| `fdn-service backup-key` | `--output FILE`; create private age identity and return public recipient |
| `fdn-service backup` | `--output FILE --recipient age1...`; encrypted consistent SQLite/trust snapshot |
| `fdn-service restore` | `--input FILE --identity-file FILE --destination NEW_DIR`; verify complete archive, never overwrite |
| `fdn-service service plan` | `--input OPTIONS`; render native Foundation definition without registering |
| `fdn-service service install` | `--input OPTIONS --apply`; explicit OS-administrator registration/start |
| `agent-service serve` | Native enrolled worker, guidance installation, one daemon, private state/tunnels/controller/session supervision |
| `agent-service status` | Read-only local daemon/work/journal/fence status |

## Worker setup and native services

| Command | Arguments / scope |
| --- | --- |
| `agent-cli config init` | `[--state DIR --url HTTPS_ORIGIN --ca-file FILE --provider codex|copilot --workspace-root DIR --allow-workspace-tools]`; refuses existing configuration; installs own-account guidance |
| `agent-cli config show` | Non-secret configuration; never credential contents |
| `agent-cli config set` | `--input FILE`; documented patch below; credentials/identity are not editable |
| `agent-cli connect` | `--invitation-file FILE --name NAME`; pinned SSH/TLS, private generated credential, recover exact pending enrollment |
| `agent-cli status` | Authenticated identity plus local daemon/work/pending/fence state |
| `agent-cli doctor` | Read-only configuration, TLS/API authentication and service version |
| `agent-cli mode show` | Current configured `ai`/`manual` |
| `agent-cli mode set` | `ai` or `manual`; sole automatic-work switch |
| `agent-cli preferences show` | Preferred provider/model |
| `agent-cli preferences set` | `--input FILE`, optional `provider`/`model`; provider change clears old model |
| `agent-cli service plan` | `--input OPTIONS`; render native worker definition |
| `agent-cli service install` | `--input OPTIONS --apply`; own-account native registration/start |
| `agent-cli service status` | Same local status without requiring network |

Configuration patches accept only `poll_seconds` (1-3600),
`invocation_timeout_seconds` (1-86400), `research_interval_hours` (12 or 24),
`automatic_provider_updates`, `allow_workspace_tools`, `provider_paths`,
`vnc_address`, `vnc_verified`, and `ssh`. Provider paths are absolute and only
name codex/copilot. Transport/provider-path changes require service restart.

`ssh` has host/user/port, absolute identity/known-hosts files, local port and
remote port. URLs/listeners remain loopback HTTPS; use an individually pinned
OpenSSH tunnel for a remote Foundation. See [DEPLOYMENT.md](DEPLOYMENT.md).

Native `OPTIONS` fields: `os` (linux/darwin), `role` (foundation/agent), absolute
`binary`, `config_file`, `state_dir`, `home`, `definition`, and dedicated `user`.
User-service registration needs the correct Linux user manager/bus environment.
Native manager stop/restart/uninstall is an explicit OS operation, not an
unimplemented `agent-cli service stop` command.

## Identity and projects

| Command | Arguments/input |
| --- | --- |
| `agent-cli agent list` | Active identity checks; shared agent registry |
| `agent-cli agent revoke` | `AGENT`; Founder only, not Founder itself |
| `agent-cli invitation create` | `--output PRIVATE_FILE [--ttl 900]`; Founder/current delegate, protected one-time output |
| `agent-cli project list` | Shared registry |
| `agent-cli project show` | `PROJECT` |
| `agent-cli project create` | `--input FILE`: `name`, `description`; consumes specific creation grant for non-Founder |
| `agent-cli project change` | `PROJECT --input FILE`: reason + optional name/description/archived; current authority, advances epoch |
| `agent-cli project delegate` | `PROJECT --input FILE`: `agent_id`, `revoke`; grant Founder-only, revoke project authority |
| `agent-cli project authorize` | `--input FILE`: `agent_id`, `revoke`; Founder-issued specific creation authorization |

## Tasks, approvals and run projections

| Command | Arguments/input |
| --- | --- |
| `agent-cli task create` | `--input FILE`: project_id, title, description, acceptance_criteria, optional parent_id/repetitive/priority/importance |
| `agent-cli task list` | `[--project ID --assignee ID --status STATUS --limit N --cursor CURSOR --include-archived]` |
| `agent-cli task show` | `TASK`; current protected/ordinary context |
| `agent-cli task change` | `TASK --input FILE`; typed mutation kinds below, reason required |
| `agent-cli task history` | `TASK`; latest three retained snapshots with action/reason |
| `agent-cli task runs` | `TASK`; execution projections/evidence, not a claim/approval shortcut |

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

Literal mentions use `@{AGENT_ID}` with the complete actual agent ID substituted.
Only literal current body mentions produce attention. Removed/edited/self
mentions do not confer authority. Cursors are actor/query-bound; pages are
limited to 1-200. Watch delivery is at least once; inspect sequence/cursor rather
than assuming exactly-once processing.

## Private messages and human-only sessions

| Command | Arguments/input |
| --- | --- |
| `agent-cli message send` | `--input FILE`: recipient_id, body; optional origin/kind/urgent/response_requested |
| `agent-cli message show` | `MESSAGE`; sender/recipient only |
| `agent-cli message inbox` | `[--cursor CURSOR --limit N --urgent]`; recipient's pending messages |
| `agent-cli message receipt` | `MESSAGE --input FILE`: state handled/deferred/stale, or pending only from deferred |
| `agent-cli session request` | `--input FILE`: target_id, project_id, optional task_id, mode, purpose, provider, reason, expires_at |
| `agent-cli session list` | Current party's unexpired pending/consented/active sessions |
| `agent-cli session show` | `SESSION`; party-only redacted state |
| `agent-cli session change` | `SESSION --input FILE`: action consent/deny/open/active/revoke/close under exact party/current revision rules |
| `agent-cli session open` | `SESSION`; explicit requester action + protected human terminal attachment, no `--json`/`--non-interactive` |

Message origin defaults to human (a declared label, not attestation); kind
defaults to direct. Kinds authentication/session cannot request automatic
responses. A protected handoff body contains references/actions/expiry, never a
token, code, private authentication URL, transcript or screenshot.

Sessions need current project authority and target consent plus explicit
requester open. Mode is tmux/vnc; purpose terminal/authentication. Authentication
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
| `agent-cli provider login` | `[--provider NAME]`; actual human TTY only, no structured/captured credential UI |
| `agent-cli provider link` | Repair/install this account's stable codex/copilot launchers and login PATH without downloading, authenticating or running a provider |
| `agent-cli provider exec` | `--provider NAME -- NATIVE_ARGUMENTS`; resolve the active managed version, preserve native output/exit status; interactive commands require a human terminal and the shared local slot |
| `agent-cli work prompt` | Shared prompt and version-matched operating guidance; no task claim or inference |
| `agent-cli work status` | Read-only local state, blockers, pending invocations/reports, session fence and durable authentication request/delivery references |
| `agent-cli work run` | One bounded manual controller step; same prompt/authority/slot as service |
| `agent-cli work recover` | Exactly one `--journal INVOCATION` or `--session SESSION`, plus `--acknowledge-stopped` after confirming owned processes stopped |

When an ai-mode or explicitly invoked controller observes `login-required` or
`credentials-rejected`, it sends a protected `authentication` message to the
active Founder and, for task-scoped work, current project delegates. This
deterministic handoff works before onboarding and requires no provider inference.
Founder can read it with `message inbox`/`message show MESSAGE` even in manual
mode. It requests no automatic AI response and creates/opens/consents to no
remote session.

`work status` exposes `authentication_requests`: request/provider/scope IDs,
creation/state, recipient/operation/message references, confirmed delivery or
pending error, and resolution time. States are `delivery-pending`, `requested`
and `resolved`. No active recipients or unavailable delivery stays explicitly
pending. Polling, restart, lost acknowledgements, ordinary replies and receipt
acknowledgement do not create repeated requests or prove authentication. A later
successful provider readiness observation resolves the local episode; a new
confirmed sign-out can create a fresh request. The immutable old inbox message
remains recipient-owned evidence; inspect current status before acting.
Unknown/outage/protocol failures do not manufacture a login request.

Preferred provider/model apply unless the task requires its own selection.
Missing/unsupported provider/model never silently falls back. Current Codex
status may be credentials-present-unverified; do not report live authentication
from that alone. Copilot has bounded read-only authentication/model RPC.

Provider install/update and worker daemon startup install managed `codex` and
`copilot` launchers in `$HOME/.local/bin`. They dispatch through `provider exec`,
so upgrades select the current private managed version rather than a shell link
to a stale release. A preserved managed block in the applicable sh/bash/zsh
login profile adds that directory to PATH without duplicates. Existing unrelated
commands/profiles are never replaced; unmanaged name collisions fail explicitly.
Use `provider link` to repair an older installation, then open a new login shell
or run `export PATH="$HOME/.local/bin:$PATH"; hash -r` in the current one.

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

Mode `ai` enables serial automatic preparation/execution/replies when all gates
permit; `manual` cancels/joins automatic inference while retaining reconciliation.
No separate work-loop switch exists. Provider tool approval is local, not root,
task readiness, spending or consent. New unknown-side-effect attempts require
current exact retry authority; recovery is not permission to rerun.

## API map for development

The API uses the same envelope and typed inputs, always over TLS. `/healthz`
reports compatibility without agent credentials; `/v1/enroll` redeems a protected
one-time token. Other `/v1` endpoints use Bearer authentication.

Families: `/me`, `/agents`, `/invitations`, `/heartbeat`, `/projects`,
`/project-creation-grants`, `/tasks`, `/entries`, `/attention`, `/events`,
`/ownership`, `/messages`, `/sessions`. Entity changes use dedicated POST
suffixes; claims/reports are controller operations, not generic CLI write
escapes. Relay routes recheck party/side, authority, consent/open and expiry.
The exact route/DTO definitions are `internal/api/server.go` and
`internal/domain/commands.go`; [SCHEMA.md](SCHEMA.md) describes all invariants.
