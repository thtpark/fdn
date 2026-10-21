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
- Worker: one dedicated native Linux/macOS account and user service. Debian
  deployments may use `fdn-worker` with HOME `/var/lib/fdn-agent`; do not assume
  that account exists on arbitrary hosts.
- Runtime: no Go/Rust build toolchain, database daemon, Docker or tests on the
  target. Codex/Copilot management needs a supported native Node/npm runtime.
  Use a trusted distribution or checksum/signature-verified upstream runtime,
  not an unreviewed curl-to-root installer.
- Network: loopback API/tunnels, outbound HTTPS, private agent-to-Foundation SSH
  with an individually pinned host key and dedicated forwarding identity.
- Keep cloud metadata credentials least-privileged; prefer no VM service account
  for workers. A dedicated Linux account does not isolate broad cloud metadata
  credentials. Preserve OS Login, organization policy and recovery access.
- GCE display attachment is separate from a desktop/VNC server. Enable the
  virtual display only on the requested worker; do not claim an enabled display
  provides a GUI, desktop consent or an authenticated VNC backend.

Use approved IAP/operator SSH where applicable. Authenticate host keys from
trusted provider/operator metadata, never accept an unknown key or scan it as a
trust-establishment shortcut. Transport enrollment does not create OS accounts.

## Obtain and verify the release

Only a PR merged into `release` creates real packages. Download the appropriate
Actions artifact and independently obtain the `SHA256SUMS` digest from the trusted
run/approved channel. Do not trust a digest downloaded beside an untrusted bundle.
Verify exact source/version/platform and every selected payload.

Targets: Linux amd64/arm64 and macOS arm64. Each artifact contains native archive,
checksums and `release.json`; Linux adds Foundation/Agent Debian packages.
The guide and third-party notices are checksum-covered. Packages are currently
unsigned/unattested; do not call them signed or notarized.

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
package ownership conflicts.

## Ensure operating knowledge is installed

All binaries expose `guide list/show/install` offline. Run as the actual owner:

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

## Initialize and start Foundation

Run initialization as the dedicated service account:

```sh
fdn-service --config /var/lib/fdn/server.json init \
  --state /var/lib/fdn --listen 127.0.0.1:8443 \
  --bootstrap-file /var/lib/fdn/bootstrap.json
fdn-service --config /var/lib/fdn/server.json doctor
```

State must be owned by that account, directory 0700/files 0600. Initialization
retains an exact private preparation plan across interruption and refuses
configuration/key conflicts. Do not delete it to start over.

Register using a reviewed service options JSON with absolute binary/config/
state/home/definition paths, role `foundation`, user `fdn`, OS `linux`:

```sh
fdn-service --config /var/lib/fdn/server.json service plan --input service.json
# Explicit OS administrator execution:
fdn-service --config /var/lib/fdn/server.json service install \
  --input service.json --apply
```

Native Linux Foundation is a system service running without root. Verify the
manager's actual active state, process owner, exact binary/version, database
integrity and the `127.0.0.1:8443` TLS listener. Never expose the API/database
publicly or claim a zero exit code proves asynchronous health.

## Enroll Founder, then a worker

Transfer the public CA and one-time bootstrap as opaque bytes over an approved
encrypted channel. Bootstrap/invitation/token contents never enter chat,
arguments, shared records, screenshots or model-visible logs.

As the worker account:

```sh
agent-cli --config /PRIVATE/agent.json config init \
  --state /PRIVATE/state --url https://localhost:8443 \
  --ca-file /PRIVATE/ca.pem --workspace-root /OWNED/workspaces
agent-cli --config /PRIVATE/agent.json connect \
  --invitation-file /PRIVATE/bootstrap.json --name Founder
```

Configure `ssh` with the worker's own private key, pinned known-hosts file,
Foundation private host/user, local port 8443 and remote port 8443 before remote
connect. The Foundation forwarding account should allow only public-key local
forwarding to `127.0.0.1:8443`, no shell/PTY/agent/X11 forwarding, and a
root-controlled authorized-key location. TLS CA/hostname checks remain required
inside the tunnel.

Founder bootstrap defaults to manual. It consumes the one-time bootstrap and
creates fresh client-generated credentials. Retained pending enrollment must
resume with its exact invitation/name/operation after uncertainty. Never enroll
a duplicate or import another computer's credential.

Founder/current delegate creates subsequent invitations:

```sh
agent-cli --config /PRIVATE/agent.json invitation create \
  --output /PRIVATE/invitation.json --ttl 900
```

