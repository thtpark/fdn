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

## Binary SSH message cutover acceptance

Native protocol 2 uses protobuf framing, kernel-bound Unix peers, pinned SSH,
retained mTLS/owner proofs and typed capabilities. HTTP business fixtures are
explicit adapters, not native compatibility or OS-isolation evidence.

Focused checks include:

```sh
go run ./tools/protocol --check
go test -race -count=1 ./internal/message ./internal/api ./internal/identityservice ./internal/foundation ./internal/work ./internal/remote ./internal/platform
FDN_MESSAGE_FLEET=1 go test -count=1 ./internal/api -run '^TestMessageFleet1000ProtobufPushAcceptance$' -v
```

The approved isolated profile has 1,000 logical identities, zero periodic
business-discovery polling, update p95 <=250 ms/p99 <=1 second and priority
cancellation p99 <=1 second. The harness uses temporary real SQLite, distinct
ephemeral mTLS certificates and loopback TLS/protobuf with a trusted logical
principal binding. It measures real mutations/delivery, unrelated idle
subscribers, replay, privacy and resource cleanup, not merely echo messages.
Compare instrumented race and uninstrumented timings separately and retain
failed runs; do not relax thresholds or turn a harness into native fleet proof.
The eight-identity unit fixture checks functional invariants and reports timing,
but does not apply a fleet p95 to four samples on arbitrary shared CI hardware.
Only the explicit 1,000-identity acceptance profile enforces the approved SLOs.

Wire regressions cover bounded malformed frames/collections, exact integers and
presence, protocol refusal, blocked-handler ping/cancel, typed faults and
explicit public-error opt-in. State tests cover transactional private updates,
rollback/replay, signed generation-bound cursors, race-free waits and shutdown.
Relay deadline tests retain a bounded in-flight read result across a local
timeout so late bytes cannot be discarded; uncertain writes close that relay.
Writer-admission tests observe queued/admitted writers directly, not total
pool use that also includes legitimate concurrent snapshot readers.
Copilot status-probe cancellation closes owned stdin/stdout pipes before
joining the child process, so a killed child cannot leave RPC reads and
`exec.Cmd.Wait` waiting on each other. Native sign-in shutdown is repeated on
Linux; forced-shutdown failures retain a diagnostic stack.
Native-source tests cover real Unix/mTLS admission and owner control, protected
maintenance/provisioning, actual SSH stdio adapters and cleanup. Worker tests
cover busy/paused controls, prompt status/health, due-only retries and durable
cursor/pending generations. Full independent suites remain required.
Projected local fence recovery must also converge when an entire fence
transition occurs between safety ticks without changing the stored work phase.

Observed on 2026-10-25 for the 0.3.0 worktree based on `ae390a55`, Go 1.27.1,
darwin/arm64: `bash scripts/test.sh all` passed every independent suite. The
unchanged approved fleet profile passed both ordinary and race builds; detailed
measurements, retained failed runs, root-cause corrections and native evidence
limits are in [CAPACITY.md](CAPACITY.md). This does not claim a release,
deployment, Linux runtime CI, 1,000 native SSH accounts or hardware assurance.

## Agent-based accounts and name mentions

Focused source regressions:

```sh
go test -race -count=1 . ./internal/domain ./internal/foundation ./internal/platform ./internal/cli ./internal/message ./cmd/agent-identity
go run ./tools/protocol --check
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeBinariesPhaseOneThroughFive$'
```

- Username normalization checks lowercase agent names, separators, exact
  32-character bounds and rejection of root, empty, numeric-leading,
  multiline and unusable names.
- New signed registrations reserve normalized names; collisions roll back
  partial agents/keys. Existing ID-derived names survive reopen, binding and
  fresh-key recovery. Named and legacy root inventories coexist without a reset.
- Recording and actual Unix/protobuf account-helper tests cover the exact
  approved username, returned-name mismatch, no unmanaged adoption, retained
  UID/name/generation, named never-provisioned tombstones, stale requests and
  changed-name rejection before OS tools. Foundation account naming remains
  independent of the existing worker's local OS username.
