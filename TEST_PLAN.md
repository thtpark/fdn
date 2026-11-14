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

## Per-agent diagnostics and supervised restart

Authenticated model/default tests cover all three providers, missing and
unverified authentication, strict computer/key-generation and model membership,
server-clock freshness, unchanged-observation replay, self versus other-actor
authority, unchanged task/run/mode generations, exact preference acknowledgement
replay and ordinary model-choice messages. Both native editors must offer only
eligible picker options, never arbitrary or signed-out model IDs.
Antigravity fixtures cover official asset digest/size/layout, retained activation,
read-only ACP admission/catalog, empty-tool text and fixed coding profiles,
disabled client/MCP/delegation capabilities, denied interactive requests and
joined cancellation. Native help and no-auth admission probes establish only
those read-only capabilities, not authenticated paid inference or production.
CLI tests execute both documented `agent model-options` leaves and the native/
embedded observer snapshot, separately from `agent environment`; explicit report
replay retains the original observation. Catalog handlers must be sibling
dispatch branches, never unreachable nested environment commands.
Independent startup catalog probes may be pending or suppressed by an unsafe
legacy fence. Control tests require zero inference and unchanged mode/identity,
with at most one bounded probe per provider; diagnostic refresh additionally
requires its own observation probe. They never require a background metadata
loop to run before a reset/repair result.

```sh
go test -race -count=1 ./internal/domain ./internal/foundation ./internal/api ./internal/cli ./internal/upgrade ./internal/identityservice -run 'NativeRestart|AgentNativeRestart|IndependentRestart|EmbeddedProtectedAgent|AgentOperation|AgentReset'
```

Shared Swift diagnostic/model tests cover complete stale/offline/missing shapes,
exact agent routes, offline reopen, private connection/key-generation caches,
Founder AI/manual versus delegate/self authority, fresh exact fence/expiry,
unresolved reset replacement denial and exact replay. Native rendering and phone
UI tests must show actual diagnosis content and confirmation controls, not an
object field count or queued-success alert.

Native tests require signed permits, exact policy/computer generation/source,
fresh nonce start authority, fixed paired native effects and durable receipts.
Health and fresh-inspection tests retain all ten supported component observations,
including `environment`, through the worker producer, server heartbeat and
protected result validation. Unknown/duplicate components and excess observations
remain rejected; resolving a problem does not erase its retained evidence.
Root/native API fixtures deny ordinary Founder/worker access to the coordinator
inbox/lease/results, deny caller paths/proof/force options, and verify exact
completion. Lost apply responses use status and lost reports reuse the original
outbox without another restart/lease. Mode tests preserve prior/newer manual
intent and pending-work/session/report fences. Recording fixtures run without
root services, real keys, target builds, provider calls or deployed endpoints;
they are not live native restart or full-host restore qualification.

## Native named entity links

```sh
go test -race -count=1 . ./internal/foundation ./internal/api ./internal/cli ./internal/identityservice ./internal/observe ./internal/work -run 'RunRead|RunCLI|RunObservation|RunLookup|HumanFacing|Guidance'
```

Shared Swift `EntityReferenceTests` and `EntityLinkTests` cover exact route/ID
validation, names instead of raw IDs, Markdown/Unicode/punctuation, legacy bare
and braced references, safe external links, literal code and malformed links,
off-page lookups, visible failures, deduplicated reads, unchanged read receipts
and desktop back navigation. Mobile navigation tests retain the originating
stack and resolve exact record queries without relying on loaded pages.
Native run API/CLI/proxy/observer tests preserve task/claim state and source
scope; every provider phase requests `@ID` only in human-facing text while
retaining exact structured context. Build both apps and the source-bound mobile
runtime. These checks do not qualify installed CLI/server upgrades, signed app
distribution, physical devices or live providers.

## Model defaults, Adhoc reporting and self messages

```sh
go test -race -count=1 ./internal/foundation ./internal/work ./internal/provider ./internal/cli ./internal/api ./internal/identityservice -run 'Preference|ModelCatalog|Adhoc|Self|ConversationActionsRequire|PublicTaskReport'
go test -tags=e2e -count=1 ./tests/e2e -run 'NativePreferredModels|NativeConversationAction'
```

Founder defaults have independent revision/provenance and exact value receipts;
worker/delegate writes, stale revisions and protected fields are denied. Changing
defaults does not change mode/authority generations or overwrite local settings.
Check agent defaults, task overrides, verifier/attention defaults, source-bound
recovery and empty-model fallback for absent/unknown catalogs. Both native
provider fixtures verify actual CLI model arguments and resulting checked files,
not merely configured strings or a stored preference. Missing authentication and
unproved native cleanup must not become model fallback.

Read-only Codex catalog tests cover initialization, JSON-lines framing,
pagination, bounded parsing, unavailable catalogs and joined cancellation;
Copilot preserves authenticated status when only catalog discovery fails.
No test starts login, a real provider turn or paid inference.

Adhoc tests require meaningful title/scope/criteria and separate team-safe reports.
Private instruction/result markers must remain only in the private source, never
shared tasks or worklogs. Creation/claim/report logs are transactional and
duplicate-free on exact replay. Full 65,536-byte descriptions, 32,768-byte
summaries and 64 evidence entries of 8,192 bytes must survive bounded splitting.
Logs do not create attention or advance discussion/approval generations.
Raw/native/CLI self-message tests cover Founder and workers. Shared Swift tests
cover recipient exclusion, restored/direct mutation guards, historical self
threads, consecutive replies, task/default editors and exact proposal replay.
App builds and these fixtures do not qualify live provider availability,
physical iOS devices, a deployment or production security/capacity.

### CI timing and process-exit regressions

The race-checked unit runner has an explicit 15-minute aggregate per-package
alarm. Its 207-test Foundation package exceeded Go's implicit 10-minute alarm
on a hosted macOS runner while an ordinary migration fixture was only one
second old; this is not a longer native session, provider or admission deadline.
Synthetic API fixtures retain bounded error-level diagnostics on failure rather
than hiding an internal failure behind the deliberately redacted client fault.
Both platforms repeat private raw/native attachment and assistance-observation
cases together; their separate aggregate repetition budget is three minutes.

Real-clock session fixtures must leave margin inside the server's unchanged
60-900 second admission window, rather than request the exact minimum. The
automatic-cleanup integration fixture models a request constructed one second
before validation and uses a two-minute lifetime; scheduler/transport delay
must not turn its cleanup assertion into an unrelated expiry failure.

`TestOwnedProcessWaitsForDisappearanceAfterSignalPermissionRace` and
`TestOwnedProcessDeniedSignalCannotProveCleanup` inject the Darwin signal/reap
race after exact UID/start/group ownership revalidation. A signal's `EPERM`
is not cleanup proof: wait for actual kernel group disappearance without
re-signalling; persistent denial retains both the permission cause and bounded
context failure. The real legacy-reset fixture must also pass repeated race
runs while preserving unrelated processes and PID-reuse rejection.
These cases must pass on their first CI attempt, not through blanket retries,
relaxed production expiry checks or platform skips.

