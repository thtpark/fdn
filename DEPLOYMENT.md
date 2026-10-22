# Native deployment and operating runbook

Use the matching [skill](AGENTS.md), [command catalog](COMMANDS.md) and current
binary help. This guide is complete without a development/legacy checkout.
Installation, enrollment, provider authentication, local tool permission, mode
and task approval are distinct states.

## Plan and prerequisites

Before writes, establish target machine/role, OS account/HOME, approved source/
version, cost/privilege boundaries, backup/recovery route and desired final mode.
Do not create billed cloud resources or alter a running host merely because a
sample command or a key file is available.

- Foundation: native Linux, dedicated unprivileged account (Debian default `fdn`,
  home/state `/var/lib/fdn`), signed distribution packages for OS prerequisites.
- Worker, including Founder's computer: distinct non-root identity-service and
  provider/tool-runner accounts, UIDs, private state and HOMEs. `agent-identity`
  owns long-term keys; `agent-service` and providers run only as the runner.
  Neither Debian worker installation nor a display name creates these accounts.
- Foundation ingress: a unique managed SSH account/UID per enrolled identity,
  including Founder, separate from the unprivileged `fdn` server account.
  A narrow, separately configured root Unix provisioner manages only this
  account/key/forced-command namespace; the server never runs as root.
- Runtime: no Go/Rust build toolchain, database daemon, Docker or tests on the
  target. Codex/Copilot management needs a supported native Node/npm runtime.
  Use a trusted distribution or checksum/signature-verified upstream runtime,
  not an unreviewed curl-to-root installer.
- Network: private agent-to-Foundation SSH with an individually pinned host key,
  each agent's own SSH key/account and a fixed stdio gateway. Inner TLS is
  mandatory on Unix API/join ingress; no API TCP listener or bearer bootstrap.
  Outbound provider HTTPS and explicitly authorized loopback desktop/relay
  endpoints are separate from Foundation enrollment.
- Keep cloud metadata credentials least-privileged; prefer no VM service account
  for workers. A dedicated Linux account does not isolate broad cloud metadata
  credentials. Preserve OS Login, organization policy and recovery access.
- GCE display attachment is separate from a desktop/VNC server. Enable the
  virtual display only on the requested worker; do not claim an enabled display
  provides a GUI, desktop consent or an authenticated VNC backend.

Use approved IAP/operator SSH where applicable. Authenticate host keys from
trusted provider/operator metadata, never accept an unknown key or scan it as a
trust-establishment shortcut. Signed registration reserves a dedicated
Foundation-host account; the narrow root provisioner applies only that managed
namespace. Never substitute a shared SSH account, a same-UID provider/identity
service, copied bearer credentials or relaxed TLS.

## Obtain and verify the release

Only a PR merged into `release` creates real packages. Download the appropriate
Actions artifact and independently obtain the `SHA256SUMS` digest from the trusted
run/approved channel. Do not trust a digest downloaded beside an untrusted bundle.
Verify exact source/version/platform and every selected payload.

Targets: Linux amd64/arm64 and macOS arm64. Each artifact contains a native
archive with `agent-cli`, `agent-service`, `agent-identity` and `fdn-service`,
checksums and `release.json` with the build list. Linux adds Foundation/Agent
Debian packages; the worker payload includes CLI, runner and identity service.
Archive/package checksums cover those binaries, the guide and notices; extracted
Debian executables are also checked against the reproducible build bytes/modes.
Packages are unsigned/unattested; do not call them signed or notarized.

For a native archive:

```sh
sh scripts/install.sh --bundle /VERIFIED/ARTIFACT \
  --manifest-sha256 TRUSTED_DIGEST --prefix /usr/local \
  --acknowledge-stopped
```

The reviewed script comes from the same source as the verified release. It
privately copies/verifies listed inputs, rejects unsafe/missing archive contents,
retains immutable releases and switches one activation link. It does not reset
state, stop work, register services, enroll, authenticate or authorize inference.

