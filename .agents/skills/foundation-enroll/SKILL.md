---
name: foundation-enroll
description: "Use when preparing Founder or Agent1 signed public identity requests, registering or migrating a computer, attaching its isolated identity proxy, recovering a banned identity with fresh keys, or granting project authority with pinned SSH and mutual TLS."
---

# Register and recover the correct identity

Read `agent-cli guide show DEPLOYMENT.md`, `guide show COMMANDS.md` and
`guide show SCHEMA.md` offline. `agent-identity --help` separately describes its
protected-account commands; it has no `guide` or `config init` leaf.

1. Resolve the computer, enrolled AgentID/role, identity-service UID/HOME/private
   configuration, distinct provider-runner UID/HOME/configuration and actual
   version. A display name, provider sign-in or copied bearer file is not an
   identity. No v2 bearer invitation or token-only runtime is supported.
2. Verify native prerequisites, private ownership and approved CA/SSH host pins.
   Each identity, including Founder, needs its own Foundation-host SSH account
   and UID. The root-controlled gateway allows only `fdn-api`/`fdn-join` stdio,
   never commands, PTY or forwarding. Kernel peer UID and inner mTLS identity
   must agree; never weaken pins or share an SSH account to make connect work.
3. An authorized native setup provides the protected identity configuration;
   native identity execution rejects root and the runner's UID. Stop that
   identity service before offline preparation/profile changes. As its own
   identity account, use `agent-identity --config PRIVATE_CONFIG prepare --name
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
5. Subsequent computer: only active Founder uses `agent-cli agent register
   --input PUBLIC_REQUEST [--output PUBLIC_PROFILE]`. Project delegates cannot
   register agents. Transfer public request/profile with authenticated integrity;
   it contains no private keys but is still a trust decision.
6. As the target identity account, use `agent-identity --config PRIVATE_CONFIG
   connect --profile PUBLIC_PROFILE`, then its `serve`. Connect stores only a
   validated public profile; serve activates the key binding and maintains
   device-authenticated owner control. Long-term keys and device/owner
   certificates never go to providers or runner state.
7. As the different runner account, run `agent-cli config init --identity-socket
   ABS` only for a missing runner configuration, then `agent-cli connect`.
   The private peer-checked Unix proxy must be active; the runner saves only
   public AgentID. Use `agent identity`, `doctor`, `status` and `agent list` to
   verify matching account/role/key-generation/owner state. Founder starts
   manual; regular agents use authoritative ai unless Founder pauses them.
8. For a designated original existing non-Founder, Founder first registers its
   freshly prepared request using `--existing-agent AGENT`; only then use that
   runner's `connect --migrate`. Preserve local SQLite 1, knowledge, bindings,
   journals/outbox and identity. A keyless legacy Founder requires root-local
   recovery before it can register others. Migration never borrows another
   computer's state/keys/provider login or erases a pending operation.
9. A proved second different live owner key plus a fresh incumbent binary control
   ping bans the entire identity, including the original and Founder, and closes
   API/streams. Stale crashes/reconnects, ordinary CLI calls, public collisions
   and unproved attempts do not ban. Never clear a ban, keys or lock by hand.
   Activation, owner proof and ordinary commands use version-2 protobuf sessions
   over pinned SSH/mTLS. Keep certificate/generation proofs: OS-only stale-SSH
   admission has not been natively qualified, and an account UID is not a key epoch.
10. Recovery requires fresh SSH/authentication keypairs, new signed public
    request, intentional reason and current authority. No `--rotate` exists:
    stop the service safely, preserve its old private StateDir and ALL runner
    state/journals, then choose a new empty identity-owned 0700 StateDir inside
    the SAME identity HOME. Keep the private config file/IPC and runner paths
    stable; update the intended `state_dir` binding in config/service OPTIONS,
    then `prepare` the new public request. Normal retries never auto-rotate.
    Founder uses `agent recover AGENT --input NEW_PUBLIC --reason REASON
    [--output PUBLIC_PROFILE]` for a non-Founder. Founder itself requires
    authorized root-local `fdn-service recover-founder --input NEW_PUBLIC
    --reason REASON` on private maintenance, never an agent gateway/API exemption.
    It adopts/recovers exactly one existing legacy/restored Founder, retaining
    AgentID/history; no extra target flag, second Founder or mode reset. After
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