`TestOwnedProcessGroupAbsenceDoesNotAdoptAChangedPID` requires kernel ESRCH for
the exact old group before accepting cleanup despite a changed PID observation.
Live/replaced groups, permission denial and I/O errors remain unproved; no
changed process is signalled or adopted.
`TestOwnedProcessGroupDisappearanceDuringInspectionRequiresFreshKernelProof`
covers group disappearance between the first existence probe and the ownership
snapshot. A changed start/group/UID or a failed snapshot needs a fresh ESRCH
probe of the original group; live groups, EPERM and I/O failures retain their
causes and fences. Pre-signal snapshot failure likewise cannot signal a changed
PID, and can finish only after the original group is proved absent.

Linux process inspection treats both ENOENT and ESRCH during `/proc` reads as
an exited PID, while cleanup still proves process-group disappearance and
preserves permission/I/O failures.
Kernel effective UID comes from `/proc/PID/status`, not mutable procfs directory
ownership; non-dumpable/closing processes can have root-owned procfs files.
The start/group recheck rejects replacement between those reads. Synthetic
owner-change and replacement cases retain the genuine UID/PID/group fences.
Sharing input recovery rechecks the exact
fence after acquiring `work.lock`, never cleans a retired/replaced lease and
does not repeat native cleanup after durable proof. CI repeats these real-tmux
lifecycle/kernel cases 20 times and the complete task-safe handoff 10 times;
these repetitions are regression checks, not retries of a failed suite.
Input arriving during the target's acknowledgement observes its completed
server decision and durable native readiness, rather than silently losing the
first input after a server-visible grant. Denied acknowledgement never enables it.

## Human help and normal local access

### Unified sharing

```sh
go test -race -count=1 ./internal/foundation ./internal/remote ./internal/provider ./mobile -run 'Sharing|TestBridge'
go test -race -count=1 -tags=integration ./tests/integration -run '^TestSharing'
```

`TestSharingViewsContinueDuringExecutionAndInputUsesTheSameLogicalSession`
keeps an actual invocation blocked while Founder receives selected terminal
bytes through Foundation, rejects ungranted input, waits for task completion
before granting input under the same work lock, then releases input and
reconnects the original view with unchanged expiry. Selection/revocation tests
deny delegate endpoint choice and withdraw old Foundation relay streams before
reconnecting the same logical session. Exact server-time tests cover 0, 1,
86,400 and 86,401 second bounds, reconnect/replay without renewal, current
project/identity authority and protected credential surfaces.

`TestSharingOwnedTransportCancellationPreservesLogicalView` injects the native
target-side cancellation ordering through the actual Foundation relay. It must
reconnect the same active logical view without extending expiry; a normal owned
stream cancellation is not a failed sharing authorization or task outcome.

Native RFB net.Pipe fixtures exercise 3.3/3.7/3.8, local VNCAuth, None-only
authenticated viewer handshake, pixels, shared flag, per-event input gating,
both clipboard directions, power/resize denial, malformed/bounded messages and
joined cancellation. Unsupported/encrypted security/encodings fail closed.
Live output tests must preserve provider stdout/stderr limits and structured
reply bytes; reading a filename/status is not an output-stream test.

`TestSharingNative*` requires a real tmux executable and disposable owner-only
state under a short test root, with explicit long-state cases. Verify producer
survival after a viewer closes, real shell
output (not typed command echo), live private-log appends, ignore-size viewing,
retired-tail cleanup and exact-generation input cleanup preserving newer
terminals. If tmux is absent these tests explicitly skip: do not report native
qualification. Missing tools or denied isolated test builds remain reported
native-test prerequisites, not successful builds or host installations.
The long-state-path cases additionally exceed the kernel Unix address limit,
observe actual shell output in unchanged canonical state and explicit workspace
directories, retain exact server ownership, and prove native cleanup. They must
not shorten hashes, move state, weaken private directories or adopt old resources.
Shared app/phone builds and fixtures do not certify actual display/OS consent,
provider privacy, credential isolation, release rollout or fleet capacity.

`TestSharingHumanViewsInAIModeAndReconnectsTheSelectedTerminal` checks mobile
Founder viewing without manual mode or a grant, actual binary output across
selection changes, one retained creation/open/close and unchanged expiry.
Shared Swift `SharingTests` cover bounded approvals, selected-resource policy,
protected uncaptured invocation and exact sharing-control replay while Founder
remains in AI mode. Disconnect/quit cancels and joins owned sharing attachments;
late results stay private and cannot replace a different connection's state.

`HelpSessionOpen*`, `AssistanceHelp*`, `Human*` and `ProjectWorkspaceUses*`
verify typed help scope, unchanged immutable notices, server-clock expiry,
exact retained grant binding, denied stale/mismatched opens, default owned
workspace preparation even with the legacy false opt-in, explicit disabled
bindings and protected binary terminal I/O outside JSON/status/event paths.
Foreground teardown joins owned viewers and erases private bootstrap credentials.
Shared Swift `HumanHelpTests` cover skew-independent snapshot deadlines,
exact active-grant reuse, terminal-vs-authentication purpose and refusal to
parse commands from message bodies. Desktop/iOS task status stays in a
persistent top-level banner across tabs and scrolling. Run native rendering,
mobile bridge tests and iOS device/simulator/macOS-test slices. Fixture/build
evidence is not live protected sign-in, physical-phone privacy/network/kill
qualification or OS administrator/spending authority.

## Phase 6 files, publications and native attachments

`go test -race -count=1 ./internal/domain ./internal/foundation ./internal/api ./internal/cli ./internal/observe ./internal/identityservice ./internal/work ./internal/backup -run 'File|Attachment|Publication|SQL9'`
checks exact 1 GiB/256 KiB input limits, the same storage path immediately below/
at/above 10,485,760 bytes, immutable staged chunks/finalization/hash verification,
SQL8-to-SQL9 preservation/rollback refusal, owner/grantee privacy without Founder
bypass, explicit publication before shared attachments, generation invalidation,
unique receipts/notification/atomic designated cleanup and logical tombstones.
Raw/proxy tests must reject unknown fields, unsupported methods/query metadata,
scope escalation, third-party re-sharing and forged receipt provenance.
CLI/native fixtures verify durable local commit before acknowledgement,
interruption/lost-response replay, replaced/symlinked resources and no overwrite.
No inference, host service, production endpoint, credentials or physical erasure.

