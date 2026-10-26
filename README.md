# Foundation in Go

A self-contained Go implementation of Foundation through **Phase 5**, with an
authoritative SQLite-backed service, a native worker daemon, and one structured
CLI for people and coding agents. Development and operation need no companion
checkout. Existing legacy configuration, credentials, and databases are **not
imported or overwritten**. The API/schema **2** software-first security upgrade
is being integrated; source-bound acceptance and native qualification are
separate. No hardware adapter or attestation is claimed.

Native coordination uses version-2 protobuf message sessions over pinned SSH
stdio and inner mTLS, not HTTP or gRPC. The kernel peer UID binds the Foundation
account; transactional policy still decides what that identity may do. Scoped
durable push replaces fixed message/task/session/control discovery polling.
See [CAPACITY.md](CAPACITY.md) for the approved 1,000-logical-identity harness and
its evidence boundaries; this is not a native fleet/deployment capacity claim.

## Documentation and operational skills

Start with [AGENTS.md](AGENTS.md). The portable
[operational skills](.agents/skills/) cover installation, enrollment, protected
provider authentication, task work, recovery and development.

| Reference | Purpose |
| --- | --- |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Code map, extension seams, compatibility and contribution workflow |
| [COMMANDS.md](COMMANDS.md) | Every implemented CLI leaf, typed inputs, authority and output contracts |
| [SCHEMA.md](SCHEMA.md) | SQLite/local state, ownership, generations, approval and recovery invariants |
| [DEPLOYMENT.md](DEPLOYMENT.md) | Native install/enroll/auth, discovery, upgrades, backup/restore and TLS |
| [TEST_PLAN.md](TEST_PLAN.md) | Independent local suites, exact regression expectations and evidence limits |
| [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) | Delivered phases, preserved product goals and remaining work |
| [CAPACITY.md](CAPACITY.md) | Current load evidence and the unqualified 1,000-agent target |
| [MIGRATION.md](MIGRATION.md) | Design continuity, deliberate changes and no automatic legacy import |

These references and skills are embedded in binaries and included in every
release format. `agent-cli guide list/show/install` works offline without
configuration/enrollment; Foundation-only hosts use `fdn-service guide`.
Worker initialization/startup safely installs the account's guide/discovery links
without overwriting unrelated instructions or skills. The shared controller also
supplies bounded guidance explicitly when provider discovery is disabled.

## What is implemented

| Phase | Delivered capability |
| --- | --- |
| 1 | Go module, typed contracts, structured errors, owner-only configuration/state, schema migrations, TLS trust, reproducible release tooling. |
| 2 | Founder-only signed public-key registration, dedicated per-agent SSH accounts/UIDs, isolated identity service, mutual TLS and owner-generation fences; project-creation grants, revocation, heartbeat, native definitions, encrypted database/trust backup and fresh-directory restore. Security acceptance remains source-bound; native qualification is separate. |
| 3 | Projects, collaborative tasks, assignment, named/independent approvals, verification/rework, three-history retention/revert with reasons, creator-owned discussions/worklogs, mentions, paginated watches, offline private knowledge, capability inventory, explicit workspace authorization. |
| 4 | Codex/Copilot adapters, private messages, urgent inboxes, staged provider installation/updates, protected login handoff, expiring target-consented tmux/existing-VNC sessions. |
| 5 | Shared manual/automatic controller and prompt, first capability review, scope/acceptance refinement, actual-answer incorporation, exact-specification signoff requests, preflight, serial claims, supervised processes, pause/checkpoints, durable report outbox, restart reconciliation, unsafe-retry approval/backoff, idle research. |

Files/diaries/shared skill publication, recurring-child generation, and full-host
disaster-recovery automation from the original Phases 6-8 are outside this
milestone. Repetitive parents are recognized but cannot be executed.

## Architecture and boundaries