- Name/ID mention tests cover case-insensitive and spaced names, canonical
  sorted/deduplicated IDs, self-exclusion, unchanged body, replay after a name
  becomes ambiguous, atomic unknown/ambiguous-name create/edit failures,
  inactive names, legacy ID compatibility, blocking answers and stale receipts.
  Worklogs do not create attention. Compiled-binary e2e submits a name mention
  and acknowledges the resulting ID-bound attention item.
- Offline CLI help and embedded skill/catalog/link/hash/home-preservation tests
  document the same contract. No source fixture creates host accounts,
  registers host services or qualifies a native deployment.

### Automatic mention replies

```sh
go test -race -count=1 -tags=integration ./tests/integration -run '^TestMention(ReplyUsesRecipientProviderAndModel|AuthenticationBeforeOnboardingUsesRecipientProvider)$'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeMentionReplyUsesRecipientProviderAndModel$'
```

Both Codex and Copilot recipients use their own preferred provider/model even
when another assignee's task pins an unavailable provider or model. Integration
tests cover before/after onboarding, retained project-scoped sign-in routing,
tool-disabled invocation, published parent-linked replies, handled attention,
accurate health metadata, unchanged execution authority and restart without
duplicate inference/help. Compiled-binary tests deliver a name mention to an
already-running daemon through scoped updates and verify its actual reply,
receipt and provider/model journal attribution. These fixtures do not certify
live provider sign-in, deployment or model quality.

## Existing-user worker ownership

```sh
go test -race -count=1 . ./internal/config ./internal/identityservice ./internal/platform ./cmd/agent-identity
CGO_ENABLED=0 go test -count=1 ./internal/platform -run '^TestIdentityServiceUserDefaultsIgnoreClaimedEnvironment$'
```

Current-user regressions prove:

- One existing OS account/HOME owns both processes and all worker state,
  independently of enrolled agent name. Defaults resolve the actual OS user
  rather than claimed environment values and work in CGO-free macOS builds.
- Both Linux definitions use the user manager; both macOS definitions are
  current-user GUI LaunchAgents. No root/system worker service, sudo, runuser,
  hidden account, named group, managed marker or no-login shell is required.
  Missing existing user-manager/login prerequisites fail before writes.
- Real owner-only Unix IPC accepts that same user with omitted UID/GID defaults,
  refuses a foreign peer and retains one owner across ordinary CLI traffic.
  The user can read its own key files; this is not same-user key isolation.
- Actual-user file fixtures preserve an existing 0755 HOME, user-owned binaries,
  exact-retry service files, private keys and runner evidence. A legacy 0710 IPC
  parent is rejected, not silently chmodded. Split-account OPTIONS and explicit
  foreign private UID/GID settings fail before automatic key/state transfer.
- Recovery, private route allowlists, certificates, duplicate-owner proofs,
  server generation fences and runner journals remain covered separately.
  Recording manager/file fixtures do not perform host service registration,
  cross-UID administrator migration or Windows runtime qualification.

## Isolated source runners and Founder upgrades

Focused source acceptance:

```sh
go test -race -count=1 . ./internal/upgrade ./internal/foundation ./internal/api ./internal/identityservice ./internal/cli ./internal/distribution ./tools/publish
go run ./tools/protocol --check
```

`TestDevelopmentScripts*` uses an owned disposable checkout, mock Go builder and
isolated HOME. It checks all three source entrypoints, `.local/.foundation`
configuration/state, unchanged real HOME and inherited external-tool/credential
settings (including unset overrides), literal arguments/stdin,
same PID/native exit status, rejected symlink escape and failed-build cleanup.
The actual source wrapper `--version` can additionally prove the development
stamp without enrollment, production access, provider authentication or inference.

Default-path tests verify XDG-independent `.foundation` configuration, runner
state/workspaces/socket, explicit legacy configuration support and refusal to
silently reset an old default identity. Guide tests cover its consolidated store,
exact legacy discovery-link migration, retained old evidence and unchanged
unrelated instructions. Provider shell tests cover `.foundation/bin`, the
managed login PATH block and existing unrelated command/profile preservation.

