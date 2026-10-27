---
name: foundation-develop
description: "Use when developing Foundation Go code, running isolated source wrappers, extending CLI/API/schema/providers or Founder software upgrades, updating documentation or skills, validating local tests, reviewing migration/recovery behavior, or preparing a source-bound main/release pull request."
---

# Develop using this repository only

Read `guide show DEVELOPMENT.md`, `guide show SCHEMA.md`,
`guide show TEST_PLAN.md` and `guide show IMPLEMENTATION_PLAN.md`.
The checkout's root documents are canonical; no old source tree is required.

1. Establish the current request/contract, source state and related tests.
   Preserve user changes, operational identities and retained evidence.
   `scripts/agent-cli.sh`, `scripts/agent_service.sh` and
   `scripts/agent-identity.sh` automatically refresh cached development builds,
   exec the selected program and isolate HOME/config/identity/provider file
   state under ignored `.local`. Go still compiles; there is no manual install
   or release packaging. Never copy a live owner or normal-home auth into tests.
2. Find existing helpers first. Keep server authority transactional, DTO/input
   validation narrow, errors explicit, and transport/provider/native boundaries
   testable. Do not enforce security only in a friendly CLI.
3. Investigate every affected surface: domain models, API/client, CLI help/input/
   exit shape, server policy, local journal/controller/manual path, providers/
   cancellation, native deployment, embedded docs/skills and packages.
4. Preserve exact replay, generation/epoch fences, completed-vs-done, creator/
   recipient ownership and the one-slot rule. Identity tests must distinguish
   separate-HOME business fixtures from actual Unix peer-UID/mTLS checks. Prove
   different live owner keys plus fresh incumbent binary ping before whole-identity
   ban (including Founder); crash/reconnect/CLI/unproved attempts do not ban.
   Workers use the existing installing user's UID/HOME for identity and tools;
   no new worker account/group is required. Keys stay private and out of model/
   shared output, not isolated from same-user tools. Keep Foundation root
   maintenance outside agent ingress.
   Root provision/disable RPCs must carry positive identity key generation:
   retain inventory/tombstones, reject stale or same-generation changed keys
   before OS calls, preserve UID/name on fresh-key recovery, and check current
   registry/request/key/generation around returned/post-bind tuples. No new
   CLI flag or generic root method is implied.
   Software/RAM cloning and sequential use remain possible; optional future
   hardware signing/attestation is not implemented or qualified. New provider
   adapters must prove non-prompting coding approval and tool-disabled text calls.
5. Add ordered compatibility-tested migrations, never rewrite shipped schemas
   or reset state. Application/schema contract is 2, native message protocol 2,
   server SQL migration 4, local SQL 1.
   Retain old identities/tasks/operations and private worker state without
   authorizing legacy bearer runtime. Explicit Founder binding and runner
   `connect --migrate` preserve the designated original non-Founder; a keyless
   legacy Founder needs root-local recovery. Future schemas fail closed; binary
   rollback is not automatically database rollback.
6. Update command/reference/skill behavior together. Skills need matching names,
   meaningful trigger descriptions and complete current procedures. Documentation
   must distinguish implemented capability, planned work and evidence limits.
   Keep cached diagnosis bodyless/read-only; Founder `diagnose --refresh` and
   exact closed-session `repair --input` share durable `agent operation` results.
   No queued/expired success claim, remote stopped-process assertion, journal/
   inference retry or native restart is implied. Recovery needs current exact
   fence/generations, terminal session and existing durable native cleanup proof
   under work.lock, with atomic mutation/receipt; old unproven fences require an
   operator. Keep dynamic server_observed_at outside immutable fingerprints and
   retained conservative server-time captured_at separate from skewable target
   UTC observations; use fresh server response plus monotonic request-start/RTT
   timing for capture/expiry, not target wall time.
   Preserve raw private evidence and test narrow real Unix-proxy method/depth/
   query/header boundaries without forwarding caller identity.
7. Native coordination uses bounded protobuf over SSH/mTLS and Unix IPC, not
   HTTP/gRPC or a downgrade path. Preserve kernel-derived principals and
   narrow typed capabilities; UID alone is not a key-generation proof.
   Run `go run ./tools/protocol --check` for the locked contract.
   Use the smallest related tests, then `bash scripts/test.sh all`. Independent
   selectors are static, unit, integration, e2e and stress. Tests use isolated
   state/HOME/ports and deterministic providers; no VM, container, root/host
   service registration, real credentials or paid inference.
8. For guidance changes, verify frontmatter, links, CLI coverage, embedded
   catalog/source hashes, preservation of home instructions/unrelated skills,
   both discovery locations and explicit prompt context. Native/reproducible
   build lists, archive checks/activation and Debian worker payloads must include
   `agent-identity`; test with synthetic payloads, never local release packaging.
   Keep its offline help distinct from the three guide-capable binaries.
   `agent-identity service plan/install/status` takes current-user OPTIONS, not
   `--config`; test defaults from the actual OS user, independent agent names,
   one existing account, both user services, no sudo/runuser/account creation or
   HOME chmod, private IPC and explicit stopped legacy migration with retained
   state. Windows adapters remain separate, unimplemented work. Fresh recovery
   uses a new empty sub-StateDir in the same user's HOME and retains old keys/
   all runner state; never invent `--rotate` or rotate during normal retries.
9. PRs into main run separate Linux/macOS suites. Real packaging only follows
   an authorized PR merge into release on the exact source commit. Never
   fabricate a local Actions event or build/test on a deployment target.
   The all-platform publisher verifies exact artifacts and pre-merge acceptance,
   creates all versioned GitHub release assets as a draft, then publishes once.
   Never overwrite a release/tag or let updater discovery fall back to Actions
   artifacts. Permission signatures for native upgrade permits/start leases
   are not artifact code signing/attestation.
   Test Founder-only `upgrade check/plan/apply/status`, administrator-pinned
   native policies/operator SSH, current nonce-bound start authority, encrypted
   cold backups, server-first order, preserved keys/journals/fences, unknown/
   expired/changed-source denial and uncertain replay without native reruns.
10. Verify reproducible builds, safe extraction, notices/guidance, trusted
    manifest and native installation behavior before a rollout. New immutable
    contents require a new VERSION. Signing/attestation and live-provider/
    desktop/full-host recovery remain separate qualification.

For future features use the roadmap, not invented CLI leaves. The 1,000-agent
target requires an agreed load/SLO benchmark and traffic reduction, not a claim
based on 1,000 stored tasks. Do not introduce speculative brokers/plugins/
distributed storage or incur cloud/model costs merely to appear scalable.

Finish with source/version, actual tests and outcomes, remaining risks, and
deployment status only if separately authorized and observed.
