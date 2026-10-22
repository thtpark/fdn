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
| `agent-service status` | Read-only local daemon/work/journal/fence status |

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
| `agent-cli service status` | Same local status without requiring network |

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

### Protected identity service (different UID)

| Command | Arguments / boundary |
| --- | --- |
| `agent-identity prepare` | Global `--config PRIVATE_CONFIG`, then `--name NAME --output PUBLIC_FILE`; retain private software SSH/authentication keypairs and export a signed public-only request; exact retry reuses the request |
| `agent-identity connect` | Global `--config PRIVATE_CONFIG`, then `--profile APPROVED_PUBLIC_PROFILE`; validate/store the provisioned public profile against this account's prepared keys |
| `agent-identity serve` | Global `--config PRIVATE_CONFIG`; maintain pinned SSH, activation, device-authenticated owner control and peer-checked local runner proxy |
| `agent-identity service plan` | `--input OPTIONS`; read-only two-account/native-definition plan, no configuration, enrollment or root execution required |
| `agent-identity service install` | `--input OPTIONS --apply`; explicit native root registration of the identity daemon and distinct runner service, never account creation/adoption |
| `agent-identity service status` | `--input OPTIONS`; native root inspection of that exact two-service installation, no `--apply` |

The three `service` leaves take an integrity-protected options **file**, not stdin
or `--config`. Their `OPTIONS` is `platform.IdentityServiceOptions`, distinct
from the single-role options below:

- Selection: `os` (`linux`/`darwin`), `name`.
- Accounts: `identity_user`, `identity_uid`, `identity_gid`, `worker_user`,
  `worker_uid`, `worker_gid`; distinct non-root users and distinct positive
  UID/GID pairs, with primary group names matching their account names.
- Absolute paths: `identity_binary`, `worker_binary`, `identity_config`,
  `worker_config`, `state_dir`, `worker_state_dir`, `identity_home`, `worker_home`,
  `socket_dir`, `identity_definition`, `worker_definition`.