On Debian, verify the selected role package against the trusted manifest, then
use `dpkg -i`. The Foundation package creates/checks its dedicated account/state
but never bootstraps or starts an unconfigured service. Role-specific
`/usr/share/doc/fdn-foundation/guide` and `/usr/share/doc/fdn-agent/guide` avoid
package ownership conflicts. The packaged Foundation unit expects ingress group
`fdn_ingress` and writable socket parent `/run/fdn-ingress`, distinct from its
`fdn` database group/state. Match the approved helper configuration, or explicitly
regenerate the reviewed unit for different actual names/paths; installation does
not itself provision ingress or start the root helper.

## Ensure operating knowledge is installed

`agent-cli`, `agent-service` and `fdn-service` expose `guide list/show/install`
offline. The isolated `agent-identity` binary instead has offline `--help` for
private `prepare`/`connect`/`serve` and paired `service plan/install/status`.
Run guide installation as the actual owner:

```sh
agent-cli guide install
agent-cli guide show AGENTS.md
agent-cli guide show .agents/skills/foundation-work/SKILL.md
```

Foundation-only hosts can use `fdn-service guide install` and equivalent show
commands. A non-login Foundation service account is not a coding agent; install
the guide separately for the authorized operator account that uses an AI.

Worker `config init` and daemon startup also install/update their own guide.
The managed guide lives below `$HOME/.local/share/fdn/guidance`, with immutable
content/source manifests and an atomic `current` link. Portable/Codex discovery
uses `$HOME/.agents/skills`; Copilot discovery also uses `$HOME/.copilot/skills`.
A managed section in HOME's `AGENTS.md` points to the current guide. Existing
instructions and unrelated skills survive. Unmanaged name collisions or changed
managed content cause an explicit conflict, not an overwrite.

Discovery differs by provider/version. The controller additionally embeds bounded
operating guidance in manual and daemon prompts; tool-disabled calls cannot
fetch files and still receive the contract. Skills guide behavior, never grant
authority, run tools, or authenticate automatically.

## Prepare protected accounts and ingress

This is the API/schema 2 software-first implementation contract, not evidence
of a completed native installation. Rootless/recording tests do not qualify
real multi-UID users, sshd, systemd/launchd or reboot behavior. No hardware
adapter, attestation or perfect physical-clone prevention is implemented.

An authorized administrator prepares the unprivileged Foundation account,
private state, ingress group/socket parents, immutable gateway binary, and two
worker-side accounts (identity service and runner). Every identity, including
Founder, also gets its **own** Foundation-host SSH account/UID. Never share that
SSH account with another identity or give it a normal shell.

The administrator configures the narrow root account service and forced gateway
before initialization. These are distinct protected configurations:

| Configuration | Fields / boundary |
| --- | --- |
| Account service | Root-owned `platform.AccountServiceConfig`: `socket`, explicit `apply:true`, `accounts` (`platform.AccountOptions`); Unix caller must be the pinned server UID |
| Forced gateway | Root-owned `ingress.ProxyOptions`: `api_socket`, `join_socket`, `server_uid`; selects only the fixed API/join stream under the calling agent UID |

`accounts` uses these exact fields: `state_dir` (private root inventory,
distinct from Foundation state), `policy_dir` (root-controlled public policy),
`gateway_binary` (immutable root-owned absolute binary), `api_socket`,
`join_socket`, `server_uid`, `server_group`, `server_gid`, `ingress_group`,
`ingress_gid`, reserved `uid_min`/`uid_max`, `sshd_config` and `sshd_service`.
Pin the actual server UID/database group, a different ingress-only group and an
approved noncolliding UID range; do not infer them from a display name. The
administrator must include `policy_dir/sshd.conf` in the selected sshd config;
managed account activation checks effective sshd policy before enabling access
and reloads only the explicitly selected native SSH service.

The helper's internal typed provision request is `agent_id`, `public_key`,
positive key `generation` and optional `reactivate`; disable is `agent_id` plus
positive `generation`. These are server-to-root Unix RPC fields, **not new CLI
flags or root methods**. Private inventory retains each generation: stale
lower-generation or same-generation different-key work is rejected before any
OS command; exact same-generation replay is idempotent. A newer disable leaves
a retained tombstone even if the account was never created, preventing an old
queued provision from resurrecting it. Explicit higher-generation fresh-key
recovery retains a provisioned account's UID/name. Registry/request/key/generation
and returned/post-bind/current tuples are checked by the server around the RPC.
Do not repair a generation mismatch by editing inventory, replaying an old key
or guessing a higher generation; use the authorized reconciliation/recovery
route and preserve both inventories/evidence. Root inventory format remains 1
(first/unshipped native format), but every retained account requires positive
int64 `generation`; missing/zero values are rejected, not inferred or reset.
Keyless UID0/GID0 disabled/retiring tombstones for never-provisioned identities
are **not root OS bindings**. Their later fresh higher-generation approval can
allocate only the same identity/name; already provisioned UID/name stay retained.