The new computer performs its own `connect --invitation-file ... --name Agent1`.
Regular enrollment retains the configured mode/default ai. Verify both actual
identities/roles with `agent list` and `doctor`, then remove consumed transfer
copies only after confirmed success. Before Founder exists, expired bootstrap
replacement uses `fdn-service bootstrap --output NEW_FILE --ttl 900`; after
Founder exists it is forbidden.

## Native worker service

Linux uses the dedicated account's systemd user manager. An OS administrator may
enable lingering and start that manager after account preparation. Invoke
registration as the worker with correct HOME, `XDG_RUNTIME_DIR=/run/user/UID`
and, where needed, `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/UID/bus`.
These bus variables are passed only to service registration, not provider runs.

macOS uses the actual account's launchd registration/GUI domain; a system daemon
does not automatically gain desktop consent. A user service definition specifies
absolute binary/config/state/home paths and the native definition path.

```sh
agent-cli --config /PRIVATE/agent.json service plan --input service.json
agent-cli --config /PRIVATE/agent.json service install --input service.json --apply
agent-cli --config /PRIVATE/agent.json service status
agent-cli --config /PRIVATE/agent.json doctor
```

Mode is the single automatic-work control. Ordinary `ai` mode is not spending
permission, provider authentication, readiness or a workspace tool grant.
Configure/approve each independently. Set an explicit local workspace grant
with `workspace bind --path OWNED_DIR --allow-tools`, or authorize generated
owned project directories through `--allow-workspace-tools`.

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

Before sign-in, inspect mode/current work and spending authority. Authenticate
only in an explicitly human-operated terminal (`provider login`, without JSON or
non-interactive flags), or through an authorized target-consented session. If
authentication could unlock automatic inference, arrange an authorized manual
hold first and preserve the prior mode for separately authorized resumption.
Never copy another account's provider credentials or capture login output.

Missing authentication is not only a local log entry. Before a selected
provider invocation (including initial capability review), the controller
durably requests sign-in from the active Founder and any relevant task-project
delegates. The protected private message contains the target/provider/request
references, supported human-terminal commands and explicit consent/spending
boundaries; it has no timed expiry and names invalidating state/authority
changes. It does not need a model, create a session, assume target consent,
change mode or perform login.

Read the request from Founder's `message inbox` or `message show MESSAGE` using
Founder's own identity, including when Founder is manual. Agent1's `work status`
shows `authentication_requests`, delivery/message references and any unresolved
delivery failure. The same episode/proposal replays after reconnect or a lost
acknowledgement instead of spamming new requests. Unknown provider status stays
an explicit diagnostic, not a guessed sign-in request. Acknowledgement is not
authentication; a successful readiness observation resolves the local episode
without editing the recipient's old message receipt.

Protected remote handoff: request a project-scoped tmux/VNC session, send the
non-secret action/IDs/UTC expiry through Foundation messaging, obtain target
consent, and let the requester explicitly use `session open SESSION`. An ordinary
reply is not consent. VNC needs a verified authenticated existing loopback
backend and trusted `vncviewer`; it does not unlock/install a desktop.

## Upgrade and rollback

1. Capture version, identity references, mode, service state, pending journals/
   reports/session fences and current work. Do not record secret contents.
2. Verify release source/digests and schema compatibility. Obtain an approved
   backup/recovery point. Resolve active operations before replacing binaries.
3. Stop/join the native worker/service; preserve configuration, credentials,
   providers, private knowledge, workspaces, bindings, guides and outbox. A mode
   change is not implied; if approved for maintenance, record/reapply it exactly.
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
authenticates the full archive, rejects unexpected/duplicate/traversing entries,
verifies schema/integrity and publishes a complete new directory. Sessions are
retired; active task ownership stays fenced. It does not restore OS/provider
homes or prove full-host disaster recovery.

Cloud disk snapshots and retained disks contain sensitive state and incur
storage costs. Verify exact source disk IDs/status and access boundaries before
retirement. A snapshot is not a successful restore drill.

Leaf certificates last up to one year under a ten-year CA. Stop Foundation and
use `fdn-service --config FILE tls-renew`; complete a retained partial renewal
before serving. This preserves CA pins/identities. Expired/compromised CA trust
needs an explicit coordinated rotation, never automatic trust regeneration.

## Diagnose and recover

Start with `doctor`, `service status`, `work status`, current mode, and redacted
native logs. A singleton conflict is a live owner to inspect, not a stale file to
delete. A process restart may reconcile prepared reports without inference.

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