Preprovision exact accounts/HOMEs/groups, managed comment/RealName markers
`fdn-identity=NAME` and `fdn-runner=NAME`, a no-login identity shell, and one-way
identity membership in the worker group. The runner cannot join identity/root
groups. Missing/mismatched accounts fail as prerequisite/conflict, not adoption
or a same-UID fallback. Both 0600 configurations must already exist under their
own private 0700 HOMEs; the runner's socket binding and all plan paths must agree.
Paired binaries must be immutable `agent-identity`/`agent-service` in the same
binary directory. Native definition paths and manager details are in
[DEPLOYMENT.md](DEPLOYMENT.md#native-identity-and-runner-services).

`agent-identity --help` is offline. There is no identity `config init` leaf:
authorized native setup supplies an identity-owned 0600 configuration, separate
0700 key state, explicit runner UID/GID and an absolute socket in a separate
identity-owned 0710 runner-group IPC directory. Private `prepare`/`connect`/`serve`
execution rejects root and an identity UID equal to the runner UID. Stop the
identity service before offline `prepare`/`connect`. Its proxy does not export
private keys, signing operations, owner control or certificates to providers.
The public profile contains `identity`, `url`, `ca`, `ssh_host`, `ssh_port` and
`known_hosts`; approve its integrity even though its contents are not secret.

There is **no `--rotate` flag**. For explicit fresh-key recovery, stop the
service, preserve its old private StateDir and **all runner state/journals**,
then choose a new empty identity-owned 0700 StateDir inside the **same identity
HOME**. Keep the private config file path/IPC and all runner paths stable;
update only the intended `state_dir` binding in that config and service OPTIONS.
Run private `prepare` in the new state, obtain Founder/root recovery, connect
its newly approved public profile and restart the named native services. Normal
prepare retry/reconnect/restart reuses the retained identity, never auto-rotates
keys. See [DEPLOYMENT.md](DEPLOYMENT.md#diagnose-identity-state-and-recover).

### Existing single-role native service options

The `fdn-service service` and `agent-cli service` commands retain their separate
`platform.Options` contract; do not pass paired identity-service options here.
Fields: `os` (linux/darwin), `role` (foundation/agent), absolute
`binary`, `config_file`, `state_dir`, `home`, `definition`, and dedicated `user`.
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
| `agent-cli agent list` | Active identity checks; shared agent registry |
| `agent-cli agent show` | `AGENT`; current role, authoritative mode/revision and Founder request attribution |
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

Literal mentions use `@{AGENT_ID}` with the complete actual agent ID substituted.
Only literal current body mentions produce attention. Removed/edited/self
mentions do not confer authority. Cursors are actor/query-bound; pages are
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
| `agent-cli message show` | `MESSAGE`; sender/recipient only |
| `agent-cli message inbox` | `[--cursor CURSOR --limit N --urgent]`; recipient's pending messages |
| `agent-cli message receipt` | `MESSAGE --input FILE`: state handled/deferred/stale, or pending only from deferred |
| `agent-cli session request` | `--input FILE`: grant_id, target_id, matching project_id/scope, optional task_id, mode, purpose, provider, reason, expires_at |
| `agent-cli session list` | Current party's unexpired pending/consented/active sessions |
| `agent-cli session show` | `SESSION`; party-only redacted state |
| `agent-cli session change` | `SESSION --input FILE`: action consent/deny/open/active/revoke/close under exact party/current revision rules |
| `agent-cli session open` | `SESSION`; explicit requester action + protected human terminal attachment, no `--json`/`--non-interactive` |

Message origin defaults to human (a declared label, not attestation); kind
defaults to direct. Kinds authentication/permission/session cannot request automatic
responses. A protected handoff body may contain public generic verification
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
workspace/tool permission, provider access, task authority or exact retry approval.
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

API/schema contract version is **2**, server SQLite migration **3**, local
SQLite **1**. The API uses the same envelope/typed inputs over pinned SSH stdio
and inner TLS on Unix ingress. `/healthz` reports compatibility on that transport;
it is not a public TCP enrollment service. No bearer enrollment or invitation
redemption exists in the runtime.

The join channel exposes `/v1/identity/challenge` and `/v1/identity/activate` for
fresh approved-key proof, then device-certificate-authenticated WebSocket
`/v1/identity/owner`. That owner control channel proves an ephemeral owner key
and issues an owner certificate. Ordinary API requires current owner mTLS plus
agreement with the kernel peer UID and instance/agent/key/owner generations;
request headers and JSON cannot supply a different principal.

Ordinary families: `/me`, `/agents`, `/heartbeat`, `/identity/register`,
`/identity/status`, `/identity/AGENT/recover`, `/projects`,
`/project-creation-grants`, `/tasks`, `/entries`, `/attention`, `/events`,
`/ownership`, `/messages`, `/sessions`. Registration/recovery is Founder-only;
Founder recovery is root-only private Unix maintenance, never one of these
agent routes. Entity changes use dedicated POST suffixes; claims/reports are
controller operations, not generic CLI write escapes. Relays recheck current
identity/owner, party/side, scope, consent/open and expiry.

Only different live owner keys with valid possession proofs and a fresh
incumbent WebSocket ping cause the **entire identity** ban, original and
contender including Founder. Ordinary CLI traffic attaches the local proxy;
crash/reconnect, key collision and unproved attempts do not ban. Ban closes API
and streams. Recovery needs fresh keypairs and explicit generation/epoch
transition; it never approves resuming uncertain work. Software/RAM clones and
sequential use remain possible; no hardware signing or attestation is claimed.

Exact routes/DTOs are in `internal/api/server.go`, `internal/api/identity.go`,
`internal/domain/commands.go` and `internal/domain/identity.go`;
[SCHEMA.md](SCHEMA.md) describes their invariants and qualification limits.
