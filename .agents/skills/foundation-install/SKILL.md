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
5. Prepare the dedicated account's native prerequisites. Services/API/VNC bind
   to loopback; remote workers need pinned individual SSH and TLS routes.
   Prefer no attached cloud identity. Display attachment is not a desktop.
6. For upgrades, inspect `work status` and native state; stop/join active work
   before replacement. Retain exact mode, identity, credentials, provider homes,
   knowledge, bindings, journals, outbox and session fences. No manual database
   edits or hidden `--force`/upgrade command exists.
   Non-Founder targets are unattended. Only Founder may explicitly request a
   global mode pause/resumption; delegates and target-local commands cannot.
   Deploy the server migration before new workers and preserve unexplained
   legacy pauses for explicit Founder reconciliation, never silent resumption.
7. As the actual account, run `guide install`. It must preserve unrelated HOME
   instructions/skills; a collision is an actionable conflict, not permission to
   overwrite them. Worker initialization/startup also installs its guide.
8. Preview registration with `service plan --input OPTIONS`, then use
   `service install --input OPTIONS --apply` only under the approved native
   account/OS privilege. Linux workers need their user manager and correct HOME/
   user-bus environment; macOS needs the actual account's launchd domain.
9. Verify actual process owner, exact version/source, enabled/running manager,
   SQLite/TLS health, private listeners, SSH route and guide discovery. Do not
   equate accepted registration with final health.
10. Report installed/enrolled/authenticated/mode/work states separately. Route
    enrollment to foundation-enroll and protected login to foundation-provider-auth.

Use `agent-cli guide show COMMANDS.md` for exact setup/service arguments and
`guide show TEST_PLAN.md` for fixture versus native evidence limits. No root
tests, source builds, VM/container setup or paid inference is part of deployment.