`FileStore*`, `SQL10File*`, `NativeFoundationColdBackup*` and
`ServerFileDirectory*` additionally verify actual filesystem bytes with no live
BLOB route, exact Foundation-host UID projections, grants/revocation/publication,
Founder-device activation/revocation, denied ACL installation and exact replay,
SQL9 materialization preserving IDs/grants/receipts, interrupted uncommitted
tails, archive-only committed bytes, root pinning and restore quarantine.
Native ACL adapters test real current-user resources plus deterministic ACL
plans/permission failures; synthetic business UIDs do not certify another OS
user's access. Linux cross-builds are not native Linux/multi-UID execution.
`FileDownloadedCLI*` and shared Swift receipt tests reject metadata-only,
missing/mismatched/shared/symlinked local copies, keep paths local, and verify
lost-ack replay still requires actual durable bytes.

`MessageAttachment*`, `AttachedReplyCLI*` and `MessageSendDefaultsResponse*`
exercise the actual desktop message/reply JSON shape through strict command/
request decoding, omitted/empty/populated attachment lists, conversion/native
transport, reply/thread linkage, exact lost-response proposals, current private
reader grants and rejection of third-party re-sharing/protected provenance.
Shared Swift composer tests verify omission of empty optional attachment lists
without losing populated attachments, reply IDs or explicit response opt-out.

`PrivateTransfer*`, `UnreadableAndOrdinary*` and `AutomaticFileReceipt*` additionally
test manual-to-AI pending notices with `response_requested:false`, no onboarding/
provider calls, owner-only automatic destinations, shared slot/claim/session/
maintenance admission, mode/grant/feed cancellation, before-receipt pause,
atomic server pause checks, lost-ack/restart exact replay and replaced staging.
Unreadable/deleted/ordinary attachments must not strand other eligible transfers.

Workspace-reception tests assert the actual file bytes/readable original names
under `workspace_root/downloads`, same-name file/sender isolation, owner-only
permissions, stable-ID fallback for unusable labels, recipient-chosen paths,
legacy state-path replay and unchanged destinations after sender/root changes.
Symlinked, writable/shared directories and traversal names must fail before
download/receipt; receiving bytes must not create workspace tool bindings.
Local download-status tests remove credentials and verify the original path/
completion/receipt is available offline with zero transport or acknowledgement.

The shared Swift `AttachmentsTests` exercise exact pending chunk replay after a
lost acknowledgement, private journal/generation isolation, complete/chunk hash
failures without receipt, owner-only finalized downloads, retained historical
draft references, distinct immutable version paging and bounded safe Markdown
image/video markers. Native views share FoundationMedia; previews use only local
verified content and do not acknowledge downloads or fetch external URLs.
Run related/full macOS Swift suites, rebuild iOS device/simulator/macOS-test
runtime slices, build the unsigned simulator app and run dedicated local UI
fixtures. Then run protocol reproducibility and every independent Go suite.

Verified filesystem bytes are embedded only into the isolated SQLite/trust
archive image while the shared writer lock excludes mutations. Live BLOBs
remain empty; old manifest2 archives stay recoverable. Oversized sources
must fail before an unrestorable archive is created. Backups/free pages and
previously downloaded copies are not secure-erasure evidence. These checks do
not certify live fleet transfers, physical-phone network/lock/kill behavior,
deployment, vendor/provider, large-database backup drills or capacity/SLOs.

## Computer environments and announcements

`go test -race -count=1 ./internal/local ./internal/work ./internal/cli ./mobile -run 'Environment|Inventory|SystemCapabilities|PrivateKnowledge'`
checks the shared capacity collector and private inventory projection: exact
historical JSON hashes, transactional legacy enrichment/concurrent refresh,
unchanged identity/knowledge/review/journals/outbox, provider path overrides,
no repeated initial AI review, detailed onboarding context and subprocess-free
embedded initialization. Repeated telemetry must retain the complete private
snapshot while refreshing live usage/runtime/executable versions. Projection
must not alias private resource pointers; changed totals require an explicit
inventory refresh instead of clamping counts or manufacturing a review.

`go test -race ./internal/foundation ./internal/api ./internal/local ./internal/work ./mobile ./cmd/agent-identity -run 'Environment|Announcements|Monitoring|WorkerHealth|Push|Mobile|IdentityVersion'`
checks measurable native/embedded resources, bounded version-only probes,
explicit missing observations, private-field rejection, computer/device/key
binding, server-timed staleness and non-renewing replay. Broadcast tests check
global/current-project Founder/delegate authority, exact audiences, independent
private threads, no implicit reads, no duplicate replay, stale delegate fences
and upgrade intent without forged permits. Existing deterministic conversation
action tests remain the execution/authority boundary.

Swift `MonitoringTests` cover typed views, missing-versus-zero values, private
draft restore, audience/exact-output shapes and delivery references. Native
renderer tests include system details and request forms. Rebuild iOS device,
simulator and macOS-test runtime slices; the foreground host publishes safe
computer facts but no worker health/controller/provider output. Run protocol
reproducibility and all independent Foundation suites. Local fixtures and
development builds are not live fleet, desktop/provider, upgrade or
1,000-agent qualification.

## Founder desktop onboarding and protected clients

`go test -race -count=1 ./internal/cli ./internal/api ./internal/identityservice ./internal/message ./internal/upgrade -run 'AgentAddition|BinaryRelay|Payload|AssistanceStatus|ReleasePublished'`
checks worker denial before SSH/terminal access, strict host pins and literal
inputs, current-user native options, signed-public-only registration, separate
installed/enrolled/connected observations, exact target replay, no reinstall
after uncertain acknowledgements or stopped services, exclusive literal JSON
writes, safe archive shape and exact server-version release selection.
The optional Foundation route additionally checks current server-side Founder
authority before endpoint dialing, literal IP/port and metadata bounds, four
pending/active streams, opaque chunk transfer, connection-private tickets,
cancelled admission cleanup and no HTTP/direct fallback. Real native OpenSSH
uses the actual raw CLI helper against an isolated Foundation business embedding
and loopback SSH server, preserving local operator-key authentication, percent/
quote-bearing proxy paths and rejection of a changed worker host key.
Real peer-checked Unix proxy tests retain the same owner transport through
renewal and close the exact remote ticket after local disconnect.
These fixtures are not a live installation or full-host SSH qualification.
Assistance status must be bodyless/read-only, preserve unknown session creation
and never open login/inference. Existing server registration/grant/relay tests
remain authoritative for server-side policy.

