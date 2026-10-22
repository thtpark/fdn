# Foundation verification plan

The source and its own tests are authoritative. Verification needs no VMs,
containers, root, database daemon, provider subscription or paid inference.
Do not import a previous implementation's test counts/coverage as Go evidence.

## Independent suites

| Command | Coverage of the contract |
| --- | --- |
| `bash scripts/test.sh static` | Formatting, module checksums, vet, native/three-target builds, shell syntax |
| `bash scripts/test.sh unit` | Race-checked domain/policy, SQLite/local state, private files, CLI, trust/backup, provider adapters/staging, guides, native definitions, archive/installer/workflow contracts, bounded scripted-provider and isolated Git/PR fixture contracts |
| `bash scripts/test.sh integration` | Race-checked real local TLS, native OpenSSH against an in-process SSH fixture, raw API authority, competing claims, privacy/pagination, consented streams, manual/daemon admission, lost acknowledgements and orphan/crash fences |
| `bash scripts/test.sh e2e` | Actual compiled binaries; deterministic Codex/Copilot executables; files changed in approved temporary workspaces; onboarding, multi-round preparation/signoff, independent delegated PR review/rework/merge against isolated Git/Git-host fixtures, pause/failure, manual cancellation, daemon kill/restart and explicit recovery |
| `bash scripts/test.sh stress` | Race-checked 1,000 tasks, keyset pagination, 24 concurrent writers, 192 accepted edits and exactly three retained histories |
| `bash scripts/test.sh all` | All independent suites, sequentially |

`go test ./...` runs ordinary unit/tooling tests. Integration, e2e and stress
packages use explicit build tags. The race detector needs its platform C
compiler; deployable Go binaries remain CGO-free.

Every fixture owns disposable directories/ports/processes and private fake
HOME directories. Tests must not register host services, change SSH/firewall
policy, touch real skills/configuration/credentials, or use a deployed endpoint.
Cancel only owned processes, using exact process identities/process groups.

## Documented test cases