Use current native types/help, not a generic root runner. The root inventory is
private; gateway policy and authorized public keys are root-controlled. The
account-helper socket is accessible only to the server's own group/UID, not the
agent ingress group. The ingress-only group
must differ from the server's primary database group. The root helper grants
only the pinned server UID supplementary ingress membership and prepares the
protected server-owned/setgid socket parents. Each tunnel account uses only
its ingress primary group, with no database/supplementary groups. Restart with
fresh OS groups or use an explicitly reviewed systemd supplementary-group
definition before init/serve: editing a group file does not change a running
process's groups. Root-reviewed sshd policy
forces `fdn-service gateway --input ROOT_CONFIG`, permits only public-key
`fdn-api`/`fdn-join` stdio, and forbids arbitrary commands, PTYs and all
forwarding. The gateway cannot select root maintenance.

```sh
# Explicit authorized root operation on Foundation, after reviewing ROOT_CONFIG:
fdn-service accounts serve --input /ROOT/PROTECTED/accounts.json
```

The main server never runs as root. Its API/join listeners use controlled group
Unix sockets with inner TLS. The separate maintenance socket stays below private
0700 state with mode 0600 and accepts root-local recovery only. Do not expose any
of them through TCP, a general-purpose forwarding account or a bearer fallback.

## Prepare Founder and initialize Foundation

An authorized setup supplies the identity account's private configuration with
absolute `state_dir`/`socket` and explicit runner `worker_uid`/`worker_gid`.
There is no `agent-identity config init` command. Keep its 0700 private key state
separate from the runner HOME/state and from the shared IPC parent. The IPC
parent is identity-owned 0710 with the runner group; the proxy socket is 0660
and additionally checks kernel runner UID/GID. Native identity execution rejects
root and the runner's own UID.

As Founder's protected identity-service account, with that service stopped:

```sh
agent-identity --config /PRIVATE/identity.json prepare \
  --name Founder --output /PUBLIC/founder-request.json
```

Transfer **only the signed public request** through an authenticated channel.
The private SSH/authentication keys never leave the identity account. As the
unprivileged Foundation account, while the approved root account helper runs:

```sh
fdn-service --config /var/lib/fdn/server.json init \
  --state /var/lib/fdn --founder-request /PUBLIC/founder-request.json \
  --ingress-socket /run/fdn-ingress/api.sock --join-socket /run/fdn-ingress/join.sock \
  --ingress-group INGRESS_GID --account-socket /run/fdn-accounts/accounts.sock
fdn-service --config /var/lib/fdn/server.json doctor
```

`INGRESS_GID` is numeric. Server state/configuration/keys remain 0700/0600.
Initialization returns `initialized`, `changed`, `config`, `ca_file` and the
public `identity`; it has **no** profile-output flag. An authorized operator
assembles an integrity-protected public `config.ConnectionProfile` with that
identity, CA PEM from `ca_file`, `url`, independently pinned `ssh_host`,
`ssh_port` and `known_hosts`. Never establish a host pin by accepting an unknown
key. The URL is the inner TLS origin, not a public TCP/bootstrap endpoint.

If provisioning is unavailable, initialization retains its config, PKI, receipt
and pending identity and reports a prerequisite failure. Retry the exact
initialization or reconcile through `serve`; do not delete preparation records,
create another Founder or turn on bearer bootstrap.

Use reviewed native service options (absolute paths, role `foundation`, user
`fdn`, OS `linux`, `ingress_group` set to the approved ingress-only group name,
and `ingress_dirs` containing the actual API/join parent paths, e.g. `["/run/fdn-ingress"]`)
to plan and explicitly register the unprivileged server. These fields are
required for native installation: `SupplementaryGroups` supplies ingress access
and `ReadWritePaths` permits socket creation under `ProtectSystem=strict`.

