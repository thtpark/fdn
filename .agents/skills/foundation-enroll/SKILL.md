---
name: foundation-enroll
description: "Use when preparing signed public identity requests, enrolling or revoking additional Founder devices with independent keys and concurrent connections, registering a worker computer, attaching its identity proxy, recovering fresh keys, or granting project authority with pinned SSH and mutual TLS."
---

# Register and recover the correct identity

Read `agent-cli guide show DEPLOYMENT.md`, `guide show COMMANDS.md` and
`guide show SCHEMA.md` offline. `agent-identity --help` separately describes its
protected-account commands; it has no `guide` or `config init` leaf.

1. Resolve the computer, enrolled AgentID/role, existing installing user's
   UID/HOME, private identity/runner configurations, distinct state paths and actual
   version. A display name, provider sign-in or copied bearer file is not an
   identity. No v2 bearer invitation or token-only runtime is supported.
   Source wrappers use an isolated ignored `.local` HOME and testing identity.
   New user files share HOME/.foundation (config, state, identity and run).
   Explicit existing paths remain valid; move them only while stopped without
   rotating keys or resetting journals. External tool settings remain available.
   They do not inherit Founder authority or migrate a running owner. Use a
   signed new request and explicit old-owner stop/rebinding, never cloned keys.
2. Verify native prerequisites, private ownership and approved CA/SSH host pins.
   Each computer, including each Founder device, needs its own Foundation-host
   SSH account and UID. New registration derives an OS-safe lowercase username
   from its agent/device name (`Agent1` -> `agent1`); invalid results/collisions fail, never
   truncate, adopt or rename. The local worker uses its existing OS user; its
   username need not match and no new worker account is created. Existing Foundation names/UIDs
   remain unchanged during upgrade and recovery. The root-controlled gateway allows only `fdn-api`/`fdn-join` stdio,
   never commands, PTY or forwarding. Kernel peer UID and inner mTLS identity
   must agree; never weaken pins or share an SSH account to make connect work.
3. As the existing installing user, prepare private identity configuration/state
   under its HOME. Omitted/zero worker_uid/worker_gid means current user;
   explicit foreign values are rejected and native execution rejects
   root. Stop the identity service before offline preparation/profile changes.
   As that same user, use `agent-identity --config PRIVATE_CONFIG prepare --name
   NAME --output PUBLIC_REQUEST`. Export only the signed public request; keep
   both private keypairs and exact retry state in that account.
4. Initial Founder: an authorized operator passes that public request to
   `fdn-service init --founder-request PUBLIC_REQUEST` with every required Unix
   ingress/account-socket flag in DEPLOYMENT.md. The unprivileged server uses
   the explicitly configured root account helper; it never accepts TCP/bearer
   bootstrap. Initialization returns the public identity/CA path, not a ready
   profile-output command. An operator assembles the approved public profile
   with independently pinned SSH host/port/known_hosts. Preserve exact pending
   initialization if provisioning fails; do not reset or create another Founder.
5. Subsequent worker computer: only active Founder uses `agent-cli agent register
   --input PUBLIC_REQUEST [--output PUBLIC_PROFILE]`. Project delegates cannot
   register agents. Transfer public request/profile with authenticated integrity;
   it contains no private keys but is still a trust decision.
   For an additional Founder computer, prepare fresh keys there with a distinct
   device name, then use `agent-cli device register --input PUBLIC_REQUEST
   [--output PUBLIC_PROFILE]` from an existing authenticated Founder device.
   This preserves one Founder AgentID/role and work slot while creating an
   independent device/key/SSH account binding; never copy another device's keys.