Upgrade cases cover stable semantic versions and no downgrade/same-version
replacement; pinned GitHub hosts, independent API ZIP digest, exact manifest/
metadata/payload names and hashes; all-platform durable publication with exact
source, immutable tags/releases and pre-merge acceptance.

Store/raw API/protobuf tests prove current Founder-only context/authorization,
paused exact target key generation, all-agent pause for Foundation, active
execution/independent verifier/assistance admission, bounded immutable receipts,
expired/foreign proposals and no replay renewal. Fresh nonce-bound start leases
recheck current issuer/target generations and active work before stopping.
Root-maintenance service leases are not ordinary agent recovery or arbitrary
root operations; proxy method/depth/query allowlists remain strict.

Native updater tests use synthetic ZIP/native bytes, actual-user private files,
recording command runners and injected release/start-lease boundaries. They
decrypt cold snapshots using only the off-host fixture key, preserve original
identity/fences, verify greater source activation and exact receipt replay,
reject busy/revoked/altered requests before service stop, and retain uncertain
installer phases as operator-required rather than repeating external effects.
Founder CLI tests exercise actual typed local API calls and bounded protobuf
operator inspection/challenges, Foundation-first queued dependencies, exact plan
state, private age keys and offline native status. Target receivers, not the
sender CLI, perform native preparation/install.

These are source fixtures, not a live privileged Linux/macOS package rollout,
new provider login, production signing/security certification, full-host restore,
reboot/Windows qualification or large-fleet acceptance. Full independent source
suites remain required. Do not mark publication, policy provisioning, Founder
migration or deployed upgrades complete from a passing local test.

## Message-first operational requests

```sh
go test -race -count=1 ./internal/foundation ./internal/work ./internal/upgrade ./internal/api ./internal/cli ./internal/identityservice ./internal/provider
go run ./tools/protocol --check
```

Request tests cover recipient-only accepted/deferred/declined/executing/
completed/failed decisions with reasons, private threaded update replies, no
manufactured read/approval, immutable terminal decisions, linked independently
authorized insistence and exact replay. Prepared request-answer replay must
produce no extra inference or duplicate result after a lost acknowledgement.
Upgrade CLI fixtures assert zero sender-side native prepare/install calls:
only signed exact-permit messages are queued with Foundation-first dependencies.
Native receiver fixtures prove target-owned accept/execute/result flow,
revoked-authority refusal and failed-dependency defer before effects, missing
acknowledgement rejection, exact accepted/executing/completed reply replay,
interrupted execution without a native job, and durable pending failure delivery
while identity transport is unavailable. Neither result replay nor restart may
repeat installation. Task-request tests recheck assignment/specification/archive/
readiness at server admission, require an actual run/new latest completed result,
and report paused/blocked/failed outcomes without another inference or false
completion. Binary API tests cover private threads, exact recipient decisions
and denied ordinary access to Foundation maintenance. Thread tests admit exactly
200 messages in insertion order and explicitly refuse larger views.