```sh
fdn-service --config /var/lib/fdn/server.json service plan --input service.json
# Explicit OS administrator execution:
fdn-service --config /var/lib/fdn/server.json service install \
  --input service.json --apply
```

Verify actual manager/process owner, source/version, SQLite integrity, Unix
socket ownership/modes, root-helper caller restrictions and pinned SSH/inner TLS.
Registration acceptance is not final health or native qualification.

## Register signed worker keys and attach the runner

For Founder, use the operator-assembled public profile. For a subsequent
computer, its protected identity account first runs `prepare --name Agent1
--output PUBLIC_REQUEST` using its own private service configuration. Founder
alone approves that request:

```sh
agent-cli agent register --input /PUBLIC/agent-request.json \
  --output /PUBLIC/agent-profile.json
```

Delegates cannot register agents, even if they can authorize project work.
Public requests/profiles contain no private keys, but must retain authenticated
provenance/integrity. Exact retries preserve the same request/operation/keys;
public-key collisions and unproved attempts are rejected without banning an
existing owner. A display name or provider login is not enrollment.

As the target's identity-service account, with that service stopped for the
offline profile operation:

```sh
agent-identity --config /PRIVATE/identity.json connect \
  --profile /PUBLIC/agent-profile.json
agent-identity --config /PRIVATE/identity.json serve
```

`connect` validates/saves the approved profile; `serve` performs fresh key-bound
activation, obtains the device certificate and maintains `/v1/identity/owner`
over the join channel. Ephemeral owner certificates authorize ordinary API;
the kernel ingress UID, instance/agent/key/owner generations must all agree.
The provider runner never receives these certificates or their private keys.

As the **different runner account**, initialize only its local proxy binding:

```sh
agent-cli --config /PRIVATE/agent.json config init \
  --state /PRIVATE/runner-state --identity-socket /run/fdn-agent/identity.sock \
  --workspace-root /OWNED/workspaces
agent-cli --config /PRIVATE/agent.json connect
agent-cli --config /PRIVATE/agent.json agent identity
agent-cli --config /PRIVATE/agent.json doctor
```

The runner retains only public AgentID. Verify matching identity/role/account
bindings, owner state and current authoritative mode. Founder starts manual;
regular agents run ai unless Founder explicitly pauses them. Installation,
provider sign-in and local workspace-tool grants are still separate.

## Native identity and runner services

There are two worker-side registrations, not one same-UID daemon: the protected
`agent-identity --config PRIVATE_CONFIG serve`, and the distinct runner's
`agent-service --config RUNNER_CONFIG serve`. Their supported paired lifecycle
uses **no `--config` or enrollment**:

```sh
agent-identity service plan --input /PROTECTED/identity-services.json
# Explicit authorized root execution on the selected native OS:
agent-identity service install --input /PROTECTED/identity-services.json --apply
agent-identity service status --input /PROTECTED/identity-services.json
```

`plan` is read-only without root; `install` requires explicit `--apply`; `status`
requires native root but no apply flag. Input is a protected public options file,
not stdin. This is different from the runner-only `agent-cli service` interface.
These paired lifecycle commands never create/adopt accounts or silently fall
back to one UID.

`platform.IdentityServiceOptions` uses these exact JSON fields:

| Fields | Contract |
| --- | --- |
| `os`, `name` | Native `linux`/`darwin` and stable managed installation name |
| `identity_user`, `identity_uid`, `identity_gid`, `identity_home` | Protected non-root identity account; positive UID/GID distinct from runner, matching named primary group, private 0700 HOME |
| `worker_user`, `worker_uid`, `worker_gid`, `worker_home` | Distinct provider runner with its own matching named primary group, UID/GID and private 0700 HOME |
| `identity_binary`, `worker_binary` | Immutable absolute paired `agent-identity`/`agent-service` binaries in the same directory |
| `identity_config`, `worker_config` | Preexisting owner-only 0600 files under their respective private HOMEs; identity/runner socket and UID/GID bindings must match the plan |
| `state_dir`, `worker_state_dir` | Separate private 0700 state under each account's HOME; identity key state can be a fresh generation-specific subdirectory |
| `socket_dir` | Separate identity-owned 0710 runner-group IPC parent outside both HOMEs/private states; identity proxy socket 0660 |
| `identity_definition`, `worker_definition` | Exact native paths below, not arbitrary root write targets |