The sibling macOS package's `swift test --filter Administration` covers fresh
Founder authority, absent non-Founder Add agent UI, sign-in request shape/expiry,
explicit retained relay selection and unchanged default direct proposals,
exact grant requester/provider scope, screen/application/desktop-fallback
requirements, bounded private VNC bootstrap parsing, exact-origin browser policy
and a real PTY running an isolated fake CLI without transcript files.
The app build includes pinned SwiftTerm/noVNC dependencies and license notices.
With an explicitly built app, `FOUNDATION_NOVNC_ASSETS=/ABSOLUTE/APP/Contents/Resources/noVNC
go test ./internal/remote -run BrowserInstalledNoVNC` verifies the actual bundled
ES-module closure against the protected CLI asset loader, without a session,
desktop, provider or external connection.
These are local recording/process/native-render fixtures, not a live SSH
installation, provider sign-in, real desktop/VNC or native reboot qualification.
Then run the protocol check and all independent Foundation suites.

## Foreground mobile embedding

`go test -race -count=1 ./mobile ./internal/transport ./internal/identityservice ./internal/cli -run 'Embedded|Mobile'`
checks pinned in-process SSH fixed commands, wrong-host denial, the private
in-process identity attachment and unchanged proxy boundaries, offline
independent-key exact preparation, canonical owner-only state, early
cancellation and unsupported provider/service/file-input rejection.
Transport regressions also cover a multi-key server with only Ed25519 pinned,
ECDSA/RSA pins, RSA-SHA2, unrelated/hashed host entries, rotation, revoked keys
and host-certificate CAs. Negotiation must not bypass the actual host-key check.
Mobile observation tests cancel a blocked interest replacement, retain the
same owned stream for the next snapshot, reject pre-cancelled dispatch and
prove foreground close joins blocked subscriptions. Positive embedded manual
tests cover message/comment/task routes, typed results, exact revisions and
retained-proposal replay without provider execution.
Container tests must distinguish Apple-managed parent permissions from private
subtree checks, reject path/root replacement and prove native desktop ancestry
remains unchanged. The real iOS setup test prepares only fresh simulator keys;
it never enrolls a device or opens an external connection.
Run the protocol check and all Foundation suites after changes to these seams.

The sibling iOS package runs actual Swift/C/Go version/guide/key-preparation
tests and shared foreground proposal tests, then native simulator UI tests.
Build both device and simulator slices; a compiler target is not device
qualification. Real approved phone identity/owner reconnect, lock/unlock,
network changes, suspension/kill/lost-ack reconciliation and signed installation
remain explicit physical-device acceptance, not inferred from fixture tests.

## Desktop history and read-only observation

`go test -race -count=1 ./internal/domain ./internal/api ./internal/cli -run 'MessageResponse|MessageSendDefaults|ExplicitAIOptOut|NativeMessageResponse'`
and `go test -race -count=1 -tags=integration ./tests/integration -run 'DefaultRequestedMessage|ControllerMarksRead|EarlyReadLostAck'`
cover omitted response defaults, explicit opt-out/null/type rejection, native
replay, CLI-inferred operational requests, AI/manual behavior, optional AI reply
opt-out and read-before-processing receipts. `internal/work` push fixtures also
check read commitment before provider entry, lost-ack restart with the identical
source-bound proposal and no premature inference, paused-agent admission and
legacy prepared explicit-read replay. Shared Swift editor tests
cover fresh message/reply defaults and preserved restored false drafts.

`go test -race -count=1 ./internal/foundation ./internal/provider ./internal/work ./internal/local -run 'ReplyLoop|Adhoc|AssignedComments|Conversation|AIResponse'`
and `go test -race -count=1 -tags=integration ./tests/integration -run 'LaterThreadAction|AssignedUnmentionedCommentAction|AdhocActionLostReceipt|QueuedAdhocAction'`
cover the exact 199/200-message warning boundary, continued replies, real store
restart and replay deduplication, private warning content, creator-only Adhoc
readiness, generation-bound assigned attention, later-thread context, actual
fixture skill/file effects, report-before-response, lost receipt restart,
explicit model opt-out and Founder pause/resume. These use local deterministic
providers and temporary state, not paid/live inference.
`go test -count=1 -tags=e2e ./tests/e2e -run 'TestNativeProviderFailureAndMidRunQuestions|TestNativeTaskRecovery'`
also compiles the actual CLI/service and drives both scripted native provider
adapters. Safe retained partial output continues on the same task with two
executions and one reconciliation; a failed provider without an effect result
stays fenced after one concrete recovery review. Neither case requires another
human readiness decision or certifies live-provider behavior.
`TestClarityConsumesOnlyExplicitlyReviewedCommentGenerations` preserves the
existing productive clarification loop: incorporated answers are handled
atomically, unrelated comments and later edits remain pending.

`go test -race -count=1 ./internal/foundation ./internal/work -run 'TaskRecovery|UnusedRecovery|TaskRequestReconciliation'`
and `go test -race -count=1 -tags=integration ./tests/integration -run 'InterruptedZIP|UnprovedProvider|BlockedRecovery|TaskRecoveryLost|AutomaticTaskReconciliation'`
cover bounded same-task reconciliation, original provider/scope/run binding,
no human readiness handoff, precise unresolved risks, missing/unproved cleanup,
changed scope/Founder pause, and exact lost claim/report/reply reconciliation.
The ZIP case uses synthetic bytes with the original report's exact 20,210,825-byte
size, an actual verified receipt, nested ZIP/XLSX content (cell value 42), a real
owned native process cancellation/join, runner restart, one reconciliation and
two execution attempts on one task. It checks actual saved output, not a
provider-declared completion proxy. Unused native claims consume no inference;
unknown effects/new message IDs never renew a spent recovery budget.
These tests use isolated local TLS/storage and deterministic providers, not
private production file bytes, paid inference or full-host qualification.

`RecoveryInspection`, `RecoveryResult`, `RecoveryPrompt`, `RecoveryContinuation`
and `RecoveryReview` unit selectors check bounded real reads, missing/symlink/FIFO
failures, explicit tool-enabled admission, preparation-versus-execution sandbox
semantics, structured blockers and exact source-bound corrective authority.
`TestMetadataOnlyRecoveryCannotPassAsInspectionAndCorrectiveReviewKeepsAccounting`
rejects the live-style zero-retry refusal, preserves its reply/count and completes
the same ZIP task only after an exact corrective authorization.
`TestRecoveryInspectionCannotForgeOrReuseChangedEvidence` rejects missing/foreign
receipts, changed bytes, failed input checks, unexpected existing outputs and
new outputs appearing after an absence check. Native fixtures call the actual
paired CLI rather than manufacturing receipt IDs.