Non-Founder agents are fully unattended and autonomous. Only Founder is the
human-help coordination point; delegates may themselves be AI agents. Regular
agents run in `ai` unless an authenticated Founder explicitly pauses them.
Persistent, scoped assistance authorization is issued once by Founder and
reused until revoked; a target never needs a local human to prepare a granted
connection. Live connection expiry/restart does not erase that authorization.

* `fdn-service` is the only SQLite writer exposed to computers. Its TLS API owns
  enrollment, roles, task authority, transaction boundaries, and run fences.
  Clients cannot bypass authorization with database writes or bulk endpoints.
* `agent-service` runs under the dedicated provider-runner OS account, never the
  protected identity-service UID. `agent-cli work run` uses the **same controller,
  prompt, local slot, and server
  checks**, not a separate privileged execution path.
* SQLite uses WAL, full synchronization, foreign keys, indexed assignment/state
  queries, keyset pagination, and bounded connection pools. It is intentionally
  a single-authority, single-writer design, not a distributed SQL system.
* Each enrolled identity, **including Founder**, has a unique Foundation-host
  SSH account and UID. An existing, pinned OpenSSH server forces the fixed
  `fdn-api`/`fdn-join` stdio gateway; it permits no arbitrary command, PTY or
  forwarding. API/join ingress is local Unix, not a TCP bootstrap listener.
* Inner TLS remains mandatory. Kernel Unix peer UID must agree with the signed
  client certificate's instance, agent, key generation and owner generation.
  Device certificates open only identity admission/control; ephemeral owner
  certificates authorize ordinary API requests while that owner is live.
* `agent-identity` runs as a protected credential-service UID distinct from the
  provider runner UID. Long-term SSH/authentication keys and device credentials
  never reach the runner. `agent-cli`/`agent-service` use its peer-checked local
  Unix proxy and retain only a public AgentID, not a bearer credential.
* One enrolled identity and one active work slot belong on each computer. A
  proved second **different live owner key**, with a fresh successful incumbent
  binary control ping, bans the entire identity: both owners lose access/streams, even
  when the original is Founder. Dead/reconnecting owners, ordinary CLI calls,
  public-key collisions and unproved attempts are not duplicate proof.
  Software keys, including RAM, can still be cloned or used sequentially;
  this is not perfect physical-clone prevention or hardware attestation.
* Enrolled agents can read shared projects/tasks/discussions. Messages and
  session records are API-private to their parties. Raw provider output, login
  credentials, workspace paths, discovery details, and local knowledge stay
  under owner-only local state unless deliberately included in a shared summary.

See the [domain contracts](internal/domain/), [server policy](internal/foundation/),
[message protocol](internal/message/), [command adapters](internal/api/), and
[controller](internal/work/).

## Local development and tests

Requirements: **Go 1.27+** with a working Go race-detector toolchain (including
its platform C compiler), a Unix host (Linux or macOS), `/bin/sh`, and OpenSSH
client. No database daemon, Node, provider subscription, root privileges, VM,
container engine, cloud account, or desktop is needed for the test suites.

```sh
mkdir -p bin
go build -o ./bin/ ./cmd/...
bash scripts/test.sh all
```

Suites are independently runnable:

```sh
bash scripts/test.sh static       # formatting, module hashes, vet, native/cross builds
bash scripts/test.sh unit         # race-checked policy, storage, CLI, installer contracts
bash scripts/test.sh integration  # race-checked real loopback TLS/SSH/relay + recovery
bash scripts/test.sh e2e          # actual native binaries and deterministic provider CLIs
bash scripts/test.sh stress       # 1,000 tasks; 24 writers; 192 accepted concurrent edits
```

Ordinary `go test ./...` runs unit/tooling tests. Integration, end-to-end, and
stress packages require their respective build tags. Every suite uses private
temporary directories and dynamically allocated local ports. Tests never
register real OS services, create accounts, modify SSH/desktop settings, or
perform paid inference.