`TestMessageRequestMigrationFivePreservesSQLFourRecordsAndUpdates` checks
message bytes/order, mode, operations, scoped feed/cursors, private update triggers,
ordinary enrolled-party constraints and SQL4-binary refusal. Existing migration/
restore fixtures now advance through SQL5 without identity or state resets.
Existing paused-mode, work/session/identity/approval fences remain required.
Foundation's root-private receiver is not an ordinary agent API privilege.
These fixtures do not certify live OS supervision, a message-driven production
rollout, provider execution, reboot recovery or cross-version protocol upgrades.

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
| `LOGIN-001`–`LOGIN-003` | Supported headless/device-flow selection, legacy fallback, bounded help-only probes, local/remote human-visible sign-in and public links without exposing credentials |
| `VNC-001`–`VNC-008` | Actual display-only inventory, scoped screen/app/fallback grants, protected target help, browser loopback/Host/Origin/session authorization, human-only credentials, native-decoration placement/bounds, runner-owned Xauthority during privileged service inspection and proven cleanup |
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
| Diagnostics CLI/guidance | Typed bodyless cached GET, explicit durable Founder refresh/repair with exact replay, strict arity/flags/private-field rejection, common operation states without freshness fallback, real Unix-proxy exact method/depth/query/header bounds, existing config/enrollment/identity matching, executable help and embedded command/catalog/skill contracts |
| Identity registration/recovery | Signed public-key possession, Founder-only registration, explicit original-agent migration, canonical-key/request/account/UID uniqueness, exact activation challenge/replay, retired generations, no legacy bearer runtime, fresh-key recovery and root-local-only Founder recovery |
| Tasks/policy | Named/independent eligibility, project epochs, invalidation, status gates, concurrent stale revisions, exact execution/verification claims/reports; verifier's current spec/result/discussion/epoch, independent own-provider selection, shared server slot, atomic feedback/rework approval, revoked authority/Founder pause and no self-acceptance |
| Discussions/messages | Creator-only edits, literal mention/body generation, requested answers, private recipients, no automatic protected replies |
| Persistence/restore | Reopen/migration/future-schema rejection, recognized backup-manifest schema 1/2 through SQL4 and future rejection, prepublication ALL-identity/owner/key quarantine, retired credential denial, sole legacy/restored Founder root-private fresh-key recovery retaining ID/history, regular fresh-key reapproval, unchanged mode/uncertain work, atomic transaction failures, immutable operation fingerprints, three-history bounds, integrity |
| Worker/controller | One slot across manual/service/session/execution/verification, onboarding once, no idle inference, partial results/questions, retry/backoff/budget, offline report replay and no duplicate inference/review/merge; prepared review reply, claim-ack recovery, uncertain verifier failure, discussion/mode/workspace cancellation, disabled-binding preservation and manual resolution |
| Providers/processes | Exact required flags, text tool isolation, prompt through stdin, private/bounded output, version/model pinning, cancellation of owned descendants, failed signed staging retains previous version; actual login PATH/bare command versions, profile preservation/collisions, current-version dispatch and real foreground-terminal input; advertised Codex/Copilot device-flow selection, legacy fallback, bounded help-only probes, protected tmux/local TTY display and public-link versus credential handling; missing-authentication to real Founder/delegate inbox before onboarding without inference, private/protected notice fields, exact replay across restart/lost ack, explicit pending failures, 15-minute expiry/legacy deadlines, no automatic renewal or receipt revival, Founder-only typed refresh including paused/offline targets, scope/authority checks, no notification spam or auth inferred from receipt acknowledgement |
| Sessions | Request != consent != explicit open, current scope/expiry/revocation, no public listener, restart retirement, cleanup fences |
| Autonomous control | Only Founder pauses/resumes; target/delegate/offline overrides denied; event-driven cancellation and server claim gate; version-1 server migration preserves identity; legacy unexplained manual pause fails closed |
| Persistent assistance | Only Founder issues/revokes; exact requester/target/purpose/mode/provider/project; no-project authentication bootstrap; target prepares unattended without local consent; disconnect/restart/expiry retains grant; role/identity/revocation closes streams; delegate is not assumed human |
| Native identity/ingress | Recording account adapters/in-process SSH, unique UID/account/key reservations, positive per-account generation on typed root provision/disable; stale/same-generation-key-change rejection before OS calls, exact replay, never-provisioned disable tombstones, higher-generation fresh-key UID/name preservation and pre/returned/post-bind/current registry tuple checks; no arbitrary commands/PTY/forwarding, wrong-peer/pin/certificate rejection, private root maintenance outside agent gateway; no host users/sshd/services changed |
| Owner/current-user boundary | Device versus ephemeral owner certificate purposes, instance/agent/key/owner-generation agreement, kernel peer UID checks and owner-only IPC, same-user key ownership but no private/control export over ordinary IPC, one local owner across CLI traffic; confirmed distinct live owner proofs plus fresh incumbent binary ping ban original and contender including Founder, while stale crash/reconnect/unproved attempts do not; ban cancels API/streams, recovery keeps work fenced |
| Native services | Recording adapters by default, one existing installing user/HOME with independent agent name, both user systemd services/macOS LaunchAgents, unchanged HOME/groups/shell, no root/sudo/runuser/new account requirements, private 0700/0600 IPC, explicit legacy migration refusal and retained state, raw scalar systemd paths, user-bus environment only for registration |
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