An opt-in `e2e && liveprovider` test,
`TestLiveCodexRecoveryInspectsZeroRetryZIP`, requires explicit
`FDN_LIVE_CODEX_AUTHORIZED=yes`, absolute `FDN_LIVE_CODEX_PATH` and owner-private
`FDN_LIVE_API_KEY_FILE` references. Obtain a current bounded spending
authorization before running it; normal suites neither enable it nor use keys.
It uses synthetic exact-size bytes, fresh fixture identities/state, a joined
controlled interruption and actual Codex recovery/preflight/execution. Assertions
require native inspection, actual nested workbook output bytes, one task,
two execution attempts, zero failures/retries and one recovery. Provider calls
and command/token totals are measured; at most six controller steps and bounded
native invocation time/output are allowed. Optional absolute
`FDN_LIVE_EVIDENCE_DIR` retains only owner-private synthetic invocation evidence.
Synthetic UID file permissions and same-user fixture proxies do not qualify
real multi-user ACLs, physical hosts, transport cutovers or deployed providers.
Authority-interruption fixtures install their immutable HTTP transport before
starting any controller feed; changing `http.Client.Transport` while a feed is
running is a data race, not a transport-failure injection. Tests retain one
immutable client transport and synchronize injected handler changes through
fixture teardown. Restoring the client field while a feed is closing is the
same race. The joined-interruption
fixture uses one bounded native process, while the separate unproved-cleanup
case deliberately retains its fence. Repeat the exact interruption/risk cases
with `-race -count=20`; do not rerun failed CI as a substitute for fixing races.

`go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeConversationAction'`
also drives the actual compiled CLI, identity proxy, daemon and both scripted
provider executables through a later-thread action: omitted response default,
applicable workspace skill read, file effect/check, self-assigned Adhoc claim,
actual report and exact-source result. It is not live-provider qualification.

`go test -race -count=1 ./internal/observe ./internal/foundation ./internal/api ./internal/cli`
covers private received/sent/thread pages, insertion-ceiling/keyset stability,
no implicit reading, signed actor/query/generation cursors, scoped post-commit
waits, unmatched-entity isolation and read-only stdio sessions.
Keep existing inbox/thread behavior, status no-op/event-budget semantics,
native principal/method/query validation and daemon checkpoints unchanged.
`discussion show ENTRY` / `GET /v1/entries/{id}` and the observer `entry` kind
must preserve exact entry/parent/generation metadata and current actor checks,
without acknowledging mentions, changing task state or writing a receipt.
They let local wrappers resolve original comment threads across page boundaries
without downloading every discussion page.
SQL8 must migrate current SQL5-SQL7 without changing payload bytes, insertion
order, receipts, identities or history. Then run the protocol check and all
independent suites. Fixture results are not live UI/fleet/deployment acceptance.

## Founder devices and concurrent connections

```sh
go test -race -count=1 . ./internal/foundation ./internal/api ./internal/device ./internal/pki ./internal/identityservice ./internal/cli ./internal/platform -run 'FounderDevice|FounderOwners|FounderOriginal|ManagedFounderDevice|NativeFounderDevice'
go test -race -count=1 -tags=integration ./tests/integration -run '^TestUnix'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeFounderDeviceEnrollmentListAndRevocation$'
go run ./tools/protocol --check
```

Regressions prove separate signed requests/keys/UIDs and device-bound activation,
certificates, exact operation replay, one Founder actor/mode, and a single
agent-wide task slot under competing claims. Raw API rejects workers, delegates,
spoofed roles and protected registration fields. Independent owners on multiple
Founder devices renew without invalidating one another. Per-device revocation
retires its challenges/certificates/live streams, preserves other device keys,
and cannot be bypassed by registration/provisioning replay. Self-revocation and
revoking the last active computer are denied; another device can revoke the
original computer. Whole-Founder recovery/restore retires all old device keys.

SQL7 migration tests preserve original SQL6 identity bytes, account reservations,
messages, requests, receipts and scoped-feed state, validate foreign keys/reopen,
and reject SQL6 rollback. Recording native-account fixtures test device IDs,
independent UIDs, lost acknowledgements and retained generation tombstones.
Actual Unix/mTLS fixtures prove concurrent Founder owner connections and the
unchanged non-Founder fresh-ping whole-identity ban. Real TLS/protobuf device
tests use distinct **logical** UIDs, not provisioned OS accounts. Compiled CLI
tests use the explicit business proxy. These are not iOS/Android/browser-client,
native multi-UID/sshd or per-device worker-management qualification.

## Founder-only runtime reset

```sh
go test -race -count=1 ./internal/domain ./internal/message ./internal/foundation ./internal/api ./internal/cli ./internal/identityservice ./internal/work -run 'AgentReset|RuntimeReset|FounderRuntimeReset|InterruptedRuntimeReset'
go test -race -count=1 -tags=integration ./tests/integration -run 'TestFounderRuntimeReset'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeFounderOnlyRuntimeResetIncludingSelf$'
go test -race -count=1 ./internal/remote -run '^TestExplicitReset'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeFounderResetClearsLegacySession$'
go run ./tools/protocol --check
```

Reset tests cover server-enforced Founder-only authority (including self-target),
denied delegates/self-authorized workers/spoofed roles, strict reason-only input,
raw API and real Unix-proxy method/depth/query bounds, exact durable CLI/request/
result replay, unchanged native wire envelope, fixed expiry/current authority,
preserved identity/mode/authentication/configuration/history, no provider calls
for reset, single joined session supervisor and no automatic uncertain retry.
Real loopback controls cancel an active deterministic invocation, retain its
uncertain journal through daemon restart, close an actual owned interactive relay
and retire only proven session fences. Real orphaned old-format Unix sockets
are inspected/retired by explicit reset, with private native proof and preserved
inode evidence. Native tests exercise kernel-authenticated server and pane
cleanup, missing-generation legacy fences, missing endpoints, unlinked live
sockets, replaced/foreign files, another native generation, wrong kernel
parentage and cancellation. Ordinary automatic cleanup still refuses legacy
adoption. Pending results/outbox, maintenance, unowned occupied slots and
genuinely unprovable/replaced resources remain operator-required.
Compiled-binary cases exercise Founder and worker resets with separate temporary
state and verify role denial, identity/configuration preservation and no inference.
`TestNativeFounderResetClearsLegacySession` specifically verifies that the actual
compiled command retires the real legacy fence/socket and that replay cannot
clear a newer fence.
These fixtures do not reset live agents, restart host services, prove cleanup of
unknown legacy resources or qualify production/provider recovery.

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
- Native sshd effective-policy parsing accepts case-insensitive directive names
  while retaining exact policy values and rejecting mixed-case duplicates.
- New signed registrations reserve normalized names; collisions roll back
  partial agents/keys. Explicitly reserved account names survive reopen,
  binding and fresh-key recovery without an ID-derived fallback.
- Recording and actual Unix/protobuf account-helper tests cover the exact
  approved username, returned-name mismatch, no unmanaged adoption, retained
  UID/name/generation, named never-provisioned tombstones, stale requests and
  changed-name rejection before OS tools. Foundation account naming remains
  independent of the existing worker's local OS username.
