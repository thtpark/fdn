# Delegated pull-request scenarios

## Default: entirely local

From the checkout root:

```sh
go test -count=1 -tags=e2e ./tests/e2e \
  -run '^TestNativeDelegatedPullRequestWorkflow$' -v
go test -race -count=1 ./tests/scenario ./tests/fixtureprovider
```

The native helpers allocate and clean short owner-only fixture roots in the test
package directory; no `TMPDIR` override or caller-created scratch root is needed.

The business-workflow harness uses compiled native worker binaries with an
**explicit test-only** `api.NewAuthenticated` authenticator, direct Store setup
and a test-local Unix worker proxy. Founder, non-delegate assignee and delegate
have separate HOME/state but run under the test OS UID. Each real worker's
identity and tools intentionally share its existing installing user. This harness
proves business policy/controller behavior, **not** Foundation-host OS account isolation, the production SSH
account-to-certificate boundary or physical computer uniqueness. It is not a
legacy bearer/TCP enrollment path available in the runtime. Actual Unix/mTLS
security regressions are cataloged separately below; they also do not create
real host accounts.

Codex/Copilot roles are swapped in table cases. Founder arranges **mock** sign-in;
scripted executables, not real providers, perform four actual-answer clarification
rounds and tool-disabled preparation. Exact Founder signoff precedes real Git edits.
An isolated bare origin and authenticated loopback GitHub API model
`https://github.com/thtpark/openai-python` with tiny original Python files, not an
upstream checkout. No default network route, credential helper, live provider,
host-service registration, Python runtime, or paid inference is used.

The delegate daemon uses its own provider/model/private checkout, requests a
negative-attempt correction, verifies the changed head on the **same** PR, then
merges. Assertions bind actual content/diff, comment authors/mentions, run kinds,
spec/result/discussion/project fences, merge order, privacy, idle, and restart.
The Python test-file contents and Git whitespace checks are verified; this is
not a claim that upstream Python tests or a live model were executed.

Private `HOME/.fdn-fixture/plan.json` files select generic phase/context rules,
relative-file actions, local Git, literal-loopback HTTP, assertions, and templated
structured replies. Actual requested replies supply answer IDs and text. No
production task-title switch is needed. Plans/actions/outputs/traces and HTTP
requests are bounded; files are owner-only. Missing plans retain existing fixture
behavior. Every server and native process is owned and cleaned by the test.

## Test-case catalog

These are implemented cases for the delegated-workflow scenario and its direct
regressions, not a complete inventory of all Foundation tests. IDs identify
contracts rather than test counts: a row may have several table-driven variants,
and each end-to-end checkpoint runs inside the same native scenario.

### RESET-001: Founder-only identity-preserving runtime reset

