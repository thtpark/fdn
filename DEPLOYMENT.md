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
- Worker, including Founder's computer: the existing installing user runs
  `agent-identity`, `agent-service`, CLI and providers in its own HOME. No new
  OS account/group, hidden user or separate identity HOME is required. That
  user owns identity keys as well as provider and runner state.
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
namespace. Never substitute a shared Foundation SSH account, copied bearer
credentials or relaxed TLS. Worker identity and tools intentionally share the
existing local user; Foundation authenticates the enrolled identity, not that
local username. Native Windows service/IPC adapters remain unimplemented.

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

## Founder-controlled native upgrades

Local development can run from source using
[the isolated source wrappers](DEVELOPMENT.md#running-source-with-an-isolated-testing-identity).
The normal release path is:

1. Fix and test code; send a reviewed PR into `main`.
2. Increment VERSION for new immutable contents and send the qualified source
   through a separately tested PR into `release`.
3. Only that actual merge builds packages. After every platform succeeds,
   the guarded publisher checks exact source/checksums and completed pre-merge
   acceptance, uploads all bundles as a draft `vVERSION` GitHub release, then
   publishes. Existing tags/releases are never rebound or overwritten.
4. As enrolled Founder, discover, prepare an exact plan and explicitly apply:

   ```sh
   agent-cli upgrade check
   agent-cli upgrade check --input /PRIVATE/targets.json
   agent-cli --timeout 300 upgrade plan --input /PRIVATE/targets.json --output /PRIVATE/plan.json
   agent-cli upgrade apply --input /PRIVATE/plan.json
   agent-cli upgrade status --input /PRIVATE/plan.json
   ```

`check` is read-only. Plan generates native challenges and an off-host age key
beside the private plan, without stopping services/installing. Apply needs both
current Founder authority and independent preprovisioned OS permission on each
target. Updater policy is **not** installed by an ordinary agent request.
Bootstrap an upgrade-capable server/worker release once through an authorized
operator; old binaries and the previous Actions-artifact-only channel cannot
upgrade themselves into these capabilities. No source change implies that a
new release, helper policy or rollout is already deployed.

### Provision a narrow native policy once

An OS administrator reviews and installs an owner-only policy file plus an
independently obtained **public** Foundation CA. A Linux package updater runs
under its own root-controlled state outside the application/backup paths.
It never uses the identity-account provisioner as a generic root command runner.

Example Foundation Debian policy (substitute the actual instance ID and
preexisting paths; the CA file contains no private key):

```json
{
  "role": "foundation",
  "name": "foundation",
  "instance_id": "instance_ACTUAL",
  "target_id": "instance_ACTUAL",
  "ca_file": "/etc/fdn/updater-ca.pem",
  "state_dir": "/var/lib/fdn-updater",
  "method": "debian",
  "binary_dir": "/usr/bin",
  "user": "fdn",
  "home": "/var/lib/fdn",
  "config_file": "/var/lib/fdn/server.json",
  "backup_paths": [
    "/var/lib/fdn-accounts",
    "/etc/fdn",
    "/etc/systemd/system/fdn-accounts.service",
    "/usr/bin/fdn-service"
  ]
}
```

For a Debian worker use `role:"agent"`, its exact stable `target_id`, installation
`name`, existing `user`/`home`, runner `config_file`, `identity_config` and
`service_options`. `method:"debian"` fixes `binary_dir` to `/usr/bin` and requires
explicit Linux root execution. Add the actual existing native definitions,
provider credential paths and old role binaries to `backup_paths`. The helper
also includes the configured application/identity state and private configs in
its cold snapshot. Additional paths must exist; do not list the filesystem root
or include updater evidence inside a snapshot of itself.

For a managed native archive, `method:"archive"` requires `prefix` and
`binary_dir: PREFIX/bin`, using the existing immutable archive installer and
fixed managed activation links. macOS worker updaters run as the existing
installing user and restart its two GUI LaunchAgents, not root/system daemons.
The two definitions must already exist at their standard paths. Foundation
upgrading remains Linux-only; this is not Windows support.

Policies/configurations/CA pins must have real protected ancestors, with no
symlink or group/other-write substitution. The policy is private to the updater
account (root for Debian); targets use the actual existing non-root service
user/HOME. No worker account/group or key rotation is introduced. Changing a
native policy invalidates its previously prepared challenge hash.

Provision only the exact operator entrypoint needed, for example a reviewed
sudoers rule allowing this **fixed complete command**, not arbitrary service
arguments, policy paths, shell commands or a general installer:

```text
/usr/bin/fdn-service upgrade-helper --policy /etc/fdn/updater.json
```

Use the corresponding `agent-service` command on workers. A helper accepts only
bounded protobuf `inspect`, `challenge`, `prepare`, `apply`, `status`; arbitrary
commands, paths or package URLs cannot be supplied in that protocol.
Inspection and status do not confer install authority.

### Configure the Founder's operator targets

Example private target file, using an already opened loopback IAP tunnel to
Foundation's SSH port (not an API listener):

```json
{
  "targets": [
    {
      "role": "foundation",
      "target_id": "instance_ACTUAL",
      "platform": "linux-amd64",
      "ssh": {
        "host": "127.0.0.1",
        "port": 2222,
        "user": "operator",
        "identity_file": "/PRIVATE/operator-key",
        "known_hosts_file": "/PRIVATE/known-hosts"
      },
      "helper": "/usr/bin/fdn-service",
      "policy_file": "/etc/fdn/updater.json",
      "sudo": true
    }
  ]
}
```

Add worker entries with role `agent`, exact AgentID and target native platform,
separate operator endpoint as needed, and `agent-service` helper. Operator keys
are separate from Foundation signing keys. Authenticate each host pin out of band;
never accept unknown keys or route the helper through the forced `fdn-api`/
`fdn-join` account. The client deliberately ignores ordinary SSH configuration,
agents and proxy settings. A plan targets 1-32 computers; split larger changes
into explicit bounded maintenance batches rather than infer fleet qualification.

### Authority, backups and failure handling

The server transaction requires current Founder and paused enrolled target key
generations, exact native source/policy hash and no active task ownership or live
assistance. Foundation upgrades require every active agent paused. Its signed
permit is fixed for 15 minutes; exact replay never extends it.
Immediately before stopping, a helper proves a fresh nonce to the server and
requires its 30-second signed start lease. Workers obtain it through their own
peer-checked identity proxy; Foundation uses the root-only private maintenance
socket. The lease rechecks current parties, key generations, pause and work.
It authorizes admitting the exact maintenance job, not arbitrary future restarts.
Expiry/revocation before admission fails explicitly. Admitted stopped jobs use
their durable receipt and original expiry; they do not regain work authority.

Every helper downloads only the exact pinned repository asset and verifies its
GitHub API ZIP digest, contained manifest, native platform/version/commit and all
payload hashes before stopping. Package/file replacement uses only the policy's
fixed native role/method. Development builds, same-version replacement, source
mismatches, downgrade and arbitrary download origins are rejected.

Stopped-process cold snapshots are age-encrypted using the plan's public
recipient. The private key remains beside the Founder's private plan, never on a
target or in messages/model output. Native snapshots can include server CA keys,
identity state and provider authentication: provision this capability only for
the trusted Founder and keep both backups and recovery key owner-only.
These snapshots and retained immutable releases are recovery evidence, not an
automatically qualified whole-host restore or safe SQL rollback.

Upgrade submission queues private request/result conversations. Each recipient
decides at a safe opportunity, then stops/backs up its own services. Foundation's
completed result precedes dependent agent requests. Actual version/
identity/fence/service checks remain target-owned. Automation
stays manual; sign-in, scope approval and explicit resumption remain separate.
Busy/uncertain invocations/reports, changed identities/policy/source, missing
native services and expired permits fail closed. No task retry, fence clearing,
provider upgrade, cloud operation, financial permission or release publication
is implied by a software update request.

### Independent native message receiver

Provision one independently supervised maintenance process per fixed policy:

```sh
# Explicit authorized native administrator context, outside the units it stops:
fdn-service request-worker --policy /etc/fdn/updater.json
# Worker counterpart under the policy's authorized updater account:
agent-service request-worker --policy /etc/fdn/updater.json
```

`--once` inspects/processes at most one available request for bounded checks.
Do not invoke this receiver as a child of the identity/runner/Foundation/account
service it must stop. Its private `request-worker.lock` prevents duplicate
receivers; retained `message-inflight.json` and native receipts preserve uncertain
effects. The inflight record retains the exact report before sending, including
its operation/revision/reason. Lost or unavailable acknowledgements remain pending
delivery, never a new refusal or permission to repeat installation. Replay the
same report first when the mailbox returns. A restart can report an exact verified
native receipt; an interrupted admission without one fails with a reason and
requires review. Native evidence stays under the administrator's existing policy state.
The Foundation receiver uses only the peer-checked root-private maintenance
socket. Agent receivers use the target's own same-user inbox adapter. Neither
starts a new application identity or exports private keys to a sender.

This receiver performs no AI inference. It accepts only the exact authenticated
Founder permit for that target, refuses invalid authority with a result reason,
waits on preceding result messages, and leaves global mode unchanged.
Failed/deferred/declined prerequisites produce a target defer reply rather than
leaving dependent requests queued indefinitely. Ordinary
model-reviewed work requests wait while paused and use the serial controller,
current task approval/workspace/spending limits. Insistence is a new linked
request, never a force or permission/retry reset. A queued request may expire
before native admission; only an explicit newly authorized request can renew
its proposal. Request/result protocol changes require a compatible coordinated
software rollout; installing source changes does not deploy receivers.

After a lost response, use the same private `upgrade status --input PLAN`.
It works from retained original-Founder evidence plus the separate operator
channel even while Foundation is offline. `prepared` means stopped and backed up,
not installed. `operator-required` plus the original mutating `code` requires
inspection; never blindly repeat apply, erase journals, replace a plan in place
or automatically roll back an upgraded database.

Native tests use synthetic bundles, recording managers and isolated actual-user
files; they do not register host services, upgrade a live host, prove reboot
recovery, certify production security or perform paid inference.

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
The managed guide lives below `$HOME/.foundation/guidance`, with immutable
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
private state, ingress group/socket parents and immutable gateway binary.
Workers use their existing installing users, without account provisioning.
Every identity, including
Founder, also gets its **own** Foundation-host SSH account/UID. Never share that
SSH account with another identity or give it a normal shell.

### Agent names and OS accounts

The worker's existing username does not matter. For example, local user `alice`
can install/run both processes and enroll as `Agent1`; Foundation reserves
`agent1` for that identity's SSH ingress on the Foundation host. No local
`agent1`, `fdn-worker` or `fdni_agent1` account is created. UIDs are local to each
host and do not need to match.

| Role | Example account | Boundary |
| --- | --- | --- |
| Worker identity and provider/tool runner | Existing `alice` | Same user/HOME owns keys, workspaces and provider login; owner-only IPC |
| Foundation SSH ingress for Agent1 | `agent1` | Dedicated local UID; forced API/join only, no ordinary shell |
| Foundation server | `fdn` | Separate unprivileged service/database account |

New registration lowercases the agent name, replaces runs outside ASCII
`a-z`, `0-9`, `_`, `-` with `-`, and removes outer `-`. The result must match
`[a-z_][a-z0-9_-]{0,31}` and cannot be `root`; the display name must be one line.
`Agent One` therefore uses `agent-one`. Invalid/oversized names and reserved or
unmanaged account collisions fail explicitly; nothing is truncated, silently
hashed, adopted or renamed. The signed profile's `identity.ssh_user` is the
authoritative chosen Foundation login.

Existing local users are retained and existing Foundation UID/name
reservations, including `fdna_...`, are retained during upgrade/recovery. Do not
rename a live account or move its HOME/state merely to match the new convention.
Matching spellings are for separate hosts, not permission to reuse a runner or
server account as SSH ingress on the same host. Upgrade the root account helper
with the server before new named registrations; the narrow RPCs now carry the
approved username. No database reset, account migration or deployment is implied.

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

The helper's internal typed provision request is `agent_id`, `username`,
`public_key`, positive key `generation` and optional `reactivate`; disable is
`agent_id`, `username` and positive `generation`. Omission of username preserves
the old ID-derived request convention only; the server supplies the exact
registry-approved username on new requests. These are server-to-root Unix RPC fields, **not new CLI
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

As the existing installing user, prepare a private configuration with absolute
`state_dir`/`socket`, distinct identity/runner state and an IPC directory under
that user's HOME. There is no `agent-identity config init` command.
Private `worker_uid`/`worker_gid` may be omitted/zero: native execution derives
the actual user's UID/GID and rejects explicit foreign/legacy values or root.
Private state/configuration is 0700/0600; IPC parent/socket is 0700/0600 with
kernel peer UID/GID checks. Do not create accounts/groups or change HOME mode.

As Founder's existing worker user, with the identity service stopped:

```sh
agent-identity --config /PRIVATE/identity.json prepare \
  --name Founder --output /PUBLIC/founder-request.json
```

Transfer **only the signed public request** through an authenticated channel.
The private SSH/authentication keys stay in that user's private identity state.
They are not exported by preparation or ordinary IPC. As the
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
computer, its existing installing user first runs `prepare --name Agent1
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

As the target's existing worker user, with the identity service stopped for the
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
The proxy never returns these certificates/private keys in ordinary IPC replies.
The same OS user owns the files; this is not isolation from its own tools.

As that **same existing user**, initialize the runner's local proxy binding:

```sh
agent-cli --config /PRIVATE/agent.json config init \
  --state /PRIVATE/runner-state --identity-socket /OWNED_HOME/.local/state/fdn-ipc/identity.sock \
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

There are two processes under the **same existing installing user**:
`agent-identity --config PRIVATE_CONFIG serve` and
`agent-service --config RUNNER_CONFIG serve`. Their paired lifecycle
uses **no `--config` or enrollment**:

```sh
agent-identity service plan --input /PROTECTED/identity-services.json
# As the existing installing user, without sudo:
agent-identity service install --input /PROTECTED/identity-services.json --apply
agent-identity service status --input /PROTECTED/identity-services.json
```

`plan` is read-only; `install` requires explicit `--apply` as the existing user;
`status` uses that same user without root or an apply flag. Input is a protected options file,
not stdin. This is different from the runner-only `agent-cli service` interface.
Neither command creates accounts/groups, changes the user's HOME permissions,
sets a no-login shell, enables linger or switches users. The user's native
manager/login domain must already be available.

New user-owned files share one private `$HOME/.foundation` directory:
`config/{agent,identity}.json`, runner `state`, key `identity`, IPC `run`,
`workspaces`, managed command `bin`, and `guidance`. `config init` selects that
runner state, workspace root and identity socket by default. Identity private
commands still require their explicit configuration. Native service definitions,
shell login profiles and tool discovery links retain their platform-required
locations. System Foundation server/account state does not move.
Explicit current-format paths remain valid and are not rewritten.
See [explicit configuration and current paths](MIGRATION.md#explicit-configuration-and-current-paths).

`platform.IdentityServiceOptions` uses these exact JSON fields:

| Fields | Contract |
| --- | --- |
| `os`, `name` | Native `linux`/`darwin` (OS defaults to current); stable installation name, not an OS account selector |
| `user`, `uid`, `gid`, `home` | Optional defaults from the actual OS account; native install/status must match that existing non-root user, independently of agent name |
| `identity_binary`, `worker_binary` | Protected absolute paired `agent-identity`/`agent-service` binaries in the same directory; owned by the user or root |
| `identity_config`, `worker_config` | Preexisting owner-only 0600 files under the same existing HOME; planned state/socket bindings must agree |
| `state_dir`, `worker_state_dir` | Distinct private 0700 directories under that HOME; identity key state can be a fresh generation-specific directory |
| `socket_dir` | Distinct user-owned 0700 IPC parent under that HOME, separate from state directories; proxy socket 0600 |
| `identity_definition`, `worker_definition` | Exact per-user native paths below; no system-service/root write targets |

No special account marker, named primary group, supplementary membership or
private 0700 HOME is required. The existing HOME remains unchanged. Configurations
and private state must already exist as this user; missing/untrusted state is an
explicit failure. Service options accept only the current single-user fields;
they never create accounts, move keys or change ownership. See the
[supported baseline](MIGRATION.md#supported-baseline).

| Native OS | Exact definition paths (`NAME`, `HOME` are plan values) |
| --- | --- |
| Linux identity | `HOME/.config/systemd/user/fdn-identity-NAME.service` |
| Linux runner | `HOME/.config/systemd/user/fdn-worker-NAME.service` |
| macOS identity | `HOME/Library/LaunchAgents/org.foundation.identity.NAME.plist` |
| macOS runner | `HOME/Library/LaunchAgents/org.foundation.runner.NAME.plist` |

Linux registers **both** with `systemctl --user` and the actual user's existing
bus. A missing manager/bus fails before service-definition writes; optional
logout persistence is a separately authorized native prerequisite. No
`loginctl enable-linger`, system `user@UID` start, root helper or `runuser` is
invoked by installation.

macOS registers **both** LaunchAgents in `gui/UID` as the existing logged-in
user. It creates no hidden account or LaunchDaemon and invokes no `sudo` or
`launchctl asuser`. Missing GUI/login domains are explicit prerequisites;
registration grants no desktop/TCC consent. Recording fixtures do not qualify
live native manager, desktop or reboot behavior. Windows service/IPC support
is still separate, unimplemented work.

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

Upgrade Foundation before workers: API/schema is 2, native message protocol 2,
server SQLite migration 5,
local SQLite remains 1. Existing server databases must already be SQL5 or a
newer supported version; older state is rejected without modification.
New enrollment uses signed `agent register`, while `connect` attaches that
same enrolled identity through its private proxy. Current configurations,
knowledge, journals, bindings and outbox are retained. Never copy another
computer's keys or reset its database to bypass an incompatibility.

The binary transport is a clean cutover, not an HTTP compatibility window.
Replace all four compatible binaries and their guide, then verify the actual
Foundation, identity-service and runner versions before resuming approved work.
Native coordination uses protobuf sessions over SSH/mTLS/Unix IPC, including
owner control, relays and narrow root maintenance/provisioning. Existing HTTPS
origin settings identify TLS trust/server naming; they do not expose an HTTP
service. Browser-facing and third-party vendor protocols remain separate.
Keep current certificate/owner-generation proofs until OS-only admission has
passed independent native stale-session/recovery qualification. A source build
or an authorized clean deployment does not by itself justify an unidentified
state-directory wipe.

Existing Founder defaults to manual, regular agents to ai. An unexplained old
local manual pause is blocked rather than silently resumed; Founder explicitly
reconciles it. Old servers without authoritative mode/identity metadata cannot
run a new worker.

## Concise public activity status

Use `agent list`, `agent show AGENT_ID` or the current caller's `status` to inspect
`status.text`, source/state, publication/observation timestamps and stale flag.
All active agents can see this intentional non-secret publication; it does not
broaden private diagnostics/messages or certify health/completion. Local `work
status` also provides immediate safe `activity_text` while publication is
coalesced.

Controller summaries require no inference: “Reviewing task scope”, “Replying to a
message”, “Waiting for clarification”, “Paused by Founder”. Normal text changes
publish no more than once per60 seconds, unchanged text is suppressed and rapid
changes coalesce to the latest. Meaningful blocked/paused/recovery transitions
can publish promptly through the existing health transport/monitor. Bookkeeping
polls and idle heartbeats do not indicate business progress.

For deliberate self-authored progress during authorized tool-enabled work:

```json
{"text":"Checking parser edge cases"}
```

```sh
agent-cli --config /PRIVATE/agent.json --operation-id progress-001 \
  agent status --input FILE
```

Only the same enrolled agent may publish; no target/priority/time/mode override
exists, including for Founder. Keep text single-line, concise (max160 characters)
and safe for the whole team. Empty text restores automatic wording. Authored notes
are scoped to current activity and cannot hide a later pause/block/completion.
`pending`/`next_update_at` is throttled publication, not a failed model/task call.
Do not repeatedly publish identical text, call models just for status, or disclose
credentials, private paths, message details or raw logs. Status does not grant
permissions, acknowledge reading, or resume automation. Preserve missing/stale
observations and use authorized diagnostics for the real blocker.

## Explicit message read acknowledgements

Peeking is not acknowledging reading. `message show`, `message inbox`,
`message diagnose`, delivery and reply handling remain passive with respect to
`read_at`. When the recipient deliberately finishes reading, use its own enrolled
identity and a stable operation:

```sh
agent-cli --config /PRIVATE/recipient-agent.json \
  --operation-id message-read-001 message read MESSAGE_ID
```

The first server UTC read timestamp appears to the message parties in `message
show`/`message diagnose`; no timestamp means no explicit acknowledgement, not
proof of unreadness. No timestamp input, reset, sender override, state change,
reply request or historical backfill exists. A tool-disabled direct-message model
can explicitly return optional `message_read:true`; omitted/false stays a peek,
even when it sends a reply. The controller must not manufacture that decision.
Install the server/read endpoint and matched identity proxy before updated
workers; a lost acknowledgement retains/replays the exact prepared result rather
than triggering another model call. This is not task approval, comprehension or
session consent.

## Read-only worker and reply diagnostics

Use the existing enrolled identity and private configuration:

```sh
agent-cli --config /PRIVATE/agent.json agent diagnose AGENT_ID
agent-cli --config /PRIVATE/agent.json message diagnose MESSAGE_ID
agent-cli --config /PRIVATE/agent.json work status
agent-cli --config /PRIVATE/agent.json service status
```

The private identity proxy must forward both diagnostic GET routes and the exact
Founder/target operation routes described below. An authorized future rollout
must include the matching `agent-identity` service as well as CLI/runner/server;
an older proxy's route denial is not a provider sign-in failure. Never work around
it by relaxing transport controls or copying identity credentials. These source
changes alone do not deploy or restart any service.

General agent diagnosis is Founder/self only; project delegation alone does not
grant it. A message diagnosis is private to its sender/recipient, even when
Founder asks. Both default diagnose commands accept exactly one ID and fetch saved
observations without provider probes or actions; only agent diagnosis additionally
accepts the explicit Founder-only `--refresh` write below. Use scoped alert messages for
delegate triage, not another conversation's message IDs or private worker data.

Inspect `health`, observation/receipt age, effective mode/provider/model and
configured `preferred_provider`, timestamped authentication readiness,
phase/action/start/deadline, separate loop/progress clocks, pending reports,
blockers and safe timestamped component problem codes. Sequence counts phase
transitions, not polling; a fresh snapshot can keep the same sequence. Sequence
0 before a first controller step remains unknown, not healthy. Idle polling and
heartbeats do not prove task progress. A finished login command is not a readiness
observation; a current observation for one provider does not authenticate another.
Unknown/stale data needs the reported inspection step, not a green fallback.

Workers piggyback optional version-1 health on their 30-second heartbeat; old
heartbeat bodies and the `Agent` result remain compatible. The server's non-AI
watchdog runs without worker cooperation, including for manual Founder. Defaults
are 90 seconds without heartbeat/report, a phase deadline plus 30 seconds, and
120 seconds for an eligible unexplained queued requested reply. Founder pause,
normal idle, no requested response and work within its declared deadline are not
stuck failures. Differentiate no requested response, queued/shared-slot waiting,
generating, blocked, awaiting delivery and handled through observed lifecycle
`state`, not lower-level `stage`. For example, `state:"generating"` and
`stage:"invoking"` can appear while the provider invocation is still pending,
with a worker heartbeat reporting the matching action/deadline; blocked recipient
observations use `state:"blocked"`, `stage:"recipient"`. Neither field proves an
authoritative receipt or AI completion. A handled receipt alone does not establish
an AI reply; inspect the private conversation and actual receipt when authorized.

Protected health alerts/recovery notices are ordinary server-created private
messages with `response_requested:false`. They route to active Founder/current
task delegates only where scoped, dedupe by incident/cause/action, survive
polling/restart and notify observed recovery. They cannot be forged through
`message send`. Health incidents are independent of 15-minute sign-in notices:
monitoring does not renew those notices or start login, a session, inference,
retry, resume or recovery. After sign-in, Founder pause, open/uncertain sessions,
uncertain invocation, a different selected/required provider and permission/report
blockers still require their own authorized resolution. Never erase journals,
clear fences or reboot active work to make a health check pass.

If Foundation itself is down, its watchdog cannot guarantee detection/delivery.
Keep native service/process and outside control-plane monitoring; no successful
local diagnostic or fixture result is live Agent1/deployment qualification.

### Phase-invalid task reviews and independent inbox progress

A provider can finish a tool-disabled scope review yet return the wrong outcome
(e.g. `reviewed`, which belongs to onboarding/verification, instead of
`clear`/`blocked`/`paused`). This is a response-contract failure, not sign-out or
proof that execution occurred. The controller preserves the original response
and exposes its `reply-rejected` journal in `work status` `text_response_blocks`.
It sends a protected `provider-response` notice and fences that semantic action
without indefinitely replaying it or blocking unrelated requested replies.

Inspect the exact private journal reference and current work. Only after native
confirmation that its owned processes stopped, an authorized operator can use
`agent-cli work recover --journal EXACT_INVOCATION --acknowledge-stopped` for one
explicit fresh review authorization. Preserve rejected evidence; do not coerce
outcomes, delete state or change a task merely to make the fence disappear.
Founder session repair does not recover journals or authorize this retry. Keep
mode and existing task/spending approvals unchanged. A restarted poll/new work-run
operation is not fresh retry authority, and uncertain/possibly applied work keeps
its original stricter fence.

### Founder refresh and exact closed-session recovery

Use the existing Founder configuration; no keys or credentials move to the target:

```sh
agent-cli --config /PRIVATE/founder-agent.json \
  --operation-id diagnostic-refresh-001 agent diagnose AGENT_ID --refresh
agent-cli --config /PRIVATE/founder-agent.json agent operation REFRESH_REQUEST_ID
```

Submission returns a durable operation, initially `queued`, not a fresh successful
check. `REFRESH_REQUEST_ID` is the returned `data.id`, not the envelope's stable
write `operation_id`. Both refresh and repair use the same `agent operation`
lookup; there is no wait/provider/inference/shell option or separate inspect path.
`checking` is still pending; `refreshed` is a bounded safe observation,
`recovered` is exact fence retirement, and `rejected`, `operator-required` or
`expired` require following the safe result rather than claiming recovery.
Every operation expires at its immutable server-clock creation time plus 900
seconds. `server_observed_at` projects current server time on lookup;
`result.captured_at` retains a conservative server-time capture bound from a fresh
server response and monotonic request-start/round-trip timing, separate from target
UTC inspection/provider/health observations. Use the server-clock capture/expiry
fields, not skewed target observation times, for operation freshness/lifespan.
Reading an operation never renews it or changes its immutable fingerprint. These
clocks are distinct from sign-in-notice expiry, provider readiness and live-session
lifetimes. Offline/dead daemons can leave the operation queued until expired;
there is no remote native restart authority.

The independent non-inference worker control loop handles controls even while
paused/fenced/busy, without normal model admission. Provider observation is
freshly timestamped separately from in-flight health/phase. Credentials present
but unverified do not establish vendor usability; `ready` is an observation,
not a paid inference or work-progress test. Refresh renews no sign-in notice,
starts no login/session/model and changes no mode or grants.

For a reported closed-session fence, inspect the fresh operation's
`result.observation.session_fence`. Copy the exact `session_id` and `fingerprint`
into a private JSON input (`expected_fence` is the copied lowercase SHA256):

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

The target independently rechecks the exact current fence, key generations,
server-confirmed terminal session and free shared `work.lock`, using existing
durable native `CleanupProven` evidence. No remote `--acknowledge-stopped` or
caller assertion of proof is accepted. Clearing the matching eligible fence and
recording the final local receipt are atomic; network uncertainty replays that
receipt, not another mutation. Keep the original operation ID/input unchanged.

**Initial limitation:** old/unproven `CleanupProven:false` fences return
`operator-required`. Process absence/name/PID scans are not substituted for native
proof. The original Agent1 fence may still require explicit authorized native
inspection and the existing exact local session stopped-process acknowledgement;
this feature does not promise automatic recovery. It does not clear journals,
retry inference, stop arbitrary processes, erase evidence, recover identity or
restart a native service. Active sessions, busy slots, changed fingerprints and
stale generations fail closed. `agent recover` still means fresh-key identity
recovery.

Recovery never bypasses Founder pause or changes task/spending/retry authority.
Previously approved `ai` work may resume only if all independent fences permit;
manual remains manual. After the operation finishes, diagnose again and observe
actual progress rather than interpreting `automation_may_resume` as execution.
Protected server-only controls/results (`response_requested:false`) cannot be
forged/settled by ordinary message send/receipt and confer no unrelated private
session/message access. Safe results omit stdout/raw errors, process arguments,
credentials and workspace paths. See the complete
[command contract](COMMANDS.md#founder-diagnostic-refresh-and-exact-closed-session-recovery).

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
account's `.foundation/bin` and preserve a managed PATH block in its login profile.
Verify the actual login shell with `command -v codex`, `command -v copilot`,
`codex --version`, and `copilot --version`, not only absolute managed paths.
For older installations run `agent-cli --config FILE provider link` as the worker
and reconnect (or export `.foundation/bin` in the current shell). Never use global
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
Prefer supported headless/device-code login: Codex `login --device-auth`,
Copilot `login --device-code`. Local `provider login` and authentication tmux
sessions inspect the selected executable's `login --help` first, add only the
advertised provider-specific flag, and otherwise retain native login. Help
failure is explicit and starts no login; Copilot self-update remains disabled.

Displaying the provider's sign-in screen, public verification URL and one-time
prompt to the authorized human through the approved terminal/session is allowed;
that expected sign-in display is not itself a privacy violation. Public generic
verification URLs/instructions may be included in help messages. This is not
permission to publish tokens, passwords, cookies, credential files, secret-bearing
URLs or unredacted credential transcripts; keep one-time codes in the human-only
flow and never copy another account's credentials. Only the human completes
credential/consent steps. Display permission does not authorize login/inference.

Missing authentication is not only a local log entry. Before a selected
provider invocation (including initial capability review), the controller
durably requests sign-in from the active Founder and any relevant task-project
delegates. The protected private message contains the target/provider/request
references, supported human-terminal commands and explicit consent/spending
boundaries. It expires 15 minutes after creation; unchanged polling, delivery
or restart does not extend it. A new requested action can ask again under its
own stable action key. Assigned work, requested messages and mentions are checked
before onboarding, so an expired computer-local notice cannot hide a new task. It does not need a model, create a session,
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
Workspace/provider-access/task-authority/retry-approval blockers also send
protected `permission` help to Founder/current relevant project delegates, with
public references and no raw errors/private paths. Inspect `permission_requests`
in work status. New requested actions can ask again; pending deliveries retain
the same operation. After a provider fails mid-work, an explicit sign-out in a
fresh readiness probe produces help, but uncertainty/report/retry fences remain.
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
### Display-equipped agents and browser VNC

VNC is conditional on an actual physical/virtual desktop, never on a VM display
hardware flag alone. `screen list` and the published public screen inventory must
report actual ready screens before VNC is requested. Headless agents keep an
empty screen inventory; do not silently create a desktop on them. A headless
Founder can still coordinate/view another agent through an SSH-forwarded browser
viewer without gaining its application identity.

As an authorized OS operator, prepare a native desktop and authenticated
loopback-only VNC backend under the same dedicated runner account that owns the
apps/provider state. Keep the identity-service account/HOME/keys separate. Use
trusted signed OS packages, not downloaded root scripts. Linux can use distinct
virtual desktops or monitor-scoped capture; macOS needs an existing authorized
desktop and any native screen-recording/accessibility consent. Never unlock
someone else's desktop or automate OS permission/credential prompts.

Readiness probes read only RFB protocol metadata, never VNC credentials or pixels.
TigerVNC can count these incomplete connections toward its connection blacklist,
then return an RFB denial banner even while the port is listening. With explicit
operator approval, a **verified loopback-only** TigerVNC backend can use
`-UseBlacklist=0` while retaining `-SecurityTypes VncAuth`, private credentials,
SSH/mTLS relay authorization and disabled X11 TCP. Never apply that exception to
an externally reachable backend. Foundation duplicate-identity bans are separate
and remain enforced. Qualify actual backend authentication separately through an
authorized human or an opaque credential probe that emits only readiness metadata.

Configure one explicit `screens` profile per usable display: stable id/name,
geometry, scope screen/desktop, verified loopback VNC address, native display
binding and a bounded list of application IDs with absolute approved executable
and argument arrays. `config set --input FILE` accepts the profiles. Local paths,
Xauthority and backend authentication stay private. Xauthority remains a bounded
0600 regular file owned by the runner, not root or the identity service. Native
service inspection validates against the planned runner UID; do not change file
ownership to satisfy a root invocation. Place/launch each app on its own screen
when native support allows; share only the selected screen. If capture
cannot isolate it, label scope desktop and require desktop_fallback in the
Founder grant/session instead of silently exposing more screens.

Targets use `assistance request --input FILE` for VNC or tmux human help. VNC
requests name the screen/application, reason, purpose authentication/terminal and
optional project/task. Founder/current delegates receive protected notices; only
Founder issues the matching retained grant and the human explicitly opens it.
Requests alone never launch a screen/app, open access, sign in or start inference.

The browser viewer uses trusted locally installed noVNC assets (for example a
signed distribution's novnc package on the requester) through `--assets-dir`.
`assistance open GRANT --viewer browser --listen 127.0.0.1:PORT` binds only loopback;
forward the same PORT over the authorized operator SSH connection to the human's
localhost. Agent RFB bytes remain inside Foundation's existing authenticated
SSH/mTLS relay. No external VNC/web/API listener or CDN is needed. The bootstrap
URL is a short-lived access capability and stays in the human terminal, unlike a
public provider verification link; do not put it in shared messages/model logs.
Clipboard transfer remains disabled. `--viewer native` is available for an
existing trusted local viewer. Expiry/revocation/disconnect cleans up the viewer
and relay; uncertain process state remains fenced.

Granting access does not install/unlock a desktop. Messages are never grants.
Actual display/backend/window placement and browser-client qualification remain
separate from synthetic source tests and must be verified on each equipped host.

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
migrated to server SQL **4**, while future manifest versions above 2 and future
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
keys, retaining its original enrolled AgentID/history. Zero or multiple existing
Founders, or a missing current identity binding, fail closed.
Then active Founder explicitly reapproves each regular identity with fresh signed
keys and its approved public profile using the documented identity recovery
path. Do not copy old credentials back to resume access.
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
mode, authorized `agent diagnose AGENT_ID`/`message diagnose MESSAGE_ID`,
protected identity-service/proxy health and redacted native logs. Verify
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
2. Choose a new **empty**, user-owned 0700 StateDir **inside the same existing
   user's HOME**, separate from the stable private configuration and IPC parent. Keep
   that 0600 config file path, account UID/GID/HOME, IPC endpoint and every runner
   path unchanged. Update its `state_dir` and the service OPTIONS `state_dir` to
   the new subdirectory only; retain the old state protected and untouched.
   The service uses the existing HOME for its stable working directory, so this
   supported workflow does not adopt accounts or require deleting old state.
3. As the existing worker user, while its identity service remains stopped, prepare
   the new signed public request:

   ```sh
   agent-identity --config /PRIVATE/identity.json prepare \
     --name NAME --output /PUBLIC/new-request.json
   ```

4. Obtain the explicit Founder/root recovery below, validate/transfer its new
   public profile, then use `agent-identity --config PRIVATE_CONFIG connect
   --profile NEW_PUBLIC_PROFILE` as that same user. Restart the exact
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