- Name/ID mention tests cover case-insensitive and spaced names, canonical
  sorted/deduplicated IDs, self-exclusion, unchanged body, replay after a name
  becomes ambiguous, atomic unknown/ambiguous-name create/edit failures,
  inactive names, exact ID mentions, blocking answers and stale receipts.
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

### Private-file requests and successive threaded replies

```sh
go test -race -count=1 -tags=integration ./tests/integration -run 'Test(PrivateFileRequestsUseReadyRecipientProviderWithoutSignIn|NestedCommentRepliesRequireEveryMentionedAgentToRespond|ConsecutiveThreadRepliesKeepRecipientAndRequireIndependentResponses)$'
```

Private-file cases cover ordinary message attachments and separately delivered
transfers, with retained or automatically deleted server content. Both Codex
credentials-present-unverified and authenticated Copilot recipients complete
actual fixture analysis of verified bytes through source-bound Adhoc execution,
including restart between planning/execution, without checking the signed-out
other provider or issuing sign-in help. File delivery/receipt itself invokes no
provider. Results stay in the private source thread; another actor cannot read
the file. Completed work is not repeated on polling/restart.

Nested comment replies mention two independently registered recipients, including
duplicate/self mentions, and require exactly one parent-linked response and
generation-bound receipt from each across two successive turns/restarts.
Consecutive direct replies retain their original other party and each receive
their own response; explicit opt-out remains unanswered. Shared Swift composer
tests submit successive replies to the sender's own previous message without
reversing the recipient or inheriting a previous response opt-out.
These are deterministic provider/business-policy fixtures, not a diagnosis of
Agent1's deployed authentication or native multi-UID qualification.

### Browser/file investigation with existing tools

```sh
go test -race -count=1 . ./internal/provider ./internal/work -run 'Guide.*(Catalog|Browser)|FileInvestigation'
go test -race -count=1 -tags=integration ./tests/integration -run 'OfficeAndPhotoQuestions|ApprovedTaskFileInvestigation|BrowserInvestigation|PrivateFileRequests'
```

Prompt/adapter checks distinguish reference-only message/comment planning from
tool-enabled execution/verification, retain explicitly requested browser methods,
permit Copilot coding `web_fetch` without calling it a browser, and keep text
tools/MCPs/delegation disabled. Embedded discovery, source hashes, private-home
preservation and the unchanged 32 KiB operating-guidance budget must pass.

`TestOfficeAndPhotoQuestionsUseActualPrivateBytes` exercises Founder/delegate
requests, actual DOCX paragraph/table XML, PPTX slides/notes and decoded image
pixels, plus follow-up questions without repeated file downloads or broadened
private readership. `TestApprovedTaskFileInvestigationUsesPublishedAttachments`
uses ordinary approved task claims and explicitly published attachments.
`TestBrowserInvestigationReportsUnavailableToolWithoutRetry` checks that a
missing browser produces a private paused result with a useful question, no
download/completion claim and no automatic execution retry after restart.
Existing source-disposition, Founder-pause, receipt-loss and single-slot tests
remain required. These are deterministic routing/content fixtures, not a live
browser download, full Office rendering/OCR/vision or model-quality certification.
Browser/document/image tools are not installed by tests or this workflow.

### Web pages and authorized ordinary website sign-in

```sh
go test -race -count=1 . ./internal/work -run 'Guide.*(Catalog|Browser|Website)|WebInvestigation|FileInvestigation'
go test -race -count=1 -tags=integration ./tests/integration -run 'WebInvestigation|WebsiteSignIn'
```

`TestWebInvestigationUsesActualPagesAndAuthorizedSignIn` uses a local HTTPS
application, real page bytes, an owner-only synthetic credential source and
cookie-bound account checks with both provider fixtures. Cover public inspection,
known-password sign-in, an existing authorized session and follow-up page
questions without another password submission. URLs/titles/login forms cannot
stand in for actual account-page facts.
`TestWebsiteSignInBlockersDoNotRetryOrLeakSecrets` covers missing authorization,
wrong credential origin/account/password, MFA and uncertain submissions, with no
guessed completion, bypass or retry on restart. Synthetic password/cookie/hidden-
field canaries must stay out of prompts, plans, journals, shared results and
private reply bodies. `TestWebsiteSignInLostReceiptReplaysWithoutAnotherLogin`
verifies exact report/receipt replay without another sign-in or provider call.
These are deterministic local tool/controller fixtures, not a browser-engine,
real-account, secret-isolation or live provider/model qualification. No browser,
credential manager, external account, actual credential or paid inference is used.

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
state/workspaces/socket, explicit current-format configuration paths and no
old-layout scan/adoption. Guide tests cover its consolidated store,
foreign discovery-link refusal, unchanged unrelated files and
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
`TestUpgradeStartLeaseBindsCallerCertificateNotOtherPermitSubjects` exercises
both current issuer and target with distinct owner-certificate fences, denies
caller substitution and stale owners before signing, and prevents treating
other permit subjects as the authenticated caller.
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

`go test -race -count=5 ./internal/upgrade ./internal/api ./internal/foundation ./internal/cli ./internal/backup -run 'Preflight|PublicRecipient|FounderUpgradeCLI'`
checks minimal-target Foundation-only check/plan/apply/status without an SSH
runner, explicit route selection without fallback, retained plan2/control IDs,
pending-status reads without dispatch and exact backup-key/native-operation
replay. Store/API/native tests require active Founder/device and exact target
privacy/generations, strict read-only actions, 32 pending controls, fixed expiry,
root-private Foundation versus worker boundaries, explicit failures/redacted
diagnostics, actual source/policy/platform and bounded reference receipts.
Receiver tests prove no stop/backup/install/start lease for preflight, exact
lost-observation replay without another probe/nonce, refusal before probing,
original maintenance-outbox priority and installed-source supervisor handoff.
These are deterministic source fixtures, not a live root upgrade, hardware
attestation or remote service-supervision qualification.

`TestNativeMessageReconcilesExactVerifiedReceiptWithoutRepeatingEffects`
prepares/applies through the actual native handler before delivering its queued
request, then requires accepted/executing/completed with the exact source/backup
receipt and no repeated stop/backup/install/start lease. A different validly
signed permit must fail without adopting that receipt or performing new effects.

`TemporaryMaintenance*`, `VerifiedMaintenance*`, `TemporaryUpgrade*` and
`NativeVerifiedUpgrade*` cover whole-rollout verified restoration, exact original
pause/key generations, unchanged preexisting manual intent, newer Founder AI/
manual decisions, immutable batch-ID/replay, strict native source/permit/backup
provenance, independent native/session/work/maintenance fences and transactional
preflight. Blocked restoration must retain completed installation history and
emit one non-inference Founder reason; storage failures remain explicit/atomic.
Raw/proxy maintenance method/query/depth and non-Founder denial remain required.