End-to-end tests execute both provider argument/output paths, actual workspace
edits, clarification before readiness, separate scope/acceptance review, manual
stop, failed/mid-run-question results, daemon kill/restart fencing, and explicit
recovery. Business fixtures use explicit test authentication and separate HOME,
not native account-isolation proof. Separate security regressions use real Unix
peer credentials, TLS/mTLS, owner control and pinned SSH stdio/in-process SSH;
new security acceptance remains pending integrated verification. Policy fixtures
also cover unauthorized writes, competing claims, consent withdrawal, orphan
claims and lost acknowledgements without repeated inference.

The measured stress workload is a reproducible correctness/retention check, not
a claim of unlimited throughput. Add workload measurements before choosing a
larger deployment or a different persistence backend.

### Evidence limits

Provider execution/authentication and privileged OS integrations use the
deterministic/recording fixtures agreed for this rewrite. Passing tests does
**not** certify live vendor login/model behavior, Linux systemd installation,
macOS GUI consent, real VNC credentials, or reboot/full-host recovery. The real
SSH/TLS/process/filesystem operations above do run locally. No hosted Actions
run or signed/notarized release is implied by local success.

## Initialize Foundation and prepare Founder

The native Foundation target is Linux. Follow [DEPLOYMENT.md](DEPLOYMENT.md)
for administrator-managed account-helper/gateway prerequisites and socket
ownership. The main server remains unprivileged. Native multi-UID installation
and hardware qualification have **not** been performed by rootless tests.

On Founder's computer, as its protected identity-service account with an
administrator-prepared private configuration (not as the provider runner):

```sh
agent-identity --config /PRIVATE/identity.json prepare \
  --name Founder --output /PUBLIC/founder-request.json
```

Only the signed public request leaves that account. As the dedicated server
account, after the approved root account helper is configured/running:

```sh
fdn-service --config /var/lib/fdn/server.json init \
  --state /var/lib/fdn --founder-request /PUBLIC/founder-request.json \
  --ingress-socket /run/fdn-ingress/api.sock --join-socket /run/fdn-ingress/join.sock \
  --ingress-group INGRESS_GID --account-socket /run/fdn-accounts/accounts.sock
fdn-service --config /var/lib/fdn/server.json serve
```

`INGRESS_GID` is the approved numeric group ID. State/configuration and keys
remain service-owned 0700/0600. Initialization returns the public identity and
CA-file path; an authorized operator assembles the public connection profile
with independently pinned SSH host/port/known_hosts. There is no init profile
output flag, bearer bootstrap or TCP API listener. An interrupted initialization
retains the same request/identity/keys; retry it exactly rather than resetting.

## Register a worker and attach its runner

Each computer, including Founder, uses the existing installing user for identity,
runner and providers. Do not create another OS account/group or rename that user.
The local username is independent of the enrolled agent name: existing `alice`
can enroll as `Agent1`. As that user, prepare private identity configuration/state
under HOME and prepare its keys:

```sh
agent-identity --config /PRIVATE/identity.json prepare \
  --name Agent1 --output /PUBLIC/agent-request.json
```

Founder alone registers subsequent signed requests, not a project delegate:

```sh
agent-cli agent register --input /PUBLIC/agent-request.json \
  --output /PUBLIC/agent-profile.json
```

After authenticated public-profile transfer, as that same existing user
with its identity service stopped for the offline profile operation:

```sh
agent-identity --config /PRIVATE/identity.json connect \
  --profile /PUBLIC/agent-profile.json
agent-identity --config /PRIVATE/identity.json serve
```

`serve` performs key-bound activation and maintains the device-authenticated
binary owner-control session. Founder uses its operator-assembled profile the same way.
Run the runner CLI/provider service as that **same user**:

```sh
agent-cli --config "$HOME/.config/fdn/agent.json" config init \
  --state "$HOME/.local/share/fdn" --identity-socket "$HOME/.local/state/fdn-ipc/identity.sock"
agent-cli --config "$HOME/.config/fdn/agent.json" connect
agent-cli agent identity
agent-cli doctor
```