## Concise public activity status

```sh
go test -race -count=1 ./internal/domain ./internal/foundation ./internal/api ./internal/cli ./internal/identityservice ./internal/work
go test -race -count=1 -tags integration ./tests/integration -run 'Test(PublicAgentActivityIsReadableThrottledAndPrivateByConstruction|AgentStatusRawAPICannotSpoofOtherAgentOrAuthority)$'
```

Deterministic checks cover generic safe controller wording/legacy activity,
local readable activity/no fake progress, critical wake suppression on unchanged
polls, self-only publication and public roster visibility,160-character/single-line
input and protected-field boundaries,60-second coalescing/last-wins/unchanged
publication timestamps, immediate meaningful block/pause/recovery, scoped authored
notes/clear/invalidation, server-clock freshness and generation/stale/restart
fences. Real local server/worker/proxy fixtures verify message context never leaks
into public status and no extra model call is made just for reporting. All tests
use disposable state and synthetic providers; no live deployment or native fleet
capacity claim is implied.

## Explicit message read-receipt regressions

```sh
go test -race -count=1 ./internal/foundation ./internal/cli ./internal/identityservice ./internal/work
go test -race -count=1 -tags integration ./tests/integration -run 'Test(ControllerMarksReadOnlyForExplicitRecipientDecision|ExplicitReadLostAckReplaysWithoutAnotherModelCall|MessageReadRawAPIEnforcesRecipientAndProtectedTimestamp)$'
```

Coverage distinguishes explicit recipient acknowledgement from inbox/show/
diagnostics/context selection, a reply or handled disposition. It checks
recipient-only raw API authority, no caller-provided/backdated timestamp,
immutable first server read_at across operations/replay, safe concurrent read and
disposition without revision invalidation, expired/protected control boundaries,
CLI peeks producing only GETs, strict explicit command input, actual Unix-proxy
method/depth/query/header forwarding, and explicit model choice versus omitted/
false. Lost read acknowledgements retain the prepared response and replay without
another inference; no legacy backfill or live-provider qualification is claimed.

## Tool-disabled response-contract regressions

```sh
go test -race -count=1 ./internal/local ./internal/work
go test -race -count=1 -tags integration ./tests/integration -run 'Test(RejectedScopeReplyDoesNotPoisonInboxOrRepeatInference|RejectedMessageDoesNotStarveAnotherRequestedReply)$'
```

These deterministic source tests cover explicit phase outcomes, `reviewed` being
invalid for scope/acceptance/preflight, preservation of raw private reply/prompt
references, atomic rejection/retry fencing and rollback, cached deployment reply
reconciliation, unchanged polls/restarts/new manual operation IDs causing no new
inference, unrelated requested messages continuing, and exact stopped-process
acknowledgement permitting a fresh tool-disabled review without task approval or
execution. Possibly applying/uncertain decisions remain fenced. Fixtures do not
certify live provider quality or substitute for native cleanup confirmation.

## Diagnostics CLI and guidance acceptance

Focused source-only checks:

```sh
go test -race -count=1 . ./internal/cli
```

Actually run on 2026-10-22 with Go 1.27.1 on darwin/arm64, in the version 0.2.6
working tree based on `1fec1419`: both packages passed. This is focused
CLI/guidance evidence only; it does not complete the server/worker feature's
coordinated acceptance.