`TestFoundationMaintenanceFailureNoticePreservesTypedMailboxReference` covers
Foundation-sender declined/deferred/failed native requests under the actual
immutable SQL5 party trigger. Both target decision and blocked-maintenance
notices must retain their typed source request/state, exact pending-report
replay must add no duplicate notice, and authoritative manual mode remains.
This reproduces the deployed acknowledgement failure without editing a live
database; the 0.3.40 repair changes only notice construction.

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

`TestMessageRequestCurrentSchemaPreservesRecordsAndUpdates` checks
message bytes/order, mode, operations, scoped feed/cursors, private update triggers,
ordinary enrolled-party constraints and SQL4-binary refusal. Existing migration/
restore fixtures preserve current SQL5 state and explicitly reject prebaseline
databases without changing their records.
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
| Identity registration/recovery | Current signed public-key possession, Founder-only new registration, canonical-key/request/account/UID uniqueness, exact activation challenge/replay, retired generations, fresh-key enrolled identity recovery and root-local-only Founder recovery |
| Tasks/policy | Named/independent eligibility, project epochs, invalidation, status gates, concurrent stale revisions, exact execution/verification claims/reports; verifier's current spec/result/discussion/epoch, independent own-provider selection, shared server slot, atomic feedback/rework approval, revoked authority/Founder pause and no self-acceptance |
| Discussions/messages | Creator-only edits, literal mention/body generation, requested answers, private recipients, no automatic protected replies |
| Shared scoped notes | Founder-only company edits; current active-project delegates; self/Founder and assignment-bounded non-Founder agent edits; revocation/archive/reassignment/terminal-task denial; exact revisions/replay, explicit clear, 64 KiB boundary, three histories, bodyless scoped updates; all task phases and independent reviewer's own context; next-call refresh, retained snapshots, missing-context failure and private knowledge separation |
| Persistence/restore | SQL5-to-SQL6 notes and SQL6-to-SQL7 device migrations/current SQL7 reopen, prebaseline/future schema rejection without mutation, current backup manifest2 only, prepublication ALL-identity/device/owner/key quarantine, missing-binding refusal, enrolled restored Founder root-private fresh-key recovery retaining ID/history, regular fresh-key reapproval, unchanged mode/uncertain work, atomic failures, immutable operations, three-history bounds, integrity |
| Worker/controller | One slot across manual/service/session/execution/verification, onboarding once, no idle inference, partial results/questions, retry/backoff/budget, offline report replay and no duplicate inference/review/merge; prepared review reply, claim-ack recovery, uncertain verifier failure, discussion/mode/workspace cancellation, disabled-binding preservation and manual resolution |
| Providers/processes | Exact required flags, text tool isolation, prompt through stdin, private/bounded output, version/model pinning, cancellation of owned descendants, failed signed staging retains previous version; actual login PATH/bare command versions, profile preservation/collisions, current-version dispatch and real foreground-terminal input; advertised Codex/Copilot device-flow selection, legacy fallback, bounded help-only probes, protected tmux/local TTY display and public-link versus credential handling; missing-authentication to real Founder/delegate inbox before onboarding without inference, private/protected notice fields, exact replay across restart/lost ack, explicit pending failures, 15-minute expiry/legacy deadlines, no automatic renewal or receipt revival, Founder-only typed refresh including paused/offline targets, scope/authority checks, no notification spam or auth inferred from receipt acknowledgement |
| Sessions | Request != consent != explicit open, current scope/expiry/revocation, no public listener, restart retirement, cleanup fences |
| Autonomous control | Only Founder pauses/resumes; target/delegate/offline overrides denied; event-driven cancellation and server claim gate; version-1 server migration preserves identity; legacy unexplained manual pause fails closed |
| Persistent assistance | Only Founder issues/revokes; exact requester/target/purpose/mode/provider/project; no-project authentication bootstrap; target prepares unattended without local consent; disconnect/restart/expiry retains grant; role/identity/revocation closes streams; delegate is not assumed human |
| Native identity/ingress | Recording account adapters/in-process SSH, unique UID/account/key reservations, positive per-account generation on typed root provision/disable; stale/same-generation-key-change rejection before OS calls, exact replay, never-provisioned disable tombstones, higher-generation fresh-key UID/name preservation and pre/returned/post-bind/current registry tuple checks; no arbitrary commands/PTY/forwarding, wrong-peer/pin/certificate rejection, private root maintenance outside agent gateway; no host users/sshd/services changed |
| Owner/current-user boundary | Device versus ephemeral owner certificate purposes, instance/agent/device/key/owner-generation agreement, kernel peer UID checks and owner-only IPC, same-user key ownership but no private/control export over ordinary IPC; independent Founder devices/connections and scoped revocation share one work slot; confirmed distinct live non-Founder owner proofs plus fresh incumbent binary ping ban original and contender, while stale crash/reconnect/unproved attempts do not; ban cancels API/streams, recovery keeps work fenced |
| Native services | Recording adapters by default, one existing installing user/HOME with independent agent name, both user systemd services/macOS LaunchAgents, unchanged HOME/groups/shell, no root/sudo/runuser/new account requirements, private 0700/0600 IPC, strict current single-user fields and retained state, raw scalar systemd paths, user-bus environment only for registration |
| Guidance/docs | Valid skill names/frontmatter/descriptions; local links/command coverage; no legacy checkout dependency; hashes/version completeness; preserve home instructions/unrelated skills; prompt inclusion; isolated homes; native and Debian guide content |
| Release/install | Merged-release-only event/commit guards, pinned Actions, two matching builds including agent-identity, native/worker binary lists and executable bytes/modes, all four archive binaries allowed/required/activated, missing identity payload denied before prefix changes, Debian parent directories and real unprivileged extraction with executable hashes, trusted manifest, immutable/idempotent installation and state preservation |

Use existing helpers and one runner invocation with related selectors rather
than repeatedly running the entire suite. Add faults/races at the actual
transaction/process/network boundary, not just a proxy string assertion.

## Shared company, project and agent notes

```sh
go test -race -count=1 -tags=integration ./internal/domain ./internal/foundation ./internal/api ./internal/cli ./internal/identityservice ./internal/work ./tests/integration -run 'Test(Note|AgentNotes|ScopedNotes|PublicIdentityStatusAndFounderRevocation|WorkerProxyForwardsExact|UpdateRouting)'
go test -count=1 -tags=e2e ./tests/e2e -run '^TestNativeScopedNotesPersistAndReachBothProviderTaskPrompts$'
go run ./tools/protocol --check
```