Provision exact accounts/UIDs/HOMEs/groups first, with OS comment/RealName markers
`fdn-identity=NAME` and `fdn-runner=NAME`. The identity shell is
`/usr/sbin/nologin` on Linux or `/usr/bin/false` on macOS; the runner uses the
planned native shell. Identity supplementary membership in the worker group is
required for unprivileged IPC ownership changes; reverse worker membership in
the identity/root group is forbidden. Missing/mismatched state stays an explicit
prerequisite/conflict, never successful adoption. The installer uses fixed
absolute native arguments and a minimal environment, not a generic root helper.

| Native OS | Exact definition paths (`NAME`, `WORKER_HOME` are plan values) |
| --- | --- |
| Linux identity | `/etc/systemd/system/fdn-identity-NAME.service` |
| Linux runner | `WORKER_HOME/.config/systemd/user/fdn-worker-NAME.service` |
| macOS identity | `/Library/LaunchDaemons/org.foundation.identity.NAME.plist` |
| macOS runner | `WORKER_HOME/Library/LaunchAgents/org.foundation.runner.NAME.plist` |

Linux installation registers the non-root identity system unit, enables the
runner's lingering/user manager, then runs `systemctl --user` as that actual
runner, never as root. Its user-bus environment is for registration only, not
provider processes. Keep Linux identity HOME outside `/home`, `/root` and
`/run/user` because `ProtectHome` hides those paths. Identity unit HOME,
working/read-write root remain the stable identity HOME so explicit fresh
sub-StateDir recovery need not change accounts or runner paths.

macOS registers the identity system LaunchDaemon and uses `launchctl asuser`
with a nonprompting privilege drop to the actual runner for its GUI LaunchAgent.
No system daemon gains desktop consent merely by registration. Account/GUI
prerequisites must already exist; fixture plans/recorded commands are not proof
of actual multi-UID, manager, GUI or reboot qualification.

After explicit registration, verify status and the runner's own identity/health:

```sh
agent-cli --config /PRIVATE/agent.json agent identity
agent-cli --config /PRIVATE/agent.json doctor
```

Mode is the single automatic-work control. Ordinary `ai` mode is not spending
permission, provider authentication, readiness or a workspace tool grant.
Configure/approve each independently. Set an explicit local workspace grant
with `workspace bind --path OWNED_DIR --allow-tools`, or authorize generated
owned project directories through `--allow-workspace-tools`.

All non-Founder workers are unattended: no workflow may wait for a person at
the target. Only an authenticated Founder request may pause/resume their global
mode. From Founder use `agent mode AGENT --input FILE` with mode and reason;
the worker mirrors the server decision and handles cancellation automatically.
Targets and delegates cannot self-set manual or bypass it with manual work.
`mode set ai|manual` is now Founder-only for its own mode, not a local worker
override. Real OS emergency stops remain OS actions, not a fabricated mode grant.

Upgrade Foundation before workers: API/schema is 2, server SQLite migration 3,
local SQLite remains 1. Migration 2's mode/assistance attribution is retained;
migration 3 adds identity/account/key/generation fences without erasing old
agents, tasks or operation evidence. Old bearer files cannot run v2. An active
Founder binds a designated original non-Founder with `agent register --input
PUBLIC_REQUEST --existing-agent AGENT [--output PUBLIC_PROFILE]`; then that
runner explicitly uses `connect --migrate` against its separate identity proxy.
This replaces only the matching legacy credential file with public AgentID and
preserves knowledge, journals, bindings and outbox. A legacy Founder must first
use the authorized root-local recovery route with fresh keypairs. Never migrate
by copying another computer's keys or resetting its database.

Existing Founder defaults to manual, regular agents to ai. An unexplained old
local manual pause is blocked rather than silently resumed; Founder explicitly
reconciles it. Old servers without authoritative mode/identity metadata cannot
run a new worker.

## Providers and protected handoffs