The [test-case catalog](tests/e2e/SCENARIOS.md#test-case-catalog) records the
requested delegated-workflow scenario and its directly related regressions, with
stable IDs, preconditions, actions, expected results, executable test names and
run commands:

| Case family | Documented coverage |
| --- | --- |
| `WF-001` / `WF-002` | Founder sign-in handoff, both provider-role swaps, four clarification rounds, exact approval, one PR, delegate review, rework, merge, done, and feedback receipts |
| `VF-001`–`VF-018` | Verification authority/fences, competing claims, shared slots, invalid reports, revocation, pause/resume, cancellation, replay and recovery |
| `POL-001` / `POL-002` | Named/independent approvers and structured provider-decision validation |
| `FX-001`–`FX-007` | Deterministic provider plans and isolated Git/Git-host fixture contracts |
| `AUTH-001`–`AUTH-008` | 15-minute sign-in expiry, immutable legacy/retry evidence, Founder-only refresh, paused/offline targets, protected API fields and no implicit login/inference |
| `HELP-001`–`HELP-005` | Requested-action help before onboarding, expired older notices, same-project task/reply/explicit-operation deduplication, current authority routing, permission boundaries and mid-work sign-out with recovery fences |
| `SEC-001`–`SEC-010` | Coordinator-confirmed strict Unix/mTLS source fixture results, including full integration and enhanced both-controller ban/no-ban regressions; final all-suite acceptance pending, not native multi-UID qualification |
| `ATT-001` / `ATT-002` | Coordinator-confirmed compiled public-only attachment/migration/retired-flag cases in full e2e; test-local business proxy, final all-suite pending |
| `NAT-001`–`NAT-006` | Paired lifecycle, exact account/config and fresh-StateDir recovery plus root generation/tombstone replay cases; subsequent native-source race/vet rerun reported passing, final coordinated all-suite pending, not live native qualification |
| `LIVE-001` | Separately opted-in real GitHub boundary check; excluded/skipped by default, not a passed live qualification |

This is a concrete catalog for the workflow introduced here, not an exhaustive
inventory of every test function in the repository. Other suite coverage remains
summarized below. New scenarios should add an explicit catalog entry alongside
their tests; planned/unimplemented cases must not be described as covered.

## Change-to-test map

| Change | Minimum related cases |
| --- | --- |
| DTO/input/CLI | Unknown/protected fields, bounded UTF-8 input, exact JSON envelope/exit code, every implemented leaf's offline help |
| Identity registration/recovery | Signed public-key possession, Founder-only registration, explicit original-agent migration, canonical-key/request/account/UID uniqueness, exact activation challenge/replay, retired generations, no legacy bearer runtime, fresh-key recovery and root-local-only Founder recovery |
| Tasks/policy | Named/independent eligibility, project epochs, invalidation, status gates, concurrent stale revisions, exact execution/verification claims/reports; verifier's current spec/result/discussion/epoch, independent own-provider selection, shared server slot, atomic feedback/rework approval, revoked authority/Founder pause and no self-acceptance |
| Discussions/messages | Creator-only edits, literal mention/body generation, requested answers, private recipients, no automatic protected replies |
| Persistence/restore | Reopen/migration/future-schema rejection, recognized backup-manifest schema 1/2 through SQL3 and future rejection, prepublication ALL-identity/owner/key quarantine, retired credential denial, sole legacy/restored Founder root-private fresh-key recovery retaining ID/history, regular fresh-key reapproval, unchanged mode/uncertain work, atomic transaction failures, immutable operation fingerprints, three-history bounds, integrity |
| Worker/controller | One slot across manual/service/session/execution/verification, onboarding once, no idle inference, partial results/questions, retry/backoff/budget, offline report replay and no duplicate inference/review/merge; prepared review reply, claim-ack recovery, uncertain verifier failure, discussion/mode/workspace cancellation, disabled-binding preservation and manual resolution |
| Providers/processes | Exact required flags, text tool isolation, prompt through stdin, private/bounded output, version/model pinning, cancellation of owned descendants, failed signed staging retains previous version; actual login PATH/bare command versions, profile preservation/collisions, current-version dispatch and real foreground-terminal input; missing-authentication to real Founder/delegate inbox before onboarding without inference, private/protected notice fields, exact replay across restart/lost ack, explicit pending failures, 15-minute expiry/legacy deadlines, no automatic renewal or receipt revival, Founder-only typed refresh including paused/offline targets, scope/authority checks, no notification spam or auth inferred from receipt acknowledgement |
| Sessions | Request != consent != explicit open, current scope/expiry/revocation, no public listener, restart retirement, cleanup fences |
| Autonomous control | Only Founder pauses/resumes; target/delegate/offline overrides denied; event-driven cancellation and server claim gate; version-1 server migration preserves identity; legacy unexplained manual pause fails closed |
| Persistent assistance | Only Founder issues/revokes; exact requester/target/purpose/mode/provider/project; no-project authentication bootstrap; target prepares unattended without local consent; disconnect/restart/expiry retains grant; role/identity/revocation closes streams; delegate is not assumed human |
| Native identity/ingress | Recording account adapters/in-process SSH, unique UID/account/key reservations, positive per-account generation on typed root provision/disable; stale/same-generation-key-change rejection before OS calls, exact replay, never-provisioned disable tombstones, higher-generation fresh-key UID/name preservation and pre/returned/post-bind/current registry tuple checks; no arbitrary commands/PTY/forwarding, wrong-peer/pin/certificate rejection, private root maintenance outside agent gateway; no host users/sshd/services changed |
| Owner/runner isolation | Device versus ephemeral owner certificate purposes, instance/agent/key/owner-generation agreement, kernel peer UID checks, runner cannot read keys or use control routes, one local proxy owner across CLI traffic; confirmed distinct live owner proofs plus fresh incumbent WS ping ban original and contender including Founder, while stale crash/reconnect/unproved attempts do not; ban cancels API/streams, recovery keeps work fenced |
| Native services | Recording adapters by default, distinct non-root identity-service and runner UIDs/HOMEs, system identity service versus runner user service/GUI agent, raw scalar systemd paths, user-bus environment only for registration, Linux parser without starting a service |
| Guidance/docs | Valid skill names/frontmatter/descriptions; local links/command coverage; no legacy checkout dependency; hashes/version completeness; preserve home instructions/unrelated skills; prompt inclusion; isolated homes; native and Debian guide content |
| Release/install | Merged-release-only event/commit guards, pinned Actions, two matching builds including agent-identity, native/worker binary lists and executable bytes/modes, all four archive binaries allowed/required/activated, missing identity payload denied before prefix changes, Debian parent directories and real unprivileged extraction with executable hashes, trusted manifest, immutable/idempotent installation and state preservation |

Use existing helpers and one runner invocation with related selectors rather
than repeatedly running the entire suite. Add faults/races at the actual
transaction/process/network boundary, not just a proxy string assertion.

## Delegated pull-request scenario

Run the native two-provider scenario independently:

```sh
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeDelegatedPullRequestWorkflow$' -v
```

The scenario swaps Codex/Copilot assignee and delegate roles, mocks Founder-arranged
sign-in, incorporates four actual requested replies, waits for exact Founder
signoff, then exercises real Git edits and one PR through independent review,
in-scope rework, corrected-head merge and done. The loopback Git-host fixture
models `https://github.com/thtpark/openai-python` with tiny original files; it
never contacts that repository. Assertions check actual content/diff, exact
actor/provider/spec/result attribution, no pre-approval tools, atomic comments,
handled rework feedback without an attention echo, and idle/restart replay.
It verifies Python fixture/test-file contents and Git whitespace checks, not
upstream Python tests or live-model quality.

The script/loopback fixture's unit tests run in the standard unit selector.
Extend phase/context rules and bounded actions for new scenarios rather than
production task-title or repository switches. Source-only harness details and
explicit live GitHub opt-in instructions are in
[SCENARIOS.md](tests/e2e/SCENARIOS.md). Live checks require both `e2e` and
`e2e_live` build tags, an explicit environment flag/repository and two separately
authenticated private GitHub CLI configurations. The normal suites cannot run
live writes, even with inherited opt-in variables. That optional boundary check
is not a live-provider or end-to-end Foundation qualification and is not implied
by passing fixture tests.

## Software-first identity security acceptance

API/schema contract 2, server SQL 3 and preserved local SQL 1 are separate
version checks. On 2026-10-21 the coordinator confirmed `SEC-001`–`SEC-010` and
`ATT-001`/`ATT-002` source fixtures in full integration (25.673s) and e2e
(94.630s); enhanced both-controller PolicyViolation ban notification and
invalid/replayed-proof no-ban `TestUnix` passed (8.432s). See
[SCENARIOS.md](tests/e2e/SCENARIOS.md#security-boundary-cases-source-suites-verified-final-acceptance-pending).
These are working-tree fixture results, not a final all-suite pass for later
restore/lifecycle/account-generation replay hardening or native deployment.
Recording lifecycle source tests were also reported passing before the latest
account-generation addition. The native source owner subsequently reported the
full race ingress/platform/CLI rerun passing (2.594s/2.958s/3.836s) and vet after
generation/root-alias/group changes, including actual Unix RPC stale replay and
post-bind revocation races. Enhanced restore and final coordinated all-suite
acceptance remain pending; none of these are live host qualification.

The real Unix/TLS tests use strict `api.New`, `ingress.Listen`, kernel peer
credentials, inner TLS/mTLS and owner WebSockets. One actual unprivileged UID
is exercised; alternate UID bindings are logical negative fixtures, not newly
created OS accounts. In contrast the existing business harness explicitly
uses `api.NewAuthenticated`, direct Store setup and test-local worker proxies.
Its separate HOME/state directories prove business isolation only, not the
production account/certificate boundary. Keep both fixture types explicit.

Source-level regression areas, without claiming a current passing result:

- `internal/device`, `internal/pki`: independent signed keys, canonical formats,
  proof field binding, certificate purposes/epochs and validity.
- `internal/foundation/security*_test.go`: Founder-only registry, UID/key/request
  races, migration/replay, whole-identity ban, fresh-key recovery, Founder-local
  recovery and durable work/generation fences.
- `internal/ingress`, `internal/transport`: actual peer UID, controlled Unix
  sockets, exact forced selectors, no forwarding/arbitrary commands, pinned
  native/in-process SSH, cancellation and observable cleanup errors.
- `internal/identityservice`: public-only preparation/profile retry, key/runner
  UID isolation, socket-peer checks, one owner across CLI traffic, fresh WS ping,
  proactive device renewal with the same per-start ephemeral owner, owner
  certificate rotation and expiry/denial stream cancellation without fallback.
- `internal/platform`: recording account provisioning/retirement, native
  identity-system-service versus runner-user-service plans and paired lifecycle
  preflight/status/install. Cover exact UID/GID/HOME/group/managed markers,
  preexisting private configurations/socket bindings, fixed native paths and
  arguments, explicit apply/root/OS guards, real-runner manager privilege drop
  and interrupted registration without account adoption or host changes.
- `cmd/agent-identity`: offline service plan/help without `--config`/enrollment,
  explicit install apply, native status and rejected lifecycle/private-command
  flag mixing. `internal/identityservice` recovery fixtures preserve old private
  state and all runner state while fresh preparation uses an empty sub-StateDir
  in the same identity HOME; normal retry never rotates keys.
- `tests/integration/identity*_test.go`: raw Unix/TLS boundary, certificate/UID
  mismatch, signed activation, Founder-only registration, fresh owner proof,
  stale/crash versus confirmed simultaneous owners, ban including Founder and
  explicit fresh-key recovery. Run with `-tags=integration -run '^TestUnix'`.

Native multi-UID filesystem/account/sshd/systemd/launchd/root-recovery/reboot
qualification is **not performed** by these rootless/recording fixtures. No real
host account, sshd or service change, real credential or paid inference belongs
in the default suites. Software keys/RAM can be cloned or used sequentially;
perfect physical-clone prevention is not proved. Hardware adapters/attestation
are optional future work, not implemented or qualified.

## Documentation and discovery checks

All six `.agents/skills/*/SKILL.md` files must have matching lowercase/hyphenated
names and useful trigger descriptions. Every supported command in executable
help must appear in [COMMANDS.md](COMMANDS.md). Source links stay within this
repository (external vendor references may supplement, never replace, the
operational procedures).

Version-matched guide assets must be readable from binaries with no checkout,
configuration, enrollment or network. Source, native archive and both Debian
role payloads contain the same guide. Installation preserves existing HOME
instructions and unrelated skill folders and refuses unmanaged collisions or
tampered managed assets rather than overwriting them. Automatic/manual controller
prompts receive bounded guide context even when provider discovery is disabled.

## CI and release qualification

PRs into `main` independently run static, unit, integration, e2e and stress jobs.
Native suites run on Linux and macOS. Real packages are made only after a PR
merges into `release`; direct pushes, tags, manual dispatch, open PRs and
unmerged closures cannot package.

Release jobs compare two independently invoked trimmed builds, validate Debian
metadata/extraction and guidance/notices, and publish source-bound metadata and
checksums. Do not fabricate merged events to package locally. Tiny synthetic
installer/archive fixtures are tests, not release packages.

## Evidence limits and acceptance reporting

- Record source revision, toolchain, OS/architecture, suite, actual outcome and
  fixture/live boundaries. No skipped prerequisite is a passed case.
- Synthetic providers prove adapter/protocol/controller behavior, not live
  authentication, vendor resume semantics, model quality, or spending authority.
- Recording native adapters do not prove administrator/systemd/launchd/desktop
  behavior. Separately authorized native checks must report their own evidence.
- Reboot/health on a deployed instance is not a full clean-host disaster restore.
  Encrypted SQLite/trust restore fixtures do not restore provider homes or OS
  configuration.
- The existing stress case is not a 1,000-agent transport/API capacity test.
  Before such a claim, agree workload and thresholds and measure as specified in
  [CAPACITY.md](CAPACITY.md).
- No current overall line/branch coverage gate or production security
  certification is claimed. Add a measured, supported coverage contract before
  advertising a numerical threshold; do not inherit historical Rust figures.