6. As the target's existing worker user, use `agent-identity --config PRIVATE_CONFIG
   connect --profile PUBLIC_PROFILE`, then its `serve`. Connect stores only a
   validated public profile; serve activates the key binding and maintains
   device-authenticated owner control. Long-term keys and device/owner
   certificates never go into model/shared output or ordinary IPC replies.
   The same user owns the files; separate processes are not key isolation.
7. As that same existing user, run `agent-cli config init --identity-socket
   ABS` only for a missing runner configuration, then `agent-cli connect`.
   The private peer-checked Unix proxy must be active; the runner saves only
   public AgentID. Use `agent identity`, `doctor`, `status` and `agent list` to
   verify matching account/role/key-generation/owner state. Founder starts
   manual; regular agents use authoritative ai unless Founder pauses them.
8. Registration creates a new signed identity. For an existing enrolled agent,
   use explicit fresh-key recovery, then attach its validated profile and
   `connect` while retaining local SQLite1, knowledge, bindings and journals/
   outbox. Missing identity bindings and obsolete credential formats fail
   closed; never borrow another computer's keys, login or pending operation.
9. A proved second different live non-Founder owner key plus a fresh incumbent
   binary control ping bans the entire identity, including the original, and closes
   API/streams. Stale crashes/reconnects, ordinary CLI calls, public collisions
   and unproved attempts do not ban. Never clear a ban, keys or lock by hand.
   Founder permits multiple devices/connections, each with current device/owner
   proofs. Inspect `device list`; from another active Founder computer use
   `device revoke BINDING_ID --input FILE` with a nonsecret `reason` to revoke
   only that computer. The original binding uses AgentID; extras use device_id.
   Self-revocation and the last active computer are protected. Other device keys
   remain unchanged; native account cleanup may still be pending.
   Activation, owner proof and ordinary commands use version-2 protobuf sessions
   over pinned SSH/mTLS. Keep certificate/generation proofs: OS-only stale-SSH
   admission has not been natively qualified, and an account UID is not a key epoch.
10. Recovery requires fresh SSH/authentication keypairs, new signed public
    request, intentional reason and current authority. No `--rotate` exists:
    stop the service safely, preserve its old private StateDir and ALL runner
    state/journals, then choose a new empty user-owned 0700 StateDir inside
    the SAME existing user's HOME. Keep the private config file/IPC and runner paths
    stable; update the intended `state_dir` binding in config/service OPTIONS,
    then `prepare` the new public request. Normal retries never auto-rotate.
    Founder uses `agent recover AGENT --input NEW_PUBLIC --reason REASON
    [--output PUBLIC_PROFILE]` for a non-Founder. Founder itself requires
    authorized root-local `fdn-service recover-founder --input NEW_PUBLIC
    --reason REASON` on private maintenance, never an agent gateway/API exemption.
    It recovers exactly one enrolled restored/banned Founder, retaining
    AgentID/history and retiring every old additional Founder device; no extra
    target flag, second Founder or mode reset. After
    restore ALL identity/owner/key epochs are quarantined before publication:
    old tokens/certificates cannot return, and regulars need active Founder's
    explicit fresh-key reapproval, not copied credentials or silent resumption.
    Connect the new approved profile and restart the same native services;
    retain old private evidence. New generations do not revive old certificates,
    approvals or uncertain runs, and never reset the runner journals/outbox.
11. Project creation/delegation remains separate: `project authorize --input
    FILE`, `project create --input FILE`, and `project delegate PROJECT --input
    FILE`. Only Founder grants delegates or globally revokes with `agent revoke
    AGENT`; project authority does not confer enrollment or OS privilege.
12. Recheck current scopes/epochs and report observed public identity/account/
    generation states and remaining prerequisites, never private key material.
    Previously downloaded information cannot be recalled. Full software/RAM
    cloning or sequential use remains possible: do not claim perfect physical
    clone prevention, an implemented hardware signer or attestation.

Before enrollment or after Founder ban, use only an approved out-of-band human
handoff; absent one, report pending delivery. Provider sign-in, workspace tools,
mode and task readiness remain independent authorization steps. Native multi-UID
and hardware qualification are separate and have not been performed by fixtures.
Device enrollment does not add an iOS/Android/browser client or per-device
worker-management commands; use only actually supported compatible clients.