Install exact approved versions or explicitly update through the CLI. Managed
staging disables install scripts, locks integrity, verifies npm registry
signatures and checks required adapter flags before activation; failures retain
the previous version. Do not bypass checks with a guessed replacement provider.

Read `provider status`. Codex credentials-present-unverified is not proof of
live authentication. Copilot uses bounded read-only auth/model RPC. Neither
status invokes paid inference.

Providers are installed in private version directories, not globally with npm.
Install/update and daemon startup expose stable `codex`/`copilot` commands in the
account's `.local/bin` and preserve a managed PATH block in its login profile.
Verify the actual login shell with `command -v codex`, `command -v copilot`,
`codex --version`, and `copilot --version`, not only absolute managed paths.
For older installations run `agent-cli --config FILE provider link` as the worker
and reconnect (or export `.local/bin` in the current shell). Never use global
reinstallation or another account's credentials to fix a PATH omission.
Launchers resolve the active version on each call; interactive native sessions
share the local slot and require the human's terminal. Version/help probes are
non-inference checks.
Copilot's self-updater stays disabled in native and remote login launches; all
version changes use Foundation's verified provider install/update path.

Before sign-in, inspect mode/current work and spending authority. Founder is the
human-help coordinator; never expect a local human at a non-Founder worker.
Delegates may be AI agents and can arrange authorized automated steps, but must
route human-only credential/consent actions to Founder. If authentication could
unlock unapproved inference, Founder explicitly requests manual mode on the
target and later explicitly resumes it. No delegate or target changes mode.
Protected credential UI/input/output remains human-only through the authorized
requester's visible session; never copy credentials or capture login output.

Missing authentication is not only a local log entry. Before a selected
provider invocation (including initial capability review), the controller
durably requests sign-in from the active Founder and any relevant task-project
delegates. The protected private message contains the target/provider/request
references, supported human-terminal commands and explicit consent/spending
boundaries. It expires 15 minutes after creation, without automatic renewal;
delivery/restart does not extend it. It does not need a model, create a session,
assume target consent, change mode or perform login.

Read the request from Founder's `message inbox` or `message show MESSAGE` using
Founder's own identity, including when Founder is manual. Agent1's `work status`
shows `authentication_requests`, delivery/message references and any unresolved
delivery failure. The same episode/proposal replays after reconnect or a lost
acknowledgement instead of spamming new requests. Unknown provider status stays
an explicit diagnostic, not a guessed sign-in request. Acknowledgement is not
authentication; a successful readiness observation resolves the local episode
without editing the recipient's old message receipt.

After expiry, Founder uses `agent request-sign-in AGENT --input FILE`, supplying
`provider`, a non-secret `reason` and optional matching `project_id`. Use a stable
new operation ID for this intentional refresh; reuse it for an uncertain retry.
The server queues a protected urgent control, also valid for 15 minutes. The
unattended target processes it under its own identity, including in manual mode,
and only sends a fresh superseding notice if a read-only probe still explicitly
requires sign-in. Check the control's receipt and the target's work status; an
offline/fenced target may need another explicit request after control expiry.
No login, provider/device-code renewal, mode change, model call or assistance
session is initiated. Persistent assistance grants remain separate and valid
until revoked; their live connections and provider codes have their own expiry.

Founder authorizes assistance once with `assistance grant --input FILE`. Scope
is the exact requester/target, tmux/VNC, authentication/terminal purpose, optional
provider, and optional project. Explicit computer-local grants work before a
project exists; project-scoped grants additionally require current project
authority. The requester may be an AI delegate, not a presumed human.

The grant has no timed expiry and is not consumed by a session. It persists
across restarts/disconnects until Founder revokes it. Reuse
`assistance open GRANT --provider codex` on the authorized requester's computer
without reauthorization; the unattended target prepares the granted session.
Only the protected UI itself requires Founder-coordinated human participation.
Opening is a launch action, not another consent/authorization decision.

Live connections remain bounded to 60-900 seconds, close on expiry/revocation,
and recheck active identities, scope and current project authority. Restart
closes routes, not the grant. Closing a connection does not revoke future access;
`assistance revoke GRANT --input FILE` does. Broad terminal access needs its own
explicit purpose grant; authentication permission is not a shell grant. Existing
no-grant per-session consent is retained only for Founder targets.
VNC still needs a verified authenticated loopback backend and trusted viewer;
granting access does not install/unlock a desktop. Messages are never grants.

