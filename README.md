# Foundation in Go

A self-contained Go implementation of Foundation through **Phase 5**, with an
authoritative SQLite-backed service, a native worker daemon, and one structured
CLI for people and coding agents. Development and operation need no companion
checkout. Existing legacy configuration, credentials, and databases are **not
imported or overwritten**.

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
| 2 | One-time Founder bootstrap, expiring invitations, delegated sponsorship, project-creation grants, revocation, heartbeat, pinned OpenSSH transport, native service definitions, encrypted database/trust backup and fresh-directory restore. |
| 3 | Projects, collaborative tasks, assignment, named/independent approvals, verification/rework, three-history retention/revert with reasons, creator-owned discussions/worklogs, mentions, paginated watches, offline private knowledge, capability inventory, explicit workspace authorization. |
| 4 | Codex/Copilot adapters, private messages, urgent inboxes, staged provider installation/updates, protected login handoff, expiring target-consented tmux/existing-VNC sessions. |
| 5 | Shared manual/automatic controller and prompt, first capability review, scope/acceptance refinement, actual-answer incorporation, exact-specification signoff requests, preflight, serial claims, supervised processes, pause/checkpoints, durable report outbox, restart reconciliation, unsafe-retry approval/backoff, idle research. |

Files/diaries/shared skill publication, recurring-child generation, and full-host
disaster-recovery automation from the original Phases 6-8 are outside this
milestone. Repetitive parents are recognized but cannot be executed.

## Architecture and boundaries

* `fdn-service` is the only SQLite writer exposed to computers. Its TLS API owns
  enrollment, roles, task authority, transaction boundaries, and run fences.
  Clients cannot bypass authorization with database writes or bulk endpoints.
* `agent-service` runs directly under the worker's dedicated OS account.
  `agent-cli work run` uses the **same controller, prompt, local slot, and server
  checks**, not a separate privileged execution path.
* SQLite uses WAL, full synchronization, foreign keys, indexed assignment/state
  queries, keyset pagination, and bounded connection pools. It is intentionally
  a single-authority, single-writer design, not a distributed SQL system.
* All listeners bind to loopback. Remote computers use an existing OpenSSH
  server with explicitly pinned `known_hosts` and an individual identity file.
  TLS remains mandatory inside the tunnel. No first-connection trust bypass,
  shell-expanded SSH commands, environment proxy, or plaintext HTTP fallback.
* Application identities are not Foundation-host shell accounts. Unlike the
  old direct-CouchDB design, enrollment needs no per-agent privileged account
  provisioning. Provision the service account and restricted SSH transport
  separately through the operating system.
* One dedicated worker account/configuration/service belongs on each computer.
  OS locks serialize daemon/manual work and interactive sessions; server-side
  ownership additionally prevents competing task claims for one identity.
  This is coordination, not hardware attestation or protection from a malicious
  administrator cloning credentials.
* Enrolled agents can read shared projects/tasks/discussions. Messages and
  session records are API-private to their parties. Raw provider output, login
  credentials, workspace paths, discovery details, and local knowledge stay
  under owner-only local state unless deliberately included in a shared summary.

See the [domain contracts](internal/domain/), [server policy](internal/foundation/),
[HTTP protocol](internal/api/), and [controller](internal/work/).

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
recovery. Integration tests exercise real OpenSSH forwarding, wrong host pins,
TLS, raw unauthorized writes, competing claims, consent withdrawal, orphan
claims, and lost-acknowledgment replay without repeated inference.

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

## Initialize Foundation

Use a dedicated unprivileged account. The Foundation deployment target is Linux;
the server also runs unprivileged on macOS for local development/tests.

```sh
fdn-service --config /var/lib/fdn/server.json init \
  --state /var/lib/fdn --listen 127.0.0.1:8443 \
  --bootstrap-file /var/lib/fdn/bootstrap.json
fdn-service --config /var/lib/fdn/server.json serve
```

The account must already own its state directory, with mode `0700`. Private
configuration, identity files, keys, locks, and journals use `0600`.
Initialization retains a private preparation record so an interrupted operation
can be retried with the original paths. Reinitialization does not reset state.

Obtain the public `ca.pem` and one-time bootstrap bundle through a trusted
out-of-band channel. Do not put bootstrap/invitation contents in chat, command
arguments, commits, or shared task records. Bootstrap expires after 15 minutes.
Before Founder enrollment only, an administrator may issue a replacement:

```sh
fdn-service --config /var/lib/fdn/server.json bootstrap \
  --output /var/lib/fdn/replacement-bootstrap.json --ttl 900
```

Enrollment consumes the credential transactionally. Retrying an uncertain
enrollment preserves the original operation and client-generated credential;
it does not create a second computer.

## Configure and enroll a worker

Run under the computer's dedicated local account. Place the trusted public CA
in a regular account/root-owned file without group/other write permission.