The local socket is absolute, user-owned 0600 under a private 0700 parent and
peer-checked. The runner's public binding contains only AgentID; ordinary IPC
does not export private keys/certificates. That user nevertheless owns its keys:
separate processes organize lifecycle, not isolation from its own tools.
Founder initially uses manual mode; regular agents use
server-controlled ai. Provider sign-in remains separate from enrollment.

For a designated original existing non-Founder, Founder first uses
`agent register --existing-agent AGENT --input PUBLIC [--output PROFILE]`;
then that runner explicitly uses `connect --migrate`, preserving local SQLite 1,
knowledge, journals, bindings and outbox. Legacy Founder recovery is root-local.
Do not copy another identity, share SSH accounts or use old tokens as fallback.

A confirmed simultaneous different owner bans the **whole identity**, including
the original and Founder, and closes API/streams. Recovery requires fresh
keypairs and explicit authority (`agent recover AGENT`, or root-local
`fdn-service recover-founder` for Founder). A crash/reconnect, ordinary CLI call,
public-key collision or unproved attempt is not sufficient ban evidence.
Software/RAM clones or sequential key use can remain undetected; no perfect
physical-clone prevention, hardware signer or attestation is claimed. Explicit
fresh-key recovery uses a new empty private StateDir inside the same identity
HOME, preserving old private evidence and all runner state/configuration paths;
there is no `--rotate` or automatic retry-time rotation. Follow the
[recovery runbook](DEPLOYMENT.md#diagnose-identity-state-and-recover).

## Tasks and approvals

Every task belongs to a project. Only Founder or that project's current
delegates can assign a worker, require a provider/model, approve readiness or
unsafe retry, and verify results. Founder grants project creation/delegation.
Ordinary enrolled agents collaborate on task content. Only a comment/worklog's
creator may edit it, including when the requester is Founder.

`required_actors` is an eligible-approver allowlist, not a multi-signature quorum
or a grant of project authority. `independent_readiness` and
`independent_verification` require an actor different from the assignee.
Self-approval is otherwise permitted.

All ordinary output is an envelope:

```json
{"ok":true,"data":{},"operation_id":"op_...","warnings":[]}
```

Errors have `ok:false` and `error:{code,message,retryable}`. Exit codes are
`2` invalid input, `3` not found, `4` denied/trust failure, `5` conflict,
`6` missing prerequisite, `7` unavailable, `8` provider failure,
`9` internal/incompatible, and `130` cancelled.

Global flags precede the command: `--config`, `--json`, `--non-interactive`,
`--timeout 1..300`, `--operation-id`, `--expected-rev`. Entity IDs precede
leaf flags. JSON input uses `--input FILE` or explicit `--input -`.
There are no hidden input prompts or force/skip-approval options.

Examples of JSON inputs:

```json
{"name":"Example project","description":"Intentional shared project context"}
```

```json
{
  "project_id":"PROJECT",
  "title":"Implement the agreed change",
  "description":"Precise instructions, inputs, scope, and constraints",
  "acceptance_criteria":"Specific verifiable checks"
}
```

```json
{"kind":"assign","reason":"Authorized assignment","agent_id":"AGENT","provider":"codex"}
```

```json
{"kind":"approve","reason":"Reviewed exact current specification","spec_generation":4}
```

Use them with `project create`, `task create`, or `task change TASK`.
`task change` supports `edit`, `status`, `assign`, `provider`, `workspace`,
`policy`, `retry-policy`, `approve`, `withdraw`, `verify`, `retry-approve`,
`clarity`, `waiting`, `revert`, `archive`, `stop`, and `release`.
The exact fields are in [TaskChange](internal/domain/commands.go).
`release` requires the exact active run, a reason, and explicit confirmation
that its original processes stopped; there is no timeout takeover.

Writes retain the exact proposal locally before submission. If an expected
revision is omitted where a current entity is readable, the CLI reads and pins
it once. On a lost response, repeat the same operation ID and original input;
do not replace the revision, create another identity request, or re-run a provider.
Server receipts reference current records rather than creating a hidden archive
of historical payloads. Only the latest **three** history snapshots are retained.

Specification changes invalidate approval/clarity and request an active run to
stop. Project authority/context epochs also invalidate old approvals and clarity.
`completed` is a fenced provider result awaiting verification; **only** current
authorized verification reaches `done`.

## Local work and providers

Bind a safe, owned workspace and explicitly authorize tools:

```sh
agent-cli workspace bind --path /OWNED/PROJECT --allow-tools --timeout 3600
```

Set its returned opaque reference through a task's `workspace` change.
Alternatively, `config init --workspace-root /OWNED/PARENT
--allow-workspace-tools` authorizes the controller to prepare reusable private
project directories there. Neither assignment nor `mode ai` grants this local
tool permission. Bindings cannot contain Foundation state/configuration, and
ownership, write permissions, inode/device identity, disablement, and time limits
are checked again before/during execution.

Go services need no build toolchain at deployment. Provider installation/update
requires a supported native Node/npm runtime (use Node 22+ and npm with
`audit signatures`), under the dedicated account:

```sh
agent-cli provider install --provider codex --version EXACT_VERSION
agent-cli provider install --provider copilot --version EXACT_VERSION
agent-cli provider status --provider codex
agent-cli provider update --provider copilot
```

The installer exposes managed `codex` and `copilot` launchers in the account's
`.local/bin` and adds it to the applicable login profile without replacing user
settings. Worker startup repairs older installs; `agent-cli provider link` does
the same offline. Start a new login shell after repair, or run
`export PATH="$HOME/.local/bin:$PATH"; hash -r` in the current shell.
Bare commands select the current managed version, not an old pinned executable.
Interactive use shares the local work slot and requires a human terminal;
`--version`/`--help` are safe non-inference probes. No global npm installation or
provider-auth copying is needed.

Managed updates resolve public registry metadata, use exact integrity-locked
packages, disable install scripts, audit registry signatures, smoke-check the
version/non-prompting capabilities, and atomically change the active pointer.
Previous versions remain. Updates serialize with work; failed staging leaves
the active version unchanged. Automatic checks are daily; explicit executable
overrides are not updated. No silent fallback to another provider/model occurs.

Codex's local login status is honestly reported as
`credentials-present-unverified`, not live authentication proof. Copilot uses
bounded read-only authentication/model-catalog RPC. No inference is used to
guess prices, rank unknown model costs, or test authentication.

For direct login, run `provider login` in a human-only terminal. It refuses
structured/non-interactive/captured terminal output. A completed login command
does not itself claim verified vendor authentication.

The controller deterministically sends missing-authentication requests to the
active Founder and relevant task-project delegates, including before its first
capability review. No AI login/model call is needed. Founder can use
`agent-cli message inbox` in manual mode to read the protected request.
`work status` exposes durable request/delivery/message references and pending
delivery errors. Unchanged blockers, polling, restart, lost acknowledgements or
receipt acknowledgement do not spam new requests or prove authentication.
The handoff never changes mode, signs in, or opens/consents to a remote session.

```sh
agent-cli preferences set --input preferences.json
# Founder explicitly resumes a target using {"mode":"ai","reason":"..."}:
agent-cli agent mode AGENT --input mode.json
agent-service --config "$HOME/.config/fdn/agent.json" serve
# Or perform one bounded manual controller step:
agent-cli work run
agent-cli work status
```

Unapproved assigned work gets separate tool-disabled scope and acceptance
reviews. Blocking questions are published with literal `@{AGENT_ID}` mentions;
actual requested replies are incorporated into task text. No unanswered-task
busy loop or fabricated approval occurs. Current signoff is requested from
eligible authorities; delivery is not approval.

Eligible independent project delegates also select completed results for a
separate tool-enabled verification phase. They use their own provider/model and
private authorized project workspace, share serial admission, and report exact
spec/result/discussion/epoch-bound decisions. Review feedback becomes a task
comment; in-scope rework returns todo with fresh recorded delegate readiness.
The assignee preflights the feedback and updates the same PR. When merging is
part of the approved criteria, the reviewer must confirm the current reviewed
head, checks and successful merge before acceptance produces done. Uncertain
reviews retain completed evidence with an explicit verification blocker, never
a blind tool retry. See [SCHEMA.md](SCHEMA.md) for the claim/report contract.

Coding execution/verification calls explicitly preapprove local tools; text-only
preparation/selection/responses do not receive coding tools. These settings are not an OS sandbox,
permission to spend/contract, or consent to another computer. The shared
[prompt](internal/work/prompt.txt) keeps financial/legal decisions, private
material, checkpoints, and authority boundaries explicit.

Only Founder can request `manual`/`ai` for a non-Founder agent. `manual` stops
automatic inference/execution while retaining reconciliation; it is not a
local-human operating model, and a delegate/target cannot bypass the pause.
Already-running approved work during a connection outage is bounded by retained
owner authority and local supervision; certificate expiry/denial closes owned
streams and preserves uncertainty, not indefinite offline permission. New
unclaimed work cannot start offline. Reports retain a stable operation ID and
are replayed without invoking the provider again.

An interrupted invocation never blindly restarts. Confirm its entire process
tree stopped, then:

```sh
agent-cli work recover --journal INVOCATION --acknowledge-stopped
```

Execution recovery reports interrupted/uncertain effects; a new attempt still
needs current approval, clarity, backoff, remaining failure budget, and exact
unsafe-retry approval. Intentional pause/stop does not consume the failure-retry
budget. Resume supplies the retained private checkpoint/summary in a new
authorized invocation; arbitrary vendor crash-resume is not assumed safe.

## Messages, sessions, and local knowledge

Messages are private to sender/recipient. Automatic replies handle only direct
response-requested messages; protected authentication/session notices never
become model prompts. Urgent messages get the next free slot, not a second
worker or a kill of in-flight work.

Founder issues a persistent assistance grant for an exact requester/target,
mode/purpose and optional provider/project scope. All non-Founder targets use
this unattended flow; delegates may be agents. Reconnect with the same grant
without reauthorization. The grant survives server restart/disconnects until
revoked; bounded live connections last 60-900 seconds, revalidate current
authority and close on revocation. Server restart retires live routes, not the
grant. Interactive sessions share the local worker lock.
Authentication through tmux launches the selected provider's login command.
VNC uses an existing authenticated loopback backend explicitly configured with
`vnc_address` and `vnc_verified`; the requester needs a trusted `vncviewer`
(TigerVNC-compatible) on PATH. Clipboard send/receive are disabled. No desktop
unlock, credential import, or external VNC listener is created.

```sh
agent-cli assistance grant --input grant.json  # Founder authorizes once
agent-cli assistance open GRANT --provider codex  # Explicit requester launch
agent-cli assistance open GRANT --provider codex  # Later reconnect, no reapproval
```

Terminal attachment uses Ctrl-] to close the local connection.
An unproven session cleanup keeps a local fence. Only after confirming all of
that session's processes stopped may its owner use
`work recover --session SESSION --acknowledge-stopped`.

