---
name: foundation-install
description: "Use when installing Foundation or a worker, preparing a native Linux/macOS computer, registering systemd/launchd services, verifying release packages, or upgrading an existing installation without losing identity and state."
---

# Install or upgrade Foundation

Read the version-matched guide with `fdn-service guide show DEPLOYMENT.md`
(or `agent-cli guide show DEPLOYMENT.md`). In a source checkout the guide is at
the repository root. Use the installed binary's help, not remembered legacy
commands.

1. Establish requested role/machine, OS account, HOME, configuration, version,
   current services/state, actual OS permissions, backup and recovery route.
   Installation is not enrollment, provider authentication or automatic work.
2. Preview significant effects. New cloud resources/cost, root changes, SSH/
   firewall policy, data restore, another computer and reboot need their
   applicable authorization. Do not infer it from possession of a key.
3. Obtain the platform's source-bound Actions release and independently trusted
   checksum-manifest digest. Real packages exist only after a PR merges into
   `release`; never build on a target or fabricate a local release event.
4. Verify source/version/platform/digests before native archive/Debian install.
   Preserve immutable releases and all existing state. Keep signing/attestation
   claims separate from checksum verification.
5. Prepare approved native accounts/UIDs/HOMEs, not a same-UID provider and
   credential service. `agent-identity` owns private key/config state; the runner
   gets only a peer-checked Unix proxy and public AgentID. Foundation also needs
   one dedicated SSH account/UID per identity, including Founder, separate from
   the unprivileged server. An authorized root-local account provisioner manages
   only that namespace. Pinned SSH forces `fdn-api`/`fdn-join` stdio into inner
   mTLS Unix ingress, with no arbitrary commands, PTY, forwarding or TCP/bearer
   bootstrap. Prefer no attached cloud identity. Display attachment is not a
   desktop or consent.
6. For upgrades, inspect `work status` and native state; stop/join active work
   before replacement. Retain exact mode, identity, credentials, provider homes,
   knowledge, bindings, journals, outbox and session fences. No manual database
   edits or hidden `--force`/upgrade command exists.
   Non-Founder targets are unattended. Only Founder may explicitly request a
   global mode pause/resumption; delegates and target-local commands cannot.
   Deploy the server migration before new workers and preserve unexplained
   legacy pauses for explicit Founder reconciliation, never silent resumption.
   A restored snapshot quarantines every identity/owner/key epoch; root-private
   fresh Founder recovery retains its ID/history, then Founder reapproves regulars
   with fresh signed keys. Manifest1/2 migration to SQL3 never reactivates old
   tokens/certificates, changes mode automatically or resets runner state.
7. As the actual account, run `guide install`. It must preserve unrelated HOME
   instructions/skills; a collision is an actionable conflict, not permission to
   overwrite them. Worker initialization/startup also installs its guide.
8. Native archives include `agent-cli`, `agent-service`, `agent-identity` and
   `fdn-service`; Debian worker payloads include the first three. Verify the
   new binary and complete guide against the trusted manifest, not just an old
   launcher. Use `agent-identity service plan --input OPTIONS` read-only, then
   authorized native-root `agent-identity service install --input OPTIONS
   --apply` and `agent-identity service status --input OPTIONS`. These take no
   `--config`; OPTIONS is the exact two-account IdentityServiceOptions in
   DEPLOYMENT.md, not single-role agent-cli service options. Preprovision the
   exact UID/GID/HOME/primary groups, managed `fdn-identity=NAME` /
   `fdn-runner=NAME` markers, no-login identity shell and one-way identity
   membership in the worker group; never worker membership in identity/root.
   Private configurations must already exist and bind the planned socket/state.
   Missing/mismatched accounts fail, never auto-adopt/create. Linux registers
   identity system unit plus actual runner user manager; macOS identity
   LaunchDaemon plus actual runner GUI LaunchAgent at fixed definition paths.
   Native multi-UID/manager/GUI qualification remains separate authorized work.
9. Verify actual process owner, exact version/source, enabled/running manager,
   SQLite/TLS health, private listeners, SSH route and guide discovery. Do not
   equate accepted registration with final health.
10. Screen/VNC capability is conditional, not permission to create a desktop on
    every headless worker. Inspect actual physical/virtual display availability;
    cloud display attachment alone is not a running desktop. As an authorized OS
    operator prepare a runner-owned desktop plus authenticated loopback backend,
    per-screen app/capture profiles and trusted noVNC assets on the requester.
    Preserve identity-service isolation. Publish only current ready screens;
    label whole-desktop fallback explicitly. The browser viewer binds localhost
    and uses SSH forwarding plus the existing authenticated RFB relay, never a
    public VNC/web listener. Verify real backend/window placement and native OS
    screen-recording/accessibility permissions separately from source fixtures.
11. Report installed/enrolled/authenticated/mode/work states separately. Route
    enrollment to foundation-enroll and protected login to foundation-provider-auth.

Use `agent-cli guide show COMMANDS.md` for exact setup/service arguments and
`guide show TEST_PLAN.md` for fixture versus native evidence limits. No root
tests, source builds, VM/container setup or paid inference is part of deployment.