## Upgrade and rollback

1. Capture version, identity references, mode, service state, pending journals/
   reports/session fences and current work. Do not record secret contents.
2. Verify release source/digests and schema compatibility. Obtain an approved
   backup/recovery point. Resolve active operations before replacing binaries.
3. Stop/join the native worker/service; preserve configuration, credentials,
   providers, private knowledge, workspaces, bindings, guides and outbox. A mode
   change is not implied; only Founder explicitly requests any maintenance
   pause/resumption, and the worker applies that authoritative decision.
4. Install the new verified package/bundle. Do not overwrite immutable releases
   with different content or reset HOME/state. Refresh the operator's guide and
   let worker startup refresh its own discoverable guide.
5. Start the service; verify exact version/process owner, unchanged identity/
   mode, TLS/tunnels, knowledge/guide hashes, pending recovery state and health.
   A failed startup stays failed; do not clear fences to make a check green.
6. Reboot/reconnect only within authorization. Report observed results and
   pending auth/approval separately from successful installation.

The current CLI has no general `upgrade`/automatic rollback command. Native
package/activation changes are explicit OS operations. A binary rollback must
not read a future schema; use an approved compatible binary or a deliberate
fresh-directory restore. Do not destroy newer uncertain evidence.

## Backup, restore and TLS lifecycle

```sh
fdn-service backup-key --output /PRIVATE/OFFLINE/backup-identity
fdn-service --config /var/lib/fdn/server.json backup \
  --output /PRIVATE/BACKUPS/foundation.age --recipient age1PUBLIC_RECIPIENT
fdn-service restore --input /PRIVATE/BACKUPS/foundation.age \
  --identity-file /PRIVATE/OFFLINE/backup-identity --destination /PRIVATE/NEW/foundation
```

The age archive contains a consistent SQLite snapshot, TLS CA/keys and a hash/
size manifest. Keep the private age identity off the backup host. Restore
authenticates the full archive, rejects unexpected/duplicate/traversing entries
and verifies schema/integrity before publishing a fresh directory. Supported
backup-manifest `schema_version` values are **1 and 2**; known prior SQL is
migrated to server SQL **3**, while future manifest versions above 2 and future
SQL remain rejected. Manifest schema is not SQLite `PRAGMA user_version`.

**Restore is not identity reactivation.** Before publication it retires sessions
and quarantines **all** restored identities, invalidating owner/key epochs so
old tokens, certificates, keys, challenges or leases cannot resurrect access.
Active/uncertain work remains fenced; histories/IDs and retained mode values are
not reset. Quarantine is not a `manual`/`ai` mode change. Native reconciliation
must not provision restored old/empty keys merely because the database reopened.

Before starting the restored server, explicitly bind its fresh configuration to
the approved native ingress/account sockets, non-root ingress group, service
owner and matching native definition. A restored configuration is not proof of
working host groups/socket policy or permission to enable TCP/bearer fallback.
Then, on that Foundation, authorized root must use private Unix
`recover-founder --input NEW_PUBLIC --reason REASON` to reapprove fresh Founder
keys, retaining its original AgentID/history. The same channel can adopt the
**sole existing** keyless legacy Founder during the first v2 upgrade; zero or
multiple existing Founders fail closed, and no target/rotation flag is invented.
Then active Founder explicitly reapproves each regular identity with fresh signed
keys and its approved public profile, using the documented recovery/original-
agent registration path. Do not copy old credentials back to resume access.
Keep affected workers stopped until identity/work/mode decisions are reconciled;
recovery never automatically changes mode or grants task retry/readiness.

Restore does not recreate root account inventory/groups/SSH policy, identity or
provider HOMEs, native services or workspaces. Reconcile those only through
approved native setup with retained evidence. No full-host recovery or native
root/group qualification is implied; this enhanced restore path still needs
final integrated/all-suite verification.

Cloud disk snapshots and retained disks contain sensitive state and incur
storage costs. Verify exact source disk IDs/status and access boundaries before
retirement. A snapshot is not a successful restore drill.

