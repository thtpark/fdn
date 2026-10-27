---
name: foundation-install
description: "Use when installing Foundation or a worker under the existing installing user, preparing native Linux/macOS computers and per-user services, migrating legacy split-account workers, verifying durable release packages, provisioning narrow native upgrade policies, or applying a Founder-authorized upgrade without losing identity and state."
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
   Durable `vVERSION` GitHub release ZIPs are published only after all native
   jobs and exact-source pre-merge acceptance succeed. Use GitHub's independent
   asset digest and every contained manifest checksum; no arbitrary URL or
   expired Actions-artifact fallback is an upgrade channel.
4. Verify source/version/platform/digests before native archive/Debian install.
   Preserve immutable releases and all existing state. Keep signing/attestation
   claims separate from checksum verification.
5. Reuse the existing installing user for worker identity, CLI, service and
   providers. No new account/group, hidden user or separate HOME is required.
   The local username need not match the enrolled agent name. That user owns
   keys and all worker state; separate processes organize lifecycle, not
   isolation from its tools. Keep state/configuration and IPC owner-only.
   New user defaults share HOME/.foundation (config, state, identity, run,
   workspaces, bin and guidance). Keep explicit existing paths; migrate them
   only while stopped, preserving keys/journals and native options. External
   tool configuration and platform service/discovery paths stay separate.
   Foundation still needs
   one dedicated SSH account/UID per identity, including Founder, separate from
   the unprivileged server. A new Agent1 uses `agent1` for Foundation SSH even
   when its local user is `alice`. Existing Foundation account names/UIDs stay
   retained; never rename the local account or move live state for this
   convention. Upgrade the account helper with the server before new named
   registrations. An authorized root-local account provisioner manages
   only that namespace. Pinned SSH forces `fdn-api`/`fdn-join` stdio into inner
   mTLS Unix ingress, with no arbitrary commands, PTY, forwarding or TCP/bearer
   bootstrap. Internal transport is protobuf protocol 2, not HTTP/gRPC.
   Prefer no attached cloud identity. Display attachment is not a
   desktop or consent.
6. For upgrades, inspect `work status` and native state; stop/join active work
   before replacement. Retain exact mode, identity, credentials, provider homes,
   knowledge, bindings, journals, outbox and session fences. No manual database
   edits or hidden `--force`/reset command exists. Founder can explicitly use
   `upgrade check`, `upgrade plan --input TARGETS --output PLAN`, `upgrade apply
   --input PLAN` and `upgrade status --input PLAN` after an operator bootstraps
   upgrade-capable binaries and each protected native policy/CA/operator key.
   Founder authority alone is not OS privilege. The helper accepts only exact
   bounded protobuf operations, validates the signed 15-minute proposal and
   fresh 30-second nonce start proof, then snapshots/stops workers before
   installing/verifying Foundation first. No ordinary message starts an update.
   Keep PLAN and its off-host age key private; native backups can include CA/
   identity/provider keys. Retain all original work fences and leave manual.
   A lost/failed install needs exact `upgrade status`, including when Foundation
   is offline; never rerun apply, renew its permit or auto-roll back SQL.
   Non-Founder targets are unattended. Only Founder may explicitly request a
   global mode pause/resumption; delegates and target-local commands cannot.
   Deploy the server migration before new workers and preserve unexplained
   legacy pauses for explicit Founder reconciliation, never silent resumption.
   A restored snapshot quarantines every identity/owner/key epoch; root-private
   fresh Founder recovery retains its ID/history, then Founder reapproves regulars
   with fresh signed keys. Manifest1/2 migration to SQL4 never reactivates old
   tokens/certificates, changes mode automatically or resets runner state.
   The transport cutover requires all four compatible binaries/guide; there is
   no native HTTP compatibility window. Resolve exact machines/state paths
   before any separately authorized clean redeployment or destructive action.
7. As the actual account, run `guide install`. It must preserve unrelated HOME
   instructions/skills; a collision is an actionable conflict, not permission to
   overwrite them. Worker initialization/startup also installs its guide.
8. Native archives include `agent-cli`, `agent-service`, `agent-identity` and
   `fdn-service`; Debian worker payloads include the first three. Verify the
   new binary and complete guide against the trusted manifest, not just an old
   launcher. Use `agent-identity service plan --input OPTIONS` read-only, then
   explicit current-user `agent-identity service install --input OPTIONS
   --apply` and `agent-identity service status --input OPTIONS`. These take no
   `--config`; OPTIONS is the current-user IdentityServiceOptions in
   DEPLOYMENT.md, not single-role agent-cli service options. User/UID/GID/HOME
   default from the actual OS account. Prepare distinct 0700 state and 0600
   configurations in that existing HOME; IPC parent/socket is 0700/0600.
   Existing HOME mode, groups and shell stay unchanged. Both Linux units use
   `systemctl --user`; both macOS services are the user's GUI LaunchAgents.
   Missing user-manager/login prerequisites fail, never invoke root, sudo or
   runuser or create accounts. Legacy split-account options need the explicit
   stopped, state-preserving migration in MIGRATION.md; never move keys/reset
   state automatically. Native manager/GUI and Windows runtime qualification
   remain separate; Windows adapters are not implemented.
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