```sh
agent-cli --config "$HOME/.config/fdn/agent.json" config init \
  --state "$HOME/.local/share/fdn" \
  --url https://localhost:8443 --ca-file "$HOME/.config/fdn/ca.pem"
agent-cli --config "$HOME/.config/fdn/agent.json" connect \
  --invitation-file "$HOME/.config/fdn/bootstrap.json" --name Founder
```

Founder bootstrap defaults to `manual`; ordinary invitations default to `ai`.
The Founder or a current project delegate creates subsequent invitations:

```sh
agent-cli invitation create --output /PRIVATE/PATH/invitation.json --ttl 900
```

A different computer enrolls with that bundle using its own configuration,
local account, CA pin, and client-generated credential. Do not copy an enrolled
computer's credential or a provider's login state to another identity.

For remote transport, `config set --input FILE` accepts an `ssh` object:

```json
{
  "ssh": {
    "host": "foundation.example",
    "user": "worker-tunnel",
    "port": 22,
    "identity_file": "/PRIVATE/PATH/ssh-key",
    "known_hosts_file": "/PRIVATE/PATH/known_hosts",
    "local_port": 8443,
    "remote_port": 8443
  }
}
```

The SSH account/forwarding restrictions and initial host pin are operator
prerequisites, not an unauthenticated enrollment shortcut. Agent Service
reconnects with bounded backoff using the same pins and identity.

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
do not replace the revision, mint another invitation, or re-run a provider.
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

```sh
agent-cli preferences set --input preferences.json
agent-cli mode set ai
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

Coding calls explicitly preapprove local tools; text-only review/selection/
responses do not receive coding tools. These settings are not an OS sandbox,
permission to spend/contract, or consent to another computer. The shared
[prompt](internal/work/prompt.txt) keeps financial/legal decisions, private
material, checkpoints, and authority boundaries explicit.

`manual` stops automatic inference/execution while retaining reconciliation.
Already-running approved work may continue during a connection outage; new
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

Sessions require current Founder/project-delegate authority, a target's exact
per-session consent, a separate explicit requester open action, and expiry
within 60-900 seconds. Revocation/expiry closes real relay streams; server
restart retires consent. Interactive sessions share the local worker lock.
Authentication through tmux launches the selected provider's login command.
VNC uses an existing authenticated loopback backend explicitly configured with
`vnc_address` and `vnc_verified`; the requester needs a trusted `vncviewer`
(TigerVNC-compatible) on PATH. Clipboard send/receive are disabled. No desktop
unlock, credential import, or external VNC listener is created.

```sh
agent-cli session request --input request.json
agent-cli session change SESSION --input consent.json
agent-cli session open SESSION  # Explicit human terminal only
```

Terminal attachment uses Ctrl-] to close the local connection.
An unproven session cleanup keeps a local fence. Only after confirming all of
that session's processes stopped may its owner use
`work recover --session SESSION --acknowledge-stopped`.

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
| Identity/projects | `agent list/revoke`, `invitation create`, `project create/list/show/change/delegate/authorize` |
| Tasks | `task create/list/show/change/history/runs` |
| Collaboration | `comment add/edit`, `worklog add`, `discussion list`, `mention list/acknowledge`, `watch [--once]` |
| Private communication | `message send/show/inbox/receipt`, `session request/list/show/change/open` |
| Local state | `knowledge put/list/show/history`, `inventory refresh/show`, `workspace bind/list/enable/disable` |
| Providers/work | `provider status/install/update/login`, `work prompt/status/run/recover` |
| Native manager | `service plan/install/status`; installation requires explicit `--apply` |
| Offline guide (all binaries) | `guide list/show/install` |

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
retires sessions, and publishes only a complete fresh destination. It does not
overwrite existing state, clear uncertain run ownership, restore OS accounts,
or claim full-host recovery. Keep the private age identity off the backup host.

Server certificates last up to a year; the CA lasts ten years. Stop Foundation
and run `fdn-service --config FILE tls-renew` to replace the leaf/key while
preserving CA pins and identities. An interrupted renewal retains its exact
pair; complete it before serving. Expired CA trust requires an explicit
operator-coordinated rotation, not silent regeneration.

`service plan --input OPTIONS` renders systemd (Linux) or launchd (macOS agent)
definitions without changing the host. `service install --input OPTIONS
--apply` explicitly registers/starts them; Foundation's system registration
requires OS administrator execution. Options are in
[platform.Options](internal/platform/service.go). The account/configuration must
already exist. Normal service startup does not provision root accounts or
change SSH/firewall/desktop policy. Use one registration per worker computer.
On Linux, invoke agent service registration as the dedicated account with its
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
  a native archive, metadata, third-party notices, a complete version-matched
  operational/development guide with all six skills, and `SHA256SUMS`; Linux also
  produces separate Foundation and Agent Debian packages. Foundation's Debian
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