Leaf certificates last up to one year under a ten-year CA. Stop Foundation and
use `fdn-service --config FILE tls-renew`; complete a retained partial renewal
before serving. This preserves CA pins/identities. Expired/compromised CA trust
needs an explicit coordinated rotation, never automatic trust regeneration.

## Diagnose identity state and recover

Start with `doctor`, `agent identity`, `service status`, `work status`, current
mode, protected identity-service/proxy health and redacted native logs. Verify
actual peer UID/group, certificate purpose/instance/agent/key/owner generations
and host/CA pins without reading private keys. A singleton conflict is a live
owner to inspect, not a stale file to delete. A restart may reconcile prepared
reports without inference; it never clears a ban or uncertain work fence.

Only two different live owner keys proving possession plus a fresh successful
incumbent WebSocket ping trigger whole-identity auto-ban. **Both original and
contender lose API and streams, including Founder.** Stale/crashed owners,
same-key reconnects, ordinary CLI traffic, public collisions and unproved
attempts do not establish that condition. Preserve redacted audit/evidence;
never select a winner, delete keys or automatically unban on reconnect.

### Fresh-key identity recovery without deleting runner state

There is no `--rotate` flag and normal `prepare`/reconnect/restart must not rotate
keys. For an explicitly authorized recovery:

1. Use the authorized native manager to stop the identity service and safely
   stop/join affected runner work. Preserve its old private StateDir, retained
   evidence and **all** runner state, credentials/public binding, journals,
   outbox, knowledge, bindings, provider HOME and workspaces. Never delete keys
   or runner journals to make the recovery appear fresh.
2. Choose a new **empty**, identity-owned 0700 StateDir **inside the same identity
   HOME**, separate from the stable private configuration and IPC parent. Keep
   that 0600 config file path, account UID/GID/HOME, IPC endpoint and every runner
   path unchanged. Update its `state_dir` and the service OPTIONS `state_dir` to
   the new subdirectory only; retain the old state protected and untouched.
   The unit uses stable identity HOME for HOME/working/read-write scope, so this
   supported workflow does not adopt accounts or require deleting old state.
3. As the protected identity account, while its service remains stopped, prepare
   the new signed public request:

   ```sh
   agent-identity --config /PRIVATE/identity.json prepare \
     --name NAME --output /PUBLIC/new-request.json
   ```

4. Obtain the explicit Founder/root recovery below, validate/transfer its new
   public profile, then use `agent-identity --config PRIVATE_CONFIG connect
   --profile NEW_PUBLIC_PROFILE` as the identity account. Restart the exact
   native identity/runner services through the approved OS manager; attach the
   same original runner with `agent-cli connect` and check identity/status.
   Recovery neither resets journals nor authorizes an uncertain task retry.

Recover a non-Founder only after Founder explicitly approves that new signed
request with fresh SSH/authentication keypairs:

```sh
agent-cli agent recover AGENT --input /PUBLIC/new-request.json \
  --reason "Approved fresh-key recovery" --output /PUBLIC/new-profile.json
```

Founder itself requires authorized root execution on the Foundation host through
private Unix maintenance, not an agent gateway/API exemption:

```sh
fdn-service --config /var/lib/fdn/server.json recover-founder \
  --input /PUBLIC/new-founder-request.json --reason "Approved local Founder recovery"
```

Reprovision the retained account binding and approve/transfer the new public
profile; attach only the matching original runner. Old keys, certificates,
owner generations and old receipts remain invalid. Recovery is not task retry,
readiness or permission to resume unknown effects. Full software/RAM clones or
sequential use can still evade simultaneous-owner detection; optional future
hardware signing/attestation is not implemented. Native root-recovery drills
have not been performed by fixture tests.

For an uncertain invocation, first confirm its entire owned process tree
stopped, preserve evidence, then use
`work recover --journal INVOCATION --acknowledge-stopped`. Interactive cleanup
uses the exact `--session SESSION` variant. New attempts still require all
current authority/spec/clarity/budget/backoff/unsafe-retry gates. No timeout
automatically transfers ownership.

Report final machine/role, version/source, identity references, native/service/
network/guide checks, actual auth/mode/work state, retained recovery evidence,
and the smallest outstanding human decision. Never publish raw credential,
provider, journal or private workspace material.