Founder can request a bounded fresh inspection with `agent diagnose AGENT_ID
--refresh`, inspect `agent operation REQUEST_ID`, then submit `agent repair
AGENT_ID --input FILE` with the reported exact closed `session_id` and fence
SHA256. Initial remote repair requires existing durable native cleanup proof;
old/unproven Agent1 fences remain `operator-required`, not automatically recovered.
Queued/expired operations are not fresh success. The 15-minute operation lifespan
is separate from sign-in and live sessions. Repair is not a journal retry, native
restart, identity recovery or pause/approval/spending bypass. Existing approved
ai work may resume only if all independent fences permit. Inspect the operation
then diagnose again for actual progress; see the
[exact workflow and safe inputs](COMMANDS.md#founder-diagnostic-refresh-and-exact-closed-session-recovery).

`knowledge put/list/show/history` manages versioned offline `memory`, `skill`,
and `knowledge` records. `selected:true` deliberately includes an entry in
private provider context; it does not publish it to Foundation. Archiving and
selection are fields of `knowledge put`. Selected content is bounded to 64 KiB.
`inventory refresh/show` records native OS/architecture/CPU, memory, disk space,
interface counts, tool presence, and explicit availability/permission unknowns;
it does not scan networks or publish addresses/identifiers.

## Command groups

`agent-cli help` and every leaf's `--help` work offline.

| Group | Leaves |
| --- | --- |
| Setup | `config init/show/set`, `connect`, `status`, `doctor`, `mode show/set`, `preferences show/set` |
| Identity/projects | `agent list/status/revoke/register/recover/identity`, `project create/list/show/change/delegate/authorize`; status is concise non-secret team-visible activity |
| Founder control / retained access | `agent show/diagnose/repair/operation/mode/request-sign-in`, `assistance grant/list/show/revoke/open` |
| Tasks | `task create/list/show/change/history/runs` |
| Collaboration | `comment add/edit`, `worklog add`, `discussion list`, `mention list/acknowledge`, `watch [--once]` |
| Private communication | `message send/show/read/diagnose/inbox/receipt`, `session request/list/show/change/open`; explicit recipient `read` records the first read acknowledgement, peeking does not |
| Local state | `knowledge put/list/show/history`, `inventory refresh/show`, `workspace bind/list/enable/disable` |
| Providers/work | `provider status/install/update/login/link/exec`, `work prompt/status/run/recover` |
| Native manager | `service plan/install/status`; installation requires explicit `--apply` |
| Same-user identity process | `agent-identity --config PRIVATE_CONFIG prepare/connect/serve`; offline `--help`, not a guide command |
| Paired identity/runner manager | `agent-identity service plan/install/status --input OPTIONS`; no `--config`; current-user defaults, plan read-only, install needs explicit `--apply`, no root or new accounts |
| Offline guide (`agent-cli`, `agent-service`, `fdn-service`) | `guide list/show/install` |

Discussion/comment edits require the creator's current revision; there is no
task/comment/worklog deletion API. List pages are bounded to 1-200 records.
Watches use opaque actor/query-bound checkpoints and at-least-once delivery,
not per-filter read isolation.

## Backup, restore, certificates, native services

```sh
fdn-service backup-key --output /PRIVATE/OFFLINE/backup-identity
fdn-service --config /var/lib/fdn/server.json backup \
  --output /PRIVATE/BACKUPS/foundation.age --recipient age1PUBLIC_RECIPIENT
fdn-service restore --input /PRIVATE/BACKUPS/foundation.age \
  --identity-file /PRIVATE/OFFLINE/backup-identity \
  --destination /PRIVATE/NEW/foundation
```

Backups stream age-encrypted archives of a consistent SQLite hot snapshot and
TLS trust/keys with verified file hashes. Restore authenticates the full stream,
rejects unexpected/duplicate/traversing entries, checks schema/database integrity,
accepts manifest schema 1/2 through supported SQL 3 migration and rejects future
formats. Before publishing the complete fresh destination it retires sessions
and quarantines **all restored identities/owner/key epochs**: old tokens, keys
and certificates cannot regain API access. Root-local fresh Founder recovery
(retaining ID/history), then explicit Founder fresh-key reapproval of regular
agents is required. It never changes mode automatically or clears uncertain
work. OS account inventory/SSH policy, identity/provider HOMEs and native
services are not restored; full-host/native root/group qualification is separate.
Keep the private age identity off-host. Final enhanced-restore/all-suite
verification remains pending.

Server certificates last up to a year; the CA lasts ten years. Stop Foundation
and run `fdn-service --config FILE tls-renew` to replace the leaf/key while
preserving CA pins and identities. An interrupted renewal retains its exact
pair; complete it before serving. Expired CA trust requires an explicit
operator-coordinated rotation, not silent regeneration.

For the same-user worker pair, `agent-identity service plan --input OPTIONS`
is read-only without configuration/enrollment. Current-user `agent-identity
service install --input OPTIONS --apply` and `service status --input OPTIONS`
use that existing user's private configurations and fixed user-definition paths.
They never create users/groups, change HOME mode or switch users with sudo.
Both Linux units use the existing `systemctl --user` manager; both macOS
services are GUI LaunchAgents for the same user. Windows native adapters remain
unimplemented. Legacy split-account options require explicit stopped migration
while retaining keys and all runner state. See
[DEPLOYMENT.md](DEPLOYMENT.md#native-identity-and-runner-services) for complete
options/prerequisites and explicitly unperformed native qualification.

The existing single-role `service plan --input OPTIONS` renders systemd (Linux)
or launchd (macOS agent) without changing the host. Its `service install --input
OPTIONS --apply` explicitly registers/starts that role; Foundation's system
registration requires OS administrator execution. Single-role options are in
[platform.Options](internal/platform/service.go). Foundation installation needs
its reviewed ingress supplementary group and writable socket directories;
see [DEPLOYMENT.md](DEPLOYMENT.md). Accounts/configuration must already be
prepared. Only the separately authorized root helper reconciles the managed
SSH namespace; ordinary services are not general-purpose root/SSH/firewall/
desktop administration. Use one identity-service process and one runner process
per computer under the same existing user. On Linux, invoke user-service
registration as that actual user with its
active user manager (`XDG_RUNTIME_DIR` and, where set,
`DBUS_SESSION_BUS_ADDRESS`). These two bus variables are passed only to the
user-service registration commands; provider processes do not inherit them.
Linux unit tests also invoke `systemd-analyze verify` when available, without
registering or starting a service, to check the rendered unit syntax.

## GitHub Actions and packaging

* [Testing](.github/workflows/tests.yml) runs **separate static, unit,
  integration, end-to-end, and stress jobs** for PRs into `main` on creation,
  updates, reopening, and readiness for review. Native suites run on Linux and
  macOS runners. No VM/container management is used.
* [Packaging](.github/workflows/package.yml) runs **only when a PR is merged
  into `release`**, checking out the exact merge commit. Unmerged closures skip
  packaging. Direct pushes, tags, manual dispatch, and PR opens cannot package.
  The packaging program independently checks that same event/commit contract.
* Targets are Linux amd64/arm64 and macOS arm64 (`darwin-arm64`).
  Each job compares two independent trimmed, CGO-free binary builds, produces
  a native archive containing `agent-cli`, `agent-service`, `agent-identity` and
  `fdn-service`, metadata/build list, third-party notices, a complete version-matched
  operational/development guide with all six skills, and `SHA256SUMS`. Archive
  checksums cover the identity binary along with every payload. Linux also
  produces separate Foundation and Agent Debian packages; the worker package
  includes `agent-cli`, `agent-service` and `agent-identity`. Foundation's Debian
  package provisions only its dedicated service account; it does not bootstrap
  or start an unconfigured service. Debian payloads include deterministic parent
  directory entries and pass unprivileged `dpkg-deb --extract` verification,
  including the installed license notices, before upload.
* Artifacts inherit repository access. No GitHub Release, signing, notarization,
  attestation, or deployment is silently performed. Local tests create tiny
  synthetic archive fixtures, **not release packages**. Real packaging is
  deliberately refused outside the merged-release Actions job.

After downloading the target artifact, get the `SHA256SUMS` digest independently
from the trusted Actions run summary/approved channel. Do not trust a hash
downloaded beside an otherwise untrusted bundle.

```sh
sh scripts/install.sh --bundle /DOWNLOADED/ARTIFACT \
  --manifest-sha256 INDEPENDENTLY_TRUSTED_SHA256 \
  --prefix /usr/local --acknowledge-stopped
```

The installer privately copies and verifies the manifest/listed files before
extraction, rejects unlisted archives and unsafe entries, preserves immutable
releases, and switches one activation link. Stop all affected services/work
first. Configuration, identities, knowledge, workspaces, and outboxes are never
replaced. Upgrades/rollback activation and subsequent service startup are
explicit operator actions, not automatic privileged maintenance.