`TestDiagnosticsCLIReadOnlyTypedRoutes` exercises both diagnostics endpoints
through the CLI's normal configuration/client path and a test-local Unix proxy,
preserving phase/deadline/progress, provider preference/model/authentication
timestamps, component problems and blocked/stale/unknown/handled observations.
It preserves observed lifecycle `state` separately from lower-level `stage`,
including generating/invoking and blocked/recipient, without inferring a receipt
or AI completion. Offline message help checks the same distinction.
`TestDiagnosticsCLIPreservesErrorsWithoutHealthyFallback` checks denied/not-found/
unavailable, incompatible API and malformed DTO responses with no retry/probe or
success fallback. `TestDiagnosticsCLIRequiresExistingEnrollment` rejects missing
configuration/enrollment, retired bearer and mismatched identity before diagnosis.
`TestDiagnosticsCLIRejectsUnexpectedArityBeforeRequests` rejects missing/extra IDs
and unsupported flags/input. `TestDiagnosticsCLIOfflineHelpAndDiscovery` and
`TestEveryCLILeafHasOfflineHelp` check executable discovery without enrollment.

The root guidance tests verify source/catalog hashes, skill frontmatter/links,
complete documented commands, both skill-discovery locations, preserved HOME
instructions/unrelated skills, tampering refusal, embedded native/Debian guide
content and bounded explicit prompt context. These are synthetic source fixtures,
not server watchdog/privacy acceptance, integration/e2e, full-suite, live Agent1,
provider-authentication, deployment or release qualification. No new e2e scenario
family is claimed by these CLI tests.

## Founder operation CLI, Unix-proxy and guidance acceptance

Focused source-only selectors for the diagnostic refresh and exact closed-session
repair additions:

```sh
go test -race -count=1 . ./internal/cli
go test -race -count=1 ./internal/identityservice \
  -run '^(TestWorkerProxy|TestPublicIdentityStatusAndFounderRevocationRemainOrdinaryRoutes|TestOrdinaryProxyRoutePolicyFailsClosed|TestKernelPeerCredentialsCannotBeOverriddenByHTTPIdentityHeaders)'
```

Actually run on 2026-10-22 with Go 1.27.1 on darwin/arm64 in the uncommitted
version 0.2.8 tree based on `692d2314`: root guidance, CLI and the selected
identity-service proxy race cases passed. Scratch/state stayed inside the
checkout. This is scoped source-fixture evidence, not coordinated full-suite or
live recovery/deployment acceptance.

`TestAgentOperationCLIRefreshUsesDurableWriteAndExactReplay` verifies the fixed
nonsecret diagnostic request, immutable local CLI proposal, same-byte lost-ack
replay and conflict on target rebinding. Repair fixtures read a private input,
replay exact bytes, reject changed fence under the same operation ID and use the
same common bodyless operation lookup. Cached diagnosis remains a saved
bodyless GET without a write proposal. Configured and PATH provider sentinels
fail if the CLI invokes a provider; these tests use no login or paid inference.
The `TestAgentOperationCLI` family checks all seven result states (including
queued/expired and unverified credentials without a freshness/usability fallback),
strict unsupported flags/arity, private/caller-proof JSON fields, typed replies
that do not echo private extras, incomplete/future outcome refusal, server error
preservation and offline help/discovery. Root guidance tests validate current
command coverage, skills/frontmatter, links/hashes, preservation/discovery and
explicit bounded prompt context. The narrow clock-field follow-up updates these
safe result fixtures to preserve projected `server_observed_at` and server-anchored
`captured_at` separately from target inspection/provider UTC observations ahead
or behind by one day; the expired fixture uses the server-clock deadline.
Caller repair input cannot provide either protected clock field. The clock-field
CLI/guidance and selected proxy race reruns passed on the same toolchain/platform.
These are serialization/preservation fixtures only. The coordinator separately
reported the paused/fenced proven/legacy recovery, blocked-inference and target
UTC +/-1-hour integration passing in 27.3s, plus local/remote/work race suites.
Those results exercise the server validation and conservative monotonic
request-start/round-trip capture bound; final coordinated all-suite acceptance
remains pending, with no live/native recovery or deployment qualification.