**Automation:** `TestNativeFounderOnlyRuntimeResetIncludingSelf` in
[runtime_reset_test.go](runtime_reset_test.go). Run with
`go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeFounderOnlyRuntimeResetIncludingSelf$'`.
Founder queues an exact reason-only reset for a paused worker and itself,
inspects the actual protected `reset` outcome, replays the original request and
verifies non-Founder denial, unchanged identity/configuration/manual mode and
no provider invocation journals. Tests own all local binaries/processes/state.
Unknown legacy cleanup, uncertain-work retention and actual owned-session
cancellation are separately exercised by the unit/loopback integration cases in
[TEST_PLAN.md](../../TEST_PLAN.md#founder-only-runtime-reset), not certified as
live native-service or production recovery.

### RESET-002: Explicit reset cleans the recognized legacy session

**Automation:** `TestNativeFounderResetClearsLegacySession` in
[runtime_reset_test.go](runtime_reset_test.go). Run with
`go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeFounderResetClearsLegacySession$'`.
The fixture creates a terminal server session, real orphaned old-format Unix
socket and unproven legacy fence, then calls the compiled Founder reset CLI
against the running native worker. Expect final `reset`, actual inactive fence,
original socket inode retained privately, unchanged manual mode, no inference
and exact replay leaving a newer fence untouched. Kernel process/endpoint and
unsafe-resource refusal checks run separately in native unit tests; this is not
host-service or live-provider qualification.

### WF-001: Founder to delegate-reviewed merge

**Automation:** `TestNativeDelegatedPullRequestWorkflow` in
[delegate_workflow_test.go](delegate_workflow_test.go), with result/Git assertions
in [delegate_workflow_assertions_test.go](delegate_workflow_assertions_test.go)
and provider plans in
[delegate_workflow_helpers_test.go](delegate_workflow_helpers_test.go).
Run it with the native command above.

**Preconditions:** a fresh explicitly test-authenticated Foundation TLS business
fixture; distinct Founder, assignee and delegate application identities and
HOME/state under one OS UID, not native account provisioning; an active project;
explicit local workspace-tool grants; deterministic provider executables;
and an isolated bare Git origin plus authenticated loopback Git-host fixture.
The assignee is not a project delegate. Founder approves the initial execution;
the current independent delegate may approve only in-scope rework.

| Native subtest | Non-delegate assignee | Independent delegate |
| --- | --- | --- |
| `codex-worker` | Codex, task-required worker model | Copilot, delegate's own preferred review model |
| `copilot-worker` | Copilot, task-required worker model | Codex, delegate's own preferred review model |

| Checkpoint / action | Expected result and observable evidence |
| --- | --- |
| Founder creates a project/task, grants delegation, assigns the non-delegate worker and sets `todo` | The task retains the actual Founder creator, worker assignment, opaque worker workspace and independent approval policy. Metadata setup does not authorize execution. |
| Both signed-out workers start; Founder arranges mock sign-in | Founder receives protected authentication notices from both identities. No invocation journal/provider inference exists before sign-in. Each identity's own provider becomes fixture-ready; no protected remote session is opened. |
| Worker asks for the file/API scope, delay parameters, edge behavior and PR/review authority | Four productive questions literally mention Founder and record the worker's question IDs/requested respondent. An unanswered question does not trigger repeated inference or coding. Four is the fixture's scenario bound, not a production question quota. |
| Founder replies to each question | Each actual requested answer ID is retained and its text is incorporated exactly once into description and acceptance criteria. The specification advances, but replies never create readiness or execution. |
| Founder reviews the refined specification and approves | Stale-generation approval fails. Before current approval, the workspace is unchanged and no coding/PR exists. Current readiness names Founder and the exact specification. |
| Worker implements the agreed change and creates a PR | Real Git edits touch only `src/openai/_retry.py` and `tests/test_retry.py`; Git whitespace checks succeed. Exactly one PR targets `main` at the worker's actual head, attributed to its required provider/model. `completed` is not `done`. |
| Delegate independently reviews the first head and requests a negative-attempt correction | Review uses the delegate's own provider/model and private checkout. One actionable delegate-owned task comment mentions the assignee. Rework advances the specification and records fresh delegate readiness without changing the approved description, criteria, assignment or worker workspace. |
| Worker preflights the review and fixes the existing PR | Current discussion-bound clarity precedes execution. The correction and regression assertion produce a second commit/head on the same PR, not a second PR. Consumed review feedback produces no redundant attention echo. |
| Delegate reviews the corrected head and merges | Corrected-head approval precedes an exact-head merge. Actual merged `main` contains the reviewed code/test bytes and the correct merge parents. Only the exact authorized verification report produces `done`, result generation 2 and current delegate verification. |
| Both controllers idle and restart | Exactly two execution and two verification runs remain, with actor/provider/model/spec attribution. Each execution is attempt 1 of its own specification. No provider call, PR, review or merge repeats; reports are acknowledged. |
| Inspect shared records and private journals throughout | Credentials, private notes and paths do not leak into task/project/discussion/run/history/watch output. Each provider receives only its own selected private knowledge; invocation context contains no credentials. |

The required Git-host event order is `created`, `review-rework`, `head-updated`,
`review-accept`, `merged`. The test fails if automatic delegate control does not
finish; it does not substitute a manual `verify` to make the scenario pass.
Python file contents and Git checks are evidence here, not executed upstream
Python tests or live-model quality.

### WF-002: feedback consumption without lost ordinary replies

**Automation:** `TestDelegateControllerReviewsReworkAndAcceptsCurrentResult` in
[verification_test.go](../integration/verification_test.go).

| Feedback variant | Action and expected result |
| --- | --- |
| `pending` | Preflight consumes the exact typed review-feedback receipt. A separate ordinary requested mention remains pending; no echo invalidates final review. |
| `deferred` | An explicit recipient deferral is respected. Rework can proceed without overriding that disposition or consuming the separate ordinary mention. |
| `lost-ack` | Lose the feedback-receipt response after the server commits. Replay the same durable operation without another semantic review, then execute and accept the revised result once. |

### Verification policy and recovery cases

Source **V** is [verification_test.go](../integration/verification_test.go).
Source **R** is
[verification_recovery_test.go](../integration/verification_recovery_test.go).
These tests use the real loopback TLS API, isolated SQLite/local state and
synthetic executors; they do not perform live GitHub writes.

| ID | Source / automated test | Action and expected result |
| --- | --- | --- |
| `VF-001` | V: `TestVerificationClaimOwnershipPolicyAndExactFences` | Reject non-delegate claims, mismatched results and another actor's reports. Exact claims/reports replay once; completed evidence and serial ownership survive until the current verifier reports. Manual acceptance cannot bypass an active review. |
| `VF-002` | V: `TestVerificationClaimRacesShareOneSlot` | Race 12 claims for the completed result; exactly one gains ownership. |
| `VF-003` | V: `TestVerificationChangedDiscussionOrAuthorityCannotAccept` | Change discussion, delegation, specification, approval withdrawal or mode during review; the old report becomes stale, releases its ownership and never produces done. |
| `VF-004` | V: `TestVerificationLostAcknowledgementDoesNotReinvokeTools` | Lose the accepted final report's response, restart the controller and flush its outbox; acceptance survives without a second review/merge invocation. |
| `VF-005` | R: `TestVerificationAndExecutionShareServerSlot` | Hold a verification claim, then claim another execution as that computer; reject the second ownership. |
| `VF-006` | R: `TestVerificationPoliciesAndFounderPauseAreServerEnforced` | Exercise named actors, self-review, Founder pause and a stale project epoch through raw claims; no client path bypasses the relevant policy. |
| `VF-007` | R: `TestExecutionReportCannotMakeVerificationDecision` | Submit acceptance fields on an execution report; reject self-acceptance rather than completing verification. |
| `VF-008` | R: `TestVerificationRejectsMalformedDecisionsWithoutReleasingOwnership` | Omit decision/evidence or supply safe-retry, assignee-blocker or unrelated-approval fields; reject transactionally without releasing the active review or changing its completed evidence. |
| `VF-009` | R: `TestVerificationUncertainReportsBlockAutomaticRetry` | Report failed, paused, interrupted or stopped verification; retain completed evidence with a verification blocker, reject automatic retry, and allow explicit authorized acceptance after inspection. |
| `VF-010` | R: `TestVerificationCannotApproveReworkForRevokedAssignee` | Revoke the assignee before or after the review claim; do not issue fresh rework readiness to that revoked identity. |
| `VF-011` | R: `TestRevokingVerifierStopsOwnershipUntilExplicitRelease` | Revoke the reviewer; request stop and deny its report while retaining the process fence until explicit stopped-process release. |
| `VF-012` | R: `TestVerificationPauseAndResumeStillInvalidatesPriorClaim` | Pause and resume the reviewer before reporting; the intervening mode revision still invalidates its earlier claim. |
| `VF-013` | R: `TestVerificationReleasePreservesResultAndDoesNotChargeWorkerRetry` | Explicitly release a stopped review; preserve the worker's result/failure budget and require inspection before further verification. |
| `VF-014` | R: `TestVerificationPreparedReplyAndInterruptedRecovery` | Recover reply-prepared, claim-prepared and invoking journals. Replay a prepared decision without inference; interrupted/unstarted review recovery retains evidence and blocks blind tool retry. |
| `VF-015` | R: `TestVerificationLostReworkAcknowledgementReplaysExactApprovalAndComment` | Replay a rework report; retain one spec advance, one feedback comment and one exact fresh delegate approval. |
| `VF-016` | R: `TestVerificationCancellationAndFailureNeverRerunTools` | Fail the provider or change discussion, mode or workspace permission while it runs; cancel/record the outcome once, retain a verification blocker and never automatically rerun tools. |
| `VF-017` | R: `TestReviewFeedbackProvenanceCannotBeForgedByOrdinaryComments` | Supply protected review provenance through an ordinary comment write; reject it as invalid input. |
| `VF-018` | R: `TestVerificationCannotBypassDisabledLocalToolBinding` | Disable the delegate's binding; launch no review tools and do not create/re-enable a replacement binding to bypass the decision. |

Run this regression family from the checkout root:

```sh
go test -race -count=1 -tags=integration ./tests/integration \
  -run '^(TestDelegateController|TestVerification|TestExecutionReportCannot|TestRevokingVerifier|TestReviewFeedback)'
```

### Policy, provider and fixture cases

Source **D** is [approval_test.go](../../internal/domain/approval_test.go),
**P** is [verification_test.go](../../internal/provider/verification_test.go),
**S** is [script_test.go](../scenario/script_test.go), and
**G** is [github_test.go](../scenario/github_test.go).

| ID | Source / automated test | Expected contract |
| --- | --- | --- |
| `POL-001` | D: `TestEligibleApproverUsesCurrentRoleAndNamedIndependentPolicy` | Founder/delegate role remains necessary; a named allowlist grants no authority, readiness/verification independence are distinct, and inactive actors/archived projects cannot approve. |
| `POL-002` | P: `TestVerificationDecisionRequiresReviewedOutcome` | Only a reviewed reply may propose accept/rework; other phases and unknown decisions cannot self-accept. |
| `FX-001` | S: `TestScriptRefinesOnlyActualRequestedRepliesWithoutTools` | Refine only actual replies from the requested actors/questions, preserve their answer IDs and keep preparation tool-disabled. |
| `FX-002` | S: `TestScriptRejectsToolActionsDuringTextCalls` | Reject a tool action in a text phase even when selected by a fixture rule. |
| `FX-003` | S: `TestScriptRejectsAnotherIdentityProviderModelAndToolPolicy` | Reject actor/provider/model or coding-flag mismatches instead of silently choosing another identity or adapter. |
| `FX-004` | S: `TestScriptHTTPNeverUsesLiveHostsOrRedirects` | Reject live/nonliteral-loopback hosts and redirects; do not send fixture authentication to another host. |
| `FX-005` | S: `TestLocalGitRejectsHTTPTransportAndFileTraversal` | Deny network Git/config overrides and relative-file traversal into paths outside the owned workspace or into Git metadata. |
| `FX-006` | S: `TestMissingPlanPreservesLegacyFixtureSelection` | Absence of a scripted plan leaves the existing deterministic provider fixture behavior selected. |
| `FX-007` | G: `TestGitHubRequiresIndependentCurrentHeadReviewBeforeMerge` | Require a distinct reviewer and corrected/current-head approval before merge; assert the actual Git result rather than trusting an API-shaped success. |

The fixture command at the start of this file runs `FX-*`. Run the two focused
policy/provider tests with:

```sh
go test -race -count=1 ./internal/domain ./internal/provider \
  -run '^(TestEligibleApproverUsesCurrentRoleAndNamedIndependentPolicy|TestVerificationDecisionRequiresReviewedOutcome)$'
```

## AUTH: expiring sign-in requests and explicit Founder refresh

These cases use synthetic provider health and isolated local state. They do not
perform provider login, expose device codes, open credential UI or certify live
vendor authentication. The business fixtures prove server authorization and
controller durability, not native multi-UID isolation.

| ID | Automated test | Expected result |
| --- | --- | --- |
| `AUTH-007` | `TestAuthenticationRefreshAPIFounderOnlyAndPrivate`, `TestAuthenticationMessageAPIRejectsForgedControlsAndExpiry`, `TestAuthenticationRefreshAPIRejectsProtectedRequestFields` and `TestAuthenticationRefreshAPIRequiresBoundTLS` in [API tests](../../internal/api/authentication_test.go) | Raw API enforces Founder/recipient identity, protected DTO ownership, bounded input and bound TLS. |
| `AUTH-008` | `TestAuthenticationOperationsReplayAfterExpiry`, `TestAuthenticationLegacySendReplayDoesNotRenewDeadline`, `TestAuthenticationExpiryProjectsLegacyWithoutMutatingEvidence`, `TestAuthenticationReceiptsCannotReviveExpiredHandoffs` and `TestAuthenticationInboxFiltersBeforePagination` in [server tests](../../internal/foundation/authentication_test.go) | Server-clock boundary, immutable legacy/exact replay, receipt nonrevival and correct filtered pagination. |
| `AUTH-001` | `TestAuthenticationExpiryProjectsLegacyRequestsWithoutRenewal` in [worker tests](../../internal/work/authentication_test.go) | Exact 900-second boundary; legacy original timestamp, pending delivery expiry, no automatic renewal, real resolution retained. |
| `AUTH-002` | `TestAuthenticationExpiryWaitsForFounderAndSupersedesExactlyOnce` in [integration tests](../integration/authentication_expiry_test.go) | Local/message deadlines agree; repeated expiry polls stay blocked; explicit Founder refresh supersedes once and survives restart. |
| `AUTH-003` | `TestFounderSignInRefreshWorksWhilePausedWithoutResumingOrInference` in [integration tests](../integration/authentication_expiry_test.go) | Target checks requested provider in manual mode; explicit missing auth alone generates a notice; healthy/unknown status does not; no inference/session/resume. |
| `AUTH-004` | `TestFounderAuthenticationRefreshDeliveryLostAckReusesPreparedEpisode` and `TestFounderAuthenticationRefreshReceiptLostAckDoesNotRepeatNotification` in [integration tests](../integration/authentication_expiry_test.go) | Unavailable/lost-ack delivery and receipt recovery preserve one episode, deadline and message operation across restart. |
| `AUTH-005` | `TestLegacySignInInstructionsRequireFounderRefreshWithoutRewritingOldOperations` in [legacy test](../integration/autonomy_notice_test.go) | Upgrade alone expires old episodes instead of notifying again; Founder explicitly supersedes without rewriting historical messages. |
| `AUTH-006` | `TestNativeDaemonRequestsSignInBeforeOnboardingWithoutInference` in [native tests](authentication_test.go) | Both providers: compiled CLI Founder refresh queues while offline, exact replay retains one control, non-Founder is denied, restart produces one fresh protected notice without model invocation. |

```sh
go test -race -count=1 ./internal/domain ./internal/foundation ./internal/api ./internal/work ./internal/identityservice -run 'Authentication|OrdinaryRoutes'
go test -race -count=1 -tags=integration ./tests/integration -run 'Authentication|SignIn|LegacySignIn'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeDaemonRequestsSignInBeforeOnboardingWithoutInference$'
```

## HELP: requested work blocked by sign-in or permissions

Fresh requested actions can request help despite an expired older notice. Stable
poll/restart retries do not renew an unchanged action, start login/inference,
grant permissions, change mode or bypass an uncertain invocation/run fence.

| ID | Automated test | Expected result |
| --- | --- | --- |
| `HELP-001` | `TestRequestedTaskEscalatesBeforeOnboardingDespiteExpiredNotice` in [integration tests](../integration/requested_help_test.go) | A todo assignment reaches Founder and current delegate before onboarding; distinct same-project tasks do not suppress or repeatedly supersede each other. |
| `HELP-002` | `TestRequestedMessageAndExplicitWorkRearmSignInWithoutIdleSpam` and `TestRequestedTaskHelpLostAcknowledgementReplaysSameScopedNotice` in [integration tests](../integration/requested_help_test.go) | A requested reply/new explicit operation asks again; exact retry, restart and lost acknowledgement reuse one action/episode/message. |
| `HELP-003` | `TestRequestedPermissionBlockEscalatesWithoutGrantingWorkspaceAccess` in [integration tests](../integration/requested_help_test.go) | Disabled workspace help reaches both authorities exactly once; no tools are enabled and no model runs. |
| `HELP-004` | `TestProviderSignOutDuringWorkEscalatesWithoutBypassingRecovery` in [integration tests](../integration/requested_help_test.go) | Fresh readiness after text/coding failure detects rejected credentials and notifies both authorities; uncertainty/failed-report/retry fences remain. |
| `HELP-005` | `TestNativeRequestedTaskEscalatesSignInBeforeOnboarding` in [native tests](requested_help_test.go) | Both compiled provider-role fixtures publish one task/project-scoped notice before onboarding and preserve it across restart without inference. |

```sh
go test -race -count=1 -tags=integration ./tests/integration -run 'Requested|ProviderSignOut'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeRequestedTaskEscalatesSignInBeforeOnboarding$'
```

These are isolated fixture results, not live provider sign-in, new VM deployment
or permission approval. Missing authentication can prevent semantic questions;
help delivery is deterministic and does not need a model to ask for access.

## LOGIN: supported device flows and authorized human display

Only synthetic provider help/screens are used; no real login, device code,
credential file, provider inference or model-visible credential UI is exercised.

| ID | Automated test | Expected result |
| --- | --- | --- |
| `LOGIN-001` | `TestLoginCommandPrefersOnlyAdvertisedDeviceFlows` and `TestLoginCommandProbeFailureNeverStartsLogin` in [provider tests](../../internal/provider/login_test.go) | Prefer Codex `--device-auth`/Copilot `--device-code` only when login help advertises the exact flag; retain legacy native login otherwise; preserve no-auto-update; failed/cancelled help starts no login. |
| `LOGIN-002` | `TestNativeAuthenticationPrefersAdvertisedDeviceFlow` in [remote tests](../../internal/remote/native_login_test.go) | The real tmux opener passes the same safe device-flow arguments to a synthetic terminal adapter; no arbitrary flags or copied credentials. |
| `LOGIN-003` | `TestNativeProviderLoginShowsSupportedDeviceFlowOnHumanTTY` in [native tests](provider_login_test.go) | Both compiled provider-login paths display a synthetic sign-in screen/public link to the actual human TTY using supported device flags. |

```sh
go test -race -count=1 ./internal/provider ./internal/remote ./internal/work -run 'Login|NativeAuthentication|AuthenticationNotice'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeProviderLoginShowsSupportedDeviceFlowOnHumanTTY$'
```

Showing provider sign-in screens/public verification URLs to the authorized
human is a normal consented authentication step, not intrinsically a privacy
violation. This does not authorize publishing credentials, one-time codes,
secret-bearing URLs or unredacted credential transcripts to models/shared logs.

## VNC: display-only, screen-bound browser assistance

These source fixtures use synthetic display metadata/RFB streams and original
small noVNC module fixtures; they never capture a real screen or login. Native OS
desktop/backend, trusted installed noVNC and actual app placement are separate
per-equipped-host qualification. Headless targets must not acquire a desktop.

| ID | Automated test | Expected result |
| --- | --- | --- |
| `VNC-001` | `TestInventoryRequiresActualDisplayAndRFBWithoutCreatingAnything`, `TestInventoryReportsOnlyVerifiedGeometryAndPublicMetadata`, `TestInventoryRejectsNonRFBAndUnavailableBackends` in [display tests](../../internal/display/display_test.go) | Configuration/cloud flags alone do not fabricate readiness; only owned native geometry plus verified loopback RFB is published, no paths/UI/credentials. |
| `VNC-002` | `TestOpenBindsChosenScreenApplicationAndFallbackExactly`, `TestLegacyDesktopNeedsOwnDisplayEvidenceAndExplicitScope` in [display tests](../../internal/display/display_test.go) | Selected screen/app cannot be substituted or widened; whole-desktop scope/fallback is explicit, not claimed isolation. |
| `VNC-003` | `TestScreenScopedHelpAndSessionsKeepAgentIdentityAndGrantBoundaries`, `TestDesktopFallbackIsNeverImplicitlyExpandedFromAScreenGrant` in [TLS integration tests](../integration/screens_test.go) | Target help reaches Founder/current delegates without opening access; self-only capability, exact replay, screen/app grants, headless denial and fallback policy hold. |
| `VNC-004` | `TestBrowserRejectsNonLoopbackListen`, `TestBrowserRejectsUnsafeAssets`, `TestBrowserBootstrapAuthorizationHeadersAndPaths`, `TestBrowserWebSocketAuthorizationAndSingleViewer` in [browser tests](../../internal/remote/browser_test.go) | Only trusted installed assets and authenticated loopback browser connection, strict Host/Origin/path and no-cache/CSP policies; no public listener/clipboard. |
| `VNC-005` | `TestBrowserCredentialFormCannotSubmitSecretsToHTTP`, `TestBrowserCredentialFormSyntheticJavaScriptBehavior` in [browser tests](../../internal/remote/browser_test.go) | Human noVNC credential form sends only to RFB, clears inputs and never uses HTTP endpoints/URLs/logs/storage or model output. |
| `VNC-006` | `TestBrowserRevocationAndTransportLossStopAttachment`, `TestBrowserContextCancellationAndExpiryCloseSockets`, `TestBrowserBoundsHTTPRequestsAndFailedHumanDiagnostics` in [browser tests](../../internal/remote/browser_test.go) | Revocation/expiry/loss/failed human output stops the viewer/relay and bounded resources; no quiet success after identity denial. |
| `VNC-007` | `TestOpenCloseAndCancellationStopOwnedApplication`, `TestFailedPlacementCleansOnlyOwnedProcessAndProvesStartupFailure` in [display tests](../../internal/display/display_test.go), plus `TestNoProcessVNCStartupFailureRetiresOnlyOwnedFence`, `TestUnknownStartupAndCleanupFailuresRemainFenced` in [manager tests](../../internal/remote/screens_test.go) | App placement/ownership is proved; expiry joins only owned helpers; no-process failures retire safely, unknown cleanup and unrelated fences remain. |
| `VNC-008` | `TestManualManagerPublishesHeadlessInventoryWithoutModeOrRevisionWrites`, `TestScreenPublicationLostAcknowledgementReusesDurableProposal`, `TestManagerReloadsActualProfilesFromLoadedSourceWithoutRestart` in [manager tests](../../internal/remote/screens_test.go), plus `TestHeadlessScreenCLIRequiresNoEnrollmentAndDoesNotInventDisplay` in [CLI tests](../../internal/cli/screens_test.go) | Capability publication works while manual without inference/claim-revision churn, exact uncertain retry, live profile reload and headless offline CLI. |

```sh
go test -race -count=1 ./internal/config ./internal/display ./internal/remote ./internal/domain ./internal/foundation ./internal/api ./internal/cli
go test -race -count=1 -tags=integration ./tests/integration -run 'ScreenScoped|DesktopFallback|ConsentRelay'
```

## Security boundary cases: source suites verified, final acceptance pending

The following cases are linked to tests in
[identity_test.go](../integration/identity_test.go), using
[identity_fixture_test.go](../integration/identity_fixture_test.go). On
2026-10-21 the coordinator confirmed all ten strict Unix/mTLS cases and both
compiled attachment cases in full integration (**25.673s**) and e2e (**94.630s**).
The enhanced `TestUnix` both-controller PolicyViolation ban notification and
invalid/replayed-proof no-ban regression also passed (**8.432s**). These are
source-working-tree fixture results, not a final all-suite acceptance of later
restore/lifecycle/account-generation replay hardening or live native qualification.
Run separately from the
business harness:

```sh
go test -race -count=1 -tags=integration ./tests/integration -run '^TestUnix'
```

These fixtures use strict `api.New`, real Unix listeners/`ConnContext`, TLS/mTLS,
signed public requests and WebSockets. One identity binds to the **actual kernel
UID** of the unprivileged test process; other logical account bindings are
negative policy fixtures, not provisioned OS users. Separate HOME directories
and injected business authentication do not substitute for this boundary.

| ID | Existing automated test | Verified source-fixture contract; final all-suite pending |
| --- | --- | --- |
| `SEC-001` | `TestUnixIdentityBoundaryRejectsSpoofedCredentials` | Actual Unix peer UID and certificate bind the principal; headers/valid legacy bearer cannot replace it. Reject missing/foreign/wrong-purpose/expired or mismatched certificates, wrong instance/agent/key/owner generation, TLS without Unix peer and retired enrollment routes. |
| `SEC-002` | `TestUnixIdentityActivationRequiresFreshProof` | Activation binds fresh one-time nonce, instance/agent/key generation and proof; exact operation replay retains identity, but replaced/reused/tampered challenges do not activate or authorize ordinary API with a device certificate. |
| `SEC-003` | `TestUnixIdentityRegistrationRequiresFounderSignedKeys` | Founder alone registers valid signed public keys with exact replay; project delegation, bad proofs, altered fields or submitted UID cannot confer registration authority. |
| `SEC-004` | `TestUnixOwnerLeaseRejectsUnprovenDuplicates` | Wrong proof, replayed owner nonce and unverified device cannot obtain ownership or ban/change the live original. |
| `SEC-005` | `TestUnixOwnerLeaseRequiresFreshRenewal` | Valid renewal rotates challenge while retaining owner generation; replay closes that owner lease without falsely banning the identity. |
| `SEC-006` | `TestUnixOwnerReconnectAndCrashDoNotBan` | Same-key retry, closed/unresponsive owner and server restart do not ban; replacement owners fence old certificates as appropriate. |
| `SEC-007` | `TestUnixConfirmedDuplicateBansOriginalWorker` | Different proved owner key plus fresh responsive incumbent ping bans the entire non-Founder identity; both owners lose ordinary API and streams, not merely the newcomer. |
| `SEC-008` | `TestUnixRevokedCertificatesRequireFreshKeyRecovery` | Revoked/retired credentials cannot recover themselves; explicit fresh-key recovery retains AgentID, requires reprovisioning the same reserved UID and fences old certificates/owners. |
| `SEC-009` | `TestUnixOwnerEquivalentPublicKeyEncodingDoesNotBan` | Whitespace/CRLF encoding of the same cryptographic owner key reconnects canonically without a false different-owner ban. |
| `SEC-010` | `TestUnixOwnerLeaseEndsWhenDeviceCertificateExpires` | Device-certificate expiry closes its owner WebSocket and denies owner API access without falsely banning the identity. |
| `SEC-011` | `TestUnixFounderMultipleConnectionsAndFreshKeyRecovery` | Multiple Founder owners remain live together; closing/renewing one does not retire the others, while whole-Founder fresh-key recovery still denies every old certificate. |

`TestFounderDeviceBinaryOwnersRenewAndRevokeIndependently` in
[device API tests](../../internal/api/devices_test.go) exercises real TLS/protobuf
and per-device proofs for multiple independently keyed Founder computers with
logical UIDs. It verifies concurrent renewal and scoped revocation, not native
multi-UID SSH accounts. `TestNativeFounderDeviceEnrollmentListAndRevocation` in
[compiled device tests](devices_test.go) verifies exact CLI replay, private
Founder-only device listing/approval/revocation and worker denial through the
explicit business proxy. Both retain one actor; the Foundation unit claim race
additionally asserts exactly one accepted work slot across Founder devices.

Compiled-worker attachment regressions in
[identity_attachment_test.go](identity_attachment_test.go) were confirmed in the
full e2e source run above; **final all-suite acceptance remains pending**. They
use the explicit test-local business proxy, not real identity-service/runner
OS isolation:

| ID | Existing automated test | Verified source-fixture contract; final all-suite pending |
| --- | --- | --- |
| `ATT-001` | `TestNativeWorkerUsesOnlyLocalIdentityAndPreservesMigrationState` | Repeated connect retains only owner-restricted public AgentID, ignores unusable remote settings, requires explicit migration, preserves local checkpoint state, rejects another actor and fails closed without the local proxy. |
| `ATT-002` | `TestNativeRetiredBearerEnrollmentFlagsDoNotReturn` | Reject retired invitation/name connect flags; no invitation output file or identity change results. |

```sh
go test -count=1 -tags=e2e ./tests/e2e \
  -run '^(TestNativeWorkerUsesOnlyLocalIdentityAndPreservesMigrationState|TestNativeRetiredBearerEnrollmentFlagsDoNotReturn)$'
```

Paired native lifecycle/recovery regression sources also exist. The source
owner reported full race tests for platform, identity CLI/service and API on
2026-10-21 **before the latest account-generation replay hardening**. A subsequent
full native-source race rerun passed ingress/platform/CLI (2.594s/2.958s/3.836s)
and vet, including the new generation, actual Unix RPC replay and post-bind
revocation races. **Final coordinating all-suite acceptance remains pending.**
These use recording native account/manager adapters, never live host installation:

| ID | Existing automated test / source | Expected fixture contract; acceptance pending |
| --- | --- | --- |
| `NAT-001` | `TestIdentityNativeInstallUsesOnlyExistingUserManager`, `TestIdentityNativeInstallUsesOnlyExistingMacOSUserGUI` in [identity_service_install_test.go](../../internal/platform/identity_service_install_test.go) | Both Linux services use the existing user's manager; both macOS processes use that user's GUI LaunchAgents, with fixed paths/minimal environment and no root, sudo, runuser, system daemon or account creation. |
| `NAT-002` | `TestIdentityNativeInstallRejectsAnotherUserWithoutAccountMutation`, `TestIdentityNativeInstallApplyAndPrerequisiteFailuresRemainObservable` in the same source; `TestCurrentUserServiceFilesPreserveExistingHomeAndLegacyEvidence` in [identity_service_current_user_test.go](../../internal/platform/identity_service_current_user_test.go) | Actual current UID/GID/HOME and explicit apply; independent local/agent names, unchanged existing HOME/groups, user-owned binaries and retained keys/journals. Missing manager or legacy shared IPC fails before native mutation; no account creation or automatic state transfer. |
| `NAT-003` | `TestStoppedRecoveryUsesNewPrivateStateWithoutDeletingOldKeysOrRunnerJournal` in [recovery_test.go](../../internal/identityservice/recovery_test.go), plus `TestIdentityNativeConfigurationBindingAndStoppedRecoveryPreserveRunnerState` in the native lifecycle source | New empty StateDir within the same existing user's HOME yields fresh prepared keys while retaining old keys and all runner evidence, stable private config/IPC and native HOME/workdir; ordinary preparation retries do not rotate keys. |
| `NAT-004` | `TestIdentityServiceLifecycleCLIPlansWithoutPrivateConfigAndAppliesOnlyExplicitly`, `TestIdentityServiceLifecycleCLIRejectsUnknownOptionsFlagsAndUIDBypasses` in [service_test.go](../../cmd/agent-identity/service_test.go) | Offline service plan without private --config/enrollment, explicit install --apply, strict lifecycle options and no root/UID bypass or flag mixing. |
| `NAT-005` | `TestManagedAccountGenerationFencesProvisionDisableAndRecovery` in [accounts_generation_test.go](../../internal/platform/accounts_generation_test.go) | Positive retained generation on typed root provision/disable; stale or same-generation changed keys fail before OS commands, exact retries are idempotent, approved newer fresh-key recovery retains UID/name. Covered by the reported native-source rerun; final all-suite pending. |
| `NAT-006` | `TestManagedAccountNeverProvisionedDisableRetainsGenerationTombstone` in the same source | Disable before first provision retains a generation tombstone across reopen without creating a user; stale queued provision cannot resurrect it, while authorized fresh higher-generation recovery can proceed. A keyless UID0/GID0 tombstone is not an OS root binding. Covered by the reported native-source rerun; final all-suite pending. |

```sh
go test -race -count=1 ./cmd/agent-identity ./internal/platform ./internal/identityservice \
  -run '^(TestIdentityNative|TestIdentityServiceLifecycleCLI|TestStoppedRecovery|TestManagedAccountGeneration|TestManagedAccountNeverProvisioned)'
```

Additional source-level account/crypto/identity-service/stream tests are listed
by area in [TEST_PLAN.md](../../TEST_PLAN.md); no test count is inferred here.

**Unperformed qualification:** real Linux/macOS multi-UID account creation,
server-group separation, private-key filesystem access as the runner, installed
sshd forced-command policy, native manager/reboot and root-local recovery drills
require separately authorized native evidence. Current tests never create host
users, modify sshd or register host services. Hardware signing/attestation is
optional future work with no implemented adapter or qualification. Full
software/RAM cloning and sequential use remain possible; these cases do not
prove perfect physical-clone prevention.

## Opt-in: live GitHub boundary only

### LIVE-001: explicitly authorized real GitHub review/rework/merge

**Automation:** `TestOptInLiveGitHubReviewMerge` in
[live_github_workflow_test.go](live_github_workflow_test.go).
This case requires two distinct authenticated GitHub actors and an explicitly
authorized disposable repository. It creates one PR, requests a change at the
first head, pushes a correction to the same branch, approves/merges the corrected
head and confirms its content on the default branch. Existing repository policy
must allow every operation. The default build substitutes the skipped test in
[live_github_disabled_test.go](live_github_disabled_test.go); a green local suite
or compile-only check does not qualify this live case.

**Not run by default, and not run as part of this implementation.** Live code is
compiled only with **both** `e2e` and `e2e_live` build tags. Ordinary
`bash scripts/test.sh all` / `e2e` cannot execute it, even with inherited live
opt-in variables. This separate harness validates real Git/PR/review/rework/merge
semantics, not live provider quality or the Foundation controller. The local test
above qualifies the latter. No flag enables paid inference.

Use an explicitly authorized **disposable repository** with two distinct GitHub
accounts and already-authenticated owner-only GitHub CLI configuration directories.
The test never reads/copies their credential contents or places them in prompts;
only native `gh` accesses them. It creates isolated HOME/Git configuration, one
unique branch/PR, and merges a small original fixture file under
`tests/_foundation_e2e/`. **Merged commits remain in the selected repository.**

```sh
FDN_E2E_LIVE=1 \
FDN_E2E_LIVE_REPOSITORY=OWNER/DISPOSABLE_REPOSITORY \
FDN_E2E_LIVE_WORKER_GH_CONFIG=/absolute/existing/private/worker-gh \
FDN_E2E_LIVE_DELEGATE_GH_CONFIG=/absolute/existing/private/delegate-gh \
  go test -count=1 -tags=e2e,e2e_live ./tests/e2e \
  -run '^TestOptInLiveGitHubReviewMerge$' -v
```

The separate live build tag, exact environment flag, and explicit repository are
all required; there is no repository default. Token environment variables are
not inherited. Existing branch/review/check policies remain enforced: an
unavailable merge fails, without
changing protections or using an administrative bypass. Cleanup closes only this
harness's unfinished PR and removes its unique owned branch. A lost external
response may still require explicit GitHub reconciliation; the harness does not
retry ambiguous writes or promise exactly-once remote effects.

## Maintaining and extending the catalog

For every new scenario or regression, record:

- **Stable ID and scope:** use the appropriate case family; do not renumber or
  reuse an existing ID. Distinguish an entire scenario from its checkpoints.
- **Preconditions and variants:** identities/roles, provider selection, task
  state, approval generations, local grants and fixture/live boundary.
- **Actions and expected results:** name the responsible actor, state changes,
  denied/no-op outcomes and observable Git/API/journal evidence. Include retry,
  cancellation, lost-response or restart behavior where relevant.
- **Automation and command:** link the source, name the actual Go test/subtests,
  and provide the smallest runner command covering the case.
- **Qualification limits:** distinguish implemented fixture coverage, planned
  work and separately observed live qualification; a compile or skip is not a
  passed live test. Record source/toolchain/platform with actual run evidence,
  never credentials or private transcripts.

Add the failing test before a production fix. Extend deterministic phase/context
rules and bounded fixture actions instead of adding task-title or repository
branches to production. Keep cases isolated: no case may rely on another test's
state or authorize a live write implicitly. Update the index and relevant runner
coverage in [TEST_PLAN.md](../../TEST_PLAN.md) with the implementation. The
catalog describes expected behavior; suite results remain source-bound evidence,
not permanent pass stamps in these tables.