These regressions check transactional authority and competing revisions, exact
content bounds/output shapes, current assignment/delegate fences, explicit
clear versus missing content, bounded history/receipts, SQL5 record preservation
and SQL6 reopen, strict binary/Unix-proxy selectors and CLI proposals.
Real loopback server/controller fixtures observe actual prompts at onboarding,
scope, acceptance, preflight, execution and independent verification, including
fresh next-call notes after an in-call edit and immutable private journal hashes.
Compiled CLI/runner/provider fixtures exercise both Codex and Copilot adapters.
Private selected skills remain local, unrelated projects/agents are omitted,
and notes never grant approval, cancel pinned calls or reset retry fences.
These are source fixtures, not live model compliance, deployment, remote
resource fetching, native fleet qualification or attachment-storage evidence.

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

Shared Swift app-model tests additionally exercise deliberate acknowledgement
when an incoming body is displayed, serial queued receipts, hidden/sent/read/
protected exclusions, unchanged disposition/revision/response requirement, no
provider checks, AI-mode acknowledgement without publishing authority, and
lost-ack/reconnection without automatic replay. Native rendering tests verify
viewport-visible bodies rather than merely mounted offscreen records, including
reusing a view for another message. Foreground mobile fixtures use the same
model and successive reply composer. CLI/observer/background reads remain
read-only; displaying a message is the app's separate acknowledgement action.

## Tool-disabled response-contract regressions

`TestNativeCodingToolsUseExplicitWorkerConfiguration` runs a real compiled
worker CLI inside a native coding child with a non-default configuration,
unrelated inherited context, isolated HOME and synthetic provider. It must
observe the correct enrolled fixture identity without paid inference. Unit
tests cover explicit flag precedence, missing-context failure without fallback,
both providers' coding-only export and no prompt/argv disclosure.
Codex machine-output tests keep event bytes distinct from the final reply file,
reproduce human diagnostic overflow rejecting a valid response, and require
actual stderr overflow to remain failure in machine mode. Native owner
readiness/ping/revocation uses a real Unix socket rather than an unbuffered
in-memory pipe; safe fixture status is emitted on failures without credentials.
Native terminal fixture failures retain a bounded synthetic-output tail and
the exact attachment process identity/join state. Linux CI repeats long-socket
attachment startup separately to expose intermittent failures without hiding
them behind a successful sibling pull-request workflow.
Workflow contracts require distinct main/release check names and execute the
publication filter against synthetic API responses. A same-head sibling PR,
different source/event, unfinished/failed run, or acceptance outside the
release PR's pre-merge interval must not authorize publication.
Assistance message comparisons preserve every immutable/private field while
allowing the server's live observation timestamp to advance. Runtime-reset
fixtures use immutable state roots when checking retained native evidence while
the controller reconciles configuration; both regressions repeat under race
detection on Linux and macOS.
Native startup must prove the exact `foundation` session, not merely an owned
tmux server. A deterministic real-tmux fixture withholds session-admission proof
after server discovery and fails if startup returns early. Missing sessions are
queried within a three-second startup context that also bounds blocked queries
and respects earlier caller deadlines; an unready deadline, unexpected
query/cleanup failure or nonzero client exit remains explicit failure.
Native sign-in fixtures wait for durable local delivery confirmation before
asserting handoff state or restarting the daemon. A notice already visible in
Founder's inbox is not proof that the target has committed its acknowledgement.

`TestOwnerRenewalHandsOffNativeWatchWithoutDisconnectingCaller` keeps a logical
binary authority query active across renewal and verifies its real reply and
old-client closure. `TestOwnerRenewalMovesNativeWatchesBeforeOldCertificateExpiry`
uses actual mTLS/native framing and the Unix identity proxy, waits beyond the
old certificate expiry, and checks unchanged update/event cursors and queries.
It uses fixture certificate issuance and accelerated lifetimes, not live SSH or
multi-user identity qualification. Watch-only replay tests exclude mutations,
nonblocking reads and unrelated queries; changed owner generations/lifetimes,
caller cancellation, failed replacement validation, real denial and unexpected
transport failure remain conservative. Expiry/revocation/shutdown still terminate
other draining calls. `TestInterruptedExecutionReportsAuthorityChannelInsteadOfProviderFailure`
checks one stopped coding run, its non-secret specific report and unchanged
unsafe-retry requirement. `TestStoppedText*` checks three-attempt source budgets, 5/10-second
wakes, restart/new-operation non-reset, success and unrelated-source isolation;
unproved cleanup remains globally fenced. The paused-file catch-up integration
queues both transfer and instruction while manual, resumes, plans and reads the
actual private bytes despite an unrelated settled text failure, and returns
the analysis without duplicate execution after restart.
Settled sign-out remains source-fenced even after credentials return; an explicit
runtime reset joins a text-only call but never grants it an automatic retry.
Coding/unknown-cleanup journals retain the original global uncertainty behavior.

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

### Autonomous session cleanup and fence retirement

```sh
go test -race -count=1 -tags=integration,e2e ./internal/remote ./internal/local ./internal/foundation ./internal/work ./tests/integration ./tests/e2e -run 'Test(Automatic|SessionCleanup|Native(Close|Prelaunch|Recovery|Assistance)|OwnedProcess)'
```

Fixtures verify no-process tmux failure retirement, retryable failed Close,
durable restart proof, exact kernel process-start/UID/group ownership, stale
sockets, PID reuse/foreign-socket rejection, active sessions, occupied slots,
unrelated invocation journals and replaced fence generations. Proof CAS and
atomic cleanup/receipt replay must never clear a later fence.
The daemon integration holds a real granted session while a message is pending,
injects transient cleanup failure, closes the session and observes automatic
reply resumption exactly once without a human recovery action. Compiled requester
CLI tests reconnect under the same retained grant after automatic cleanup.
Server tests enforce zero human notifications before the exact 30-second
server-clock grace, no notification after prompt recovery and one deduplicated
fallback for persistent failure; target wall-clock skew cannot skip the grace.
These are deterministic local software fixtures, not live provider, production
recovery, native fleet or hardware-assurance qualification.

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

API/schema contract 2, current server SQL7 and preserved local SQL1 are separate
version checks. On 2026-10-21 the coordinator confirmed `SEC-001`–`SEC-010` and
`ATT-001`/`ATT-002` source fixtures in full integration (25.673s) and e2e
(94.630s); enhanced both-controller PolicyViolation ban notification and
invalid/replayed-proof no-ban `TestUnix` passed (8.432s). See
[SCENARIOS.md](tests/e2e/SCENARIOS.md#security-boundary-cases-source-suites-verified-final-acceptance-pending).
These are working-tree fixture results, not a final all-suite pass for later
restore/lifecycle/account-generation replay hardening or native deployment.
The historical duplicate-Founder result is superseded by the Founder device
and concurrent-connection regressions above; current duplicate-owner auto-ban
applies only to non-Founders.
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
  stale/crash versus confirmed simultaneous non-Founder owners, concurrent
  Founder connections and
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