`TestWorkerProxyForwardsExactDiagnosticAndOperationRoutes` uses the actual
identity-service Unix listener, real kernel peer check and ordinary owner-client
path to forward exact diagnostic GET/POST, repair POST, operation list/detail GET
and result POST method/path/query/body. It strips forged caller identity/private
headers and upstream private metadata. Denial fixtures exercise wrong methods,
message-diagnostic writes, deeper paths, unsupported/duplicate queries, escaped
paths and identity/native controls without forwarding. The fake upstream owner/
cryptography fixture is not a native separate-UID/mTLS deployment qualification;
the CLI-only Unix fixture is not this private proxy regression.

These checks do not establish server authorization, native cleanup/recovery
mutation, dedicated worker-loop acceptance, live Agent1 recovery, vendor usability,
native service restart/deployment or full-suite/release qualification. The initial
feature intentionally leaves old unproven fences operator-required. Source-only
implementation and actual results must be reported separately from those limits.

## Worker health unit and integration acceptance

On 2026-10-22 the coordinator reported the implemented deterministic cases in
`tests/integration/health_work_test.go` passing. Exact runnable selectors:

```sh
go test -race -count=1 -tags=integration ./tests/integration \
  -run '^(TestWorkerHeartbeatPublishesLiveReplyWithoutWaitingForInference|TestSignedInWorkerExplainsSessionFenceWithoutRetryingOrClearingIt)$'
```

- `TestWorkerHeartbeatPublishesLiveReplyWithoutWaitingForInference` exercises an
  isolated real worker `Serve`/watch/heartbeat path while its deterministic
  provider invocation remains pending. Founder diagnostics expose `invoking`,
  the matching message action/deadline and an unfinished step; private message
  body/provider path stay redacted. Message diagnostics report lifecycle
  `state:"generating"`, not completion. Releasing the fixture invocation records
  progress and a handled receipt without a second invocation.
- `TestSignedInWorkerExplainsSessionFenceWithoutRetryingOrClearingIt` exercises
  a signed-in worker step with a retained session fence, publishes its heartbeat
  and reads Founder/message diagnostics. The fence remains visible and intact,
  the reply is blocked without inference/retry, and an information-only message
  remains `not-requested`. Sign-in or diagnosis does not clear recovery evidence.

The coordinator also reported the implemented `TestWorkerHealth` unit family in
`internal/work/health_test.go` passing. Reproduce it with:

```sh
go test -race -count=1 ./internal/work -run '^TestWorkerHealth'
```

That family covers in-flight phase snapshots, idle polls not counting as
progress, timestamped/redacted/resolved component errors, sign-in preserving
Founder pause/uncertain invocation fences, stable repeated-blocker timing,
successful reply progress, safe provider metadata/old-fence observations and
unsupported local formats failing without overwriting retained evidence.

These are coordinator-reported deterministic source unit/integration results,
separate from the CLI/guidance checks above. The fixtures use no real credentials
or paid inference. They do not qualify e2e, live Agent1/vendor authentication,
native-service/multi-UID deployment, control-plane outage detection, the full
suite or a release.

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
- `internal/identityservice`: public-only preparation/profile retry, same-user
  key ownership, owner-only socket/peer checks, one owner across CLI traffic, fresh binary ping,
  proactive device renewal with the same per-start ephemeral owner, owner
  certificate rotation and expiry/denial stream cancellation without fallback.
- `internal/platform`: recording Foundation account provisioning/retirement,
  same-existing-user identity/runner plans and paired lifecycle
  preflight/status/install. Cover actual user defaults, independent agent names,
  unchanged HOME/groups, preexisting private configurations/socket bindings,
  fixed user native paths/arguments, explicit apply/current-user/OS guards,
  no privileged switching and interrupted registration without host changes.
- `cmd/agent-identity`: offline service plan/help without `--config`/enrollment,
  explicit install apply, native status and rejected lifecycle/private-command
  flag mixing. `internal/identityservice` recovery fixtures preserve old private
  state and all runner state while fresh preparation uses an empty sub-StateDir
  in the same existing user's HOME; normal retry never rotates keys.
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
