# Developing Foundation

This is the complete Go development entry point. No legacy checkout, Rust
toolchain, CouchDB server, VM, or container engine is required.

## Read in order

1. [AGENTS.md](AGENTS.md): operating and coding boundaries.
2. [SCHEMA.md](SCHEMA.md): identities, authority, persistence and fencing.
3. [COMMANDS.md](COMMANDS.md): actual CLI/API contracts and input examples.
4. [TEST_PLAN.md](TEST_PLAN.md): independent local suites and evidence limits.
5. [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md): delivered scope and backlog.
6. [CAPACITY.md](CAPACITY.md): measured evidence and the 1,000-agent target.
7. [DEPLOYMENT.md](DEPLOYMENT.md): native installation, upgrades and recovery.
8. [MIGRATION.md](MIGRATION.md): preserved design decisions and deliberate changes.

## Toolchain and feedback loop

Use Go 1.27 or later. Race tests need the platform race-detector toolchain and C
compiler; release binaries are CGO-free. OpenSSH, `/bin/sh`, and standard native
archive tools support local integration/installer checks. Runtime Node/npm and
provider accounts are not test prerequisites.
Protocol regeneration/static checks also require protoc 36.2; CI installs its
checksum-pinned upstream compiler. The Go plugin is built from the module's
pinned protobuf dependency. No generation or source build runs on deployment targets.

```sh
go mod download
go test ./...
go run ./tools/protocol --check
mkdir -p bin
go build -o ./bin/ ./cmd/...
bash scripts/test.sh all
```

Install dependencies only after manifest changes or an actual missing-tool
failure. Use the smallest related suite during development, then all independent
suites before a release. Do not build a release package locally or on a deployed
computer. Do not run test services against an existing Foundation/state directory.

### Embedded iOS development

The public `mobile` facade and `mobile/capi` bridge build the actual CLI and a
limited foreground identity/service host into the sibling iOS app. The mobile
host has no controller, provider execution, task claims, native receivers or
worker-health publisher. Desktop binaries remain CGO-free; the app-linked
C archive needs CGO, Xcode and its SDKs. There is no `os.Exit`, process-global
HOME change, shell/CLI subprocess or HTTP relay in the mobile dispatch path.

From `../fdn-ui-ios`, `bash scripts/build-app.sh` builds source-bound arm64
iOS device/simulator and macOS-test XCFramework slices, generates the native
project with XcodeGen, and builds an unsigned simulator app. These are development
artifacts, not Foundation release packages or device/distribution qualification.
The Swift app shares the macOS `FoundationCore` DTOs, editors, thread projections,
manual policy, paging and durable exact proposals.

Use temporary keys/state, native loopback fixtures, and a dedicated simulator.
Physical phone enrollment, Developer signing, protected-data/network/kill
acceptance and publication require separate authorization/evidence. Never copy
desktop keys or run the desktop `agent-service serve` controller on the phone.

## Running source with an isolated testing identity

No separate manual build/install is needed:

```sh
./scripts/agent-cli.sh --version
./scripts/agent-cli.sh config init
./scripts/agent-identity.sh prepare --name LocalTest --output "$PWD/.local/.foundation/public-request.json"
# After explicit signed registration/profile approval:
./scripts/agent-identity.sh connect --profile "$PWD/.local/.foundation/public-profile.json"
./scripts/agent-identity.sh serve
# Other terminals, while that identity process is running:
./scripts/agent-cli.sh connect
./scripts/agent-service.sh serve
```

Go still compiles: the wrappers use Go's existing build/module caches and
automatically refresh all three development executables in `.local/.foundation/bin`,
then `exec` the selected executable so native signals, stdin, arguments and
exit codes survive. They stamp `VERSION-dev` and the actual commit/dirty source,
not a release identity. They never package a release.

The wrappers set the testing HOME to this checkout's ignored `.local`.
Foundation-owned files are grouped beneath `HOME/.foundation`: `config` holds
the two private configurations, `state` holds runner data, `identity` holds keys,
`run` holds the IPC socket, and `workspaces`, `bin`, and `guidance` hold their
respective managed files. The same layout applies outside development with the
actual HOME; system server paths remain explicit and unchanged.

The wrappers do not set or clear XDG, Codex, GitHub, Cloud SDK, SSH-agent or
credential environment settings. Existing values remain available; otherwise
each tool chooses its own defaults for the selected HOME. Those external tool
directories are not Foundation state and are not moved into `.foundation`.
This is not an OS sandbox or a guarantee of isolated provider authentication:
inherited overrides or OS credential stores may still provide access. Go's
existing build/module caches remain shared.

Existing current-layout identity configuration is not overwritten.
Other locations are neither scanned nor adopted. See
[explicit configuration and current paths](MIGRATION.md#explicit-configuration-and-current-paths).
Symlinked or other-user-
writable test directories fail before adoption. Keep `.local` out of Git and
never copy a running production identity into it. Additional Founder computers
use their own fresh signed `device register` enrollment; replacing Founder's
original keys still needs root-local fresh-key recovery. Wrappers do not enroll,
rename agents, reset journals/fences, publish or deploy.
Use these foreground wrappers for development, not installed native service
registration. Native managers continue to bind the actual OS user's HOME.

## Code map and extension seams

| Area | Location | Responsibility |
| --- | --- | --- |
| Entrypoints/commands | `cmd/`, `internal/cli/` | Flags, bounded input, envelopes, private write proposals, native operations |
| Contracts | `internal/domain/` | IDs, DTOs, generations, errors and limits |
| Server authority | `internal/foundation/` | Transactional policy, enrollment, projects, tasks, reports, discussions, messages/sessions |
| Persistence | `internal/database/` | Private SQLite, WAL/full sync, migrations and integrity |
| Transport and identity | `internal/api/`, `internal/pki/`, `internal/transport/`, `internal/ingress/`, `internal/device/`, `internal/identityservice/`, `cmd/agent-identity/` | Signed public registration, pinned SSH stdio, peer-UID/mTLS binding, protected identity proxy and owner control |
| Binary message contract | `internal/message/`, `tools/protocol/` | Locked protobuf schema, bounded framing, duplex commands/queries/updates, priority ping/cancel and reproducible generation |
| Worker durability | `internal/local/`, `internal/work/` | Identity binding, knowledge, inventory, workspace authorization, serial controller, journals/outbox |
| Provider/process boundary | `internal/provider/`, `internal/process/` | Capability/auth probes, staged updates, stdin prompts, cancellation and bounded private output |
| Human sessions | `internal/remote/`, `internal/relay/` | Explicit open + consent, expiring relays, native endpoint/viewer control |
| Native deployment | `internal/platform/`, `cmd/agent-identity/`, `internal/distribution/`, `tools/package/`, `scripts/` | Existing-user paired service plan/install/status, user systemd/LaunchAgents, recording lifecycle tests, deterministic packages and trusted installation |
| Embedded guidance | Root guidance package, `.agents/skills/`, root Markdown | Same versioned knowledge in source, binaries, packages, prompts and worker homes |

Keep policy in the server's transactions, not just friendly CLI validation.
Use the existing `process.Runner`, provider executor, recording native adapters,
and real loopback fixtures for boundary tests. Do not introduce a generic plugin,
queue, broker, configuration framework or second database merely in anticipation
of a future need.

## Making a behavior change

1. State the exact contract and failure/recovery cases. Find the existing helper
   and tests before adding logic.
2. Add/update DTOs and narrow validation; reject unknown JSON fields and invalid
   input explicitly. Do not accept protected fields in ordinary edits.
3. Apply server policy and durable state changes in the existing transaction.
   Retain immutable operation fingerprints and bounded receipts.
4. Wire CLI, API/client, controller/manual path, native packaging, and related
   docs/skills as applicable. A supported command must be in executable help and
   the complete [command reference](COMMANDS.md).
5. Add exact authorization, version/race, replay and privacy regressions. Failure
   must remain observable; never replace it with an empty list/default success.
6. Verify the smallest suite, then all required suites. Record what was actually
   exercised and what remains unqualified.

Interactive cleanup is target-owned and autonomous before operator escalation.
Keep native ownership records generation-bound and private, validate kernel
process start identity/UID/group before signals, and retry only the same owned
resources with bounded cleanup and backoff. Commit cleanup proof and retirement
against exact fence fingerprints with durable receipts. Never recover by
guessing a PID, adopting a socket, rewriting legacy evidence or replaying
inference/login. Preserve Founder pause and every independent work fence.

The controller owns execution/verification admission and reports. A provider
adapter supplies structured observations/results; it does not receive authority
to accept its own work or start another AI worker. Independent delegated review
uses the same durable coding journal/claim/report/outbox path, its own local
provider/workspace, and current server-bound result/discussion/project fences.
New scenarios should extend deterministic test-provider plans and isolated Git/
Git-host fixtures rather than add task-title, provider or repository branches
to production policy. All providers must prove non-prompting local
approval behavior before enabling coding; text calls must remain tool-disabled.

### Founder desktop onboarding

Founder desktop onboarding is implemented by `internal/cli/agent_add.go`,
reusing the published release catalog, signed registration and paired existing-
user services. Keep operator SSH/passwords in a protected human PTY; no new API,
native payload, identity owner or SQL migration is implied. Test uncertain
native-stage fences and actual native service status, not merely downloaded
files or accepted registration. The macOS wrapper's protected terminal/browser
must remain separate from captured JSON processes and durable non-secret
proposals. Do not start a live installation or login during local development.

### Saved diagnostics and bounded Founder controls

Worker health piggybacks version 1 on the existing heartbeat; it does not grant
mode/work/retry/recovery authority or require a new SQLite migration. Keep phase
transition sequence, fresh snapshot time, loop liveness and real work progress
separate; repeated idle steps must not advance progress. Include effective
provider/model versus configured reply preference and timestamped actual auth
observations, not a sign-in command's exit status. Project delegates receive
scoped server-created health messages, not general access to another agent or
unrelated private message diagnostics. Redact private journals/raw errors/paths.

Focused CLI regressions cover typed bodyless GET routes, strict arity, existing
enrollment and error envelopes, unknown/stale observations without a healthy
fallback, offline help and command discovery. Run them with embedded guidance
contracts using `go test -race -count=1 . ./internal/cli`. Server/watchdog and
worker semantics need their owning package tests; CLI fixtures do not prove
live Agent1 recovery, external outage detection, provider auth or deployment.
See [TEST_PLAN.md](TEST_PLAN.md#diagnostics-cli-and-guidance-acceptance).

Keep `agent diagnose AGENT` as bodyless cached GET; only explicit `--refresh`
submits a durable Founder-only operation through the existing CLI proposal path.
`agent repair AGENT --input FILE` binds exact session/fresh fence SHA256, not a
remote stopped-process assertion. Both use `agent operation REQUEST_ID` for
safe eventual state/results. Queued/checking/offline/expired is not fresh success.
The operation's fixed 900-second server-clock lifespan is separate from
authentication and session lifetimes. Keep dynamic `server_observed_at` outside
immutable fingerprints, and the retained conservative server-time result
`captured_at` separate from skewable target inspection/provider/health UTC
observations. Derive the bound from a fresh server response and monotonic
request-start/round-trip timing, not target wall time. Source changes do not
imply deployment or a version bump.

The independent non-inference worker control loop must remain responsive while
paused/fenced/busy without overwriting in-flight health/phase. Recovery is exact
terminal-session, current-generation/current-fence and existing native
`CleanupProven` only, under `work.lock` with atomic mutation/receipt replay.
Legacy/unproven fences require an operator, not force, process-name/PID absence,
evidence erasure or a guessed native restart. A recovered fence does not change
mode/approval/spending/retry authority or promise Agent1 resumed. Test strict
input/flags, immutable replay, protected control/result privacy, real Unix-proxy
method/depth/query/header boundaries and embedded help/guidance. Keep raw output,
errors, credentials, process arguments and workspace paths out of safe results.

## Schema and compatibility changes

Founder-only runtime reset reuses protected agent operations and the existing
reason-only wire envelope. Keep current key generations/expiry, joined owned
work and session cancellation, one-slot admission, exact replay and retained
uncertainty. A reset must not erase journals/outbox, retry authority, identities,
provider sign-in or mode. An interrupted reset must not cancel newer work on
restart. Test Founder self-reset and denial through raw API/Unix proxy as well
as the CLI; it is not an OS-service restart or a force-clear capability.
Recognized terminal legacy tmux endpoints require actual bounded native cleanup,
not a blanket missing-journal refusal. Preserve exact socket/inode evidence,
kernel peer/start/group and pane-parent checks, independent kernel endpoint
absence, private generation-bound proof and atomic matching-fence retirement.
Ordinary automatic recovery and exact repair retain their existing restrictions.
Test a real orphan Unix socket through the compiled reset command and assert
the fence is inactive, the original inode preserved and replay leaves newer
resources untouched. An `operator-required` result is not a successful reset.

Server, local-worker and guidance formats have separate responsibilities.
`PRAGMA user_version` tracks each SQLite schema. The current API/schema contract
version is **2**; native message protocol is **2**, server SQLite migration is
**8**, local SQLite remains **1**. Existing SQL5 is the server-state floor;
SQL6 adds notes, SQL7 adds Founder device bindings and SQL8 adds observer/history
indexes while preserving all business records and shipped SQL1-SQL7.
Read [SCHEMA.md](SCHEMA.md) before changing either database.

- Add ordered, idempotence-tested migrations; do not rewrite a shipped migration.
- Scoped Markdown notes live in SQL6, separate from private local knowledge.
  Keep company/project/self snapshots atomic, editing authority transactional
  and project-assignment-bounded, and every new text/coding invocation wired to
  the same refresh path. Edits must not cancel pinned calls, alter approval/
  project epochs or reset recovery/retry fences. Preserve references as text;
  never implicitly fetch files, websites, repositories or private knowledge.
- Reject databases newer than the binary rather than resetting them. Restore
  requires manifest schema 2 and a supported SQL5-or-newer server database,
  and quarantines all restored identity/owner/key epochs before publication.
  Test root-private enrolled-Founder recovery preserving ID/history, regular
  fresh-key reapproval, retired credential denial and unchanged mode/work fences.
- Test migration of representative prior state, unknown/future versions, crash
  recovery and reopen/integrity. Preserve existing identities, operation IDs,
  generations, private bindings, knowledge, journals and outbox.
- Do not assume binary rollback can read an upgraded schema. Define and verify
  a compatibility/restore path before deployment.
- Version an incompatible API/DTO and document upgrade order. Do not add a hidden
  success-shaped fallback for an older client. Existing server SQL below 5 is
  rejected unchanged; immutable shipped statements still initialize an empty
  database. Registration creates a new signed identity; recovery retains an
  enrolled identity. Preserve worker SQLite1, knowledge, journals, outbox and bindings.
- Keep long-term keys in private state owned by the existing installing user,
  never provider prompts or shared output. The runner and identity process use
  the same UID/HOME; no new local account/group or UID-based key compartment is
  required. Test current-user defaults independently of the enrolled agent name,
  unchanged HOME permissions, private 0700/0600 IPC, user native-manager calls
  without sudo/runuser and strict current single-user option decoding.
  Validate Foundation ingress peer UID against certificate instance/agent/key/
  owner generations and recheck the fence inside authoritative transactions.
  New SSH account names use the shared agent-name normalization; existing
  reserved names/UIDs never change. The server supplies the approved username
  to the root helper, which rejects changed names and unmanaged account adoption.
  Resolve literal name mentions transactionally into stable recipient IDs;
  retain original bodies, exact-replay recipients and body-generation receipts.
  The root provisioner is a fixed local account operation surface, not a root
  command runner; the forced SSH gateway selects only API or join stdio.
  Root provision/disable RPCs carry positive identity key generation, retained
  with per-account inventory/tombstones. Test stale/same-generation-key-change
  rejection before OS commands, exact retries, disable-before-provision, fresh
  higher-generation UID/name retention and current registry/returned/post-bind
  tuple checks; do not add a generation override CLI or reset root inventory.
- Duplicate-owner tests must prove different live non-Founder owner keys and a
  fresh incumbent authenticated binary ping before the whole-identity ban.
  Same-key reconnect, dead owner, CLI traffic and unproved attempts must not
  ban. Founder permits independent device keys and multiple live owner sessions,
  with device-bound proofs/certificates, scoped revocation and one shared work
  slot. Test renewal/closure isolation, original-computer revocation, per-device
  account generations, SQL6 preservation and all-device recovery/quarantine.
  Recovery rotates keys/generations explicitly and cannot revive old
  certificates or work authority. Software/RAM cloning and sequential use
  remain possible; optional future native hardware signing is not implemented.

## Documentation and skills are runtime assets

Root Markdown and `.agents/skills/*/SKILL.md` are canonical, not copies generated
from a second repository. The embedded catalog records source/version and file
hashes. Every native archive and Debian role package includes the same guide.

Use `agent-cli guide list/show/install` without network or enrollment. Worker
initialization/startup installs a managed guide in the actual account's HOME and
links the skills into both `.agents/skills` and `.copilot/skills`. Existing home
instructions and unrelated skills must survive. The shared manual/daemon prompt
explicitly includes bounded operating guidance because provider discovery is
not assumed when custom instructions/tools are disabled.

When changing guidance, test frontmatter, links, command coverage, private-home
preservation, immutable guide verification, release contents and prompt delivery.
Do not put a real token, private path, cloud key, deployment inventory, provider
transcript or user-specific authorization into shipped documentation.

## Pull requests and release evidence

PRs into `main` or `release` run separate static, unit, integration,
native-binary end-to-end and stress jobs; Linux/macOS runs qualify their own
sources. Release PRs must pass this acceptance before merge; packaging is not
a substitute for those checks. Packaging runs only
for merged PRs into `release`, on the exact merge commit. Version changes must
produce new immutable releases.

The release jobs build all four binaries (`agent-cli`, `agent-service`,
`agent-identity`, `fdn-service`) twice, compare hashes, create native archives
and Linux Debian role packages, verify Debian executable/guide extraction and
include notices, guidance and checksums. The worker Debian payload includes
`agent-identity` alongside its runner/CLI; archive installer validation and
activation must require/link all four binaries. The operator independently obtains the manifest digest
from the trusted Actions result before deployment. Packages are unsigned and
unattested unless that policy is explicitly changed and implemented.

After every native target succeeds, the guarded publication job downloads
only that run's artifacts, verifies the exact source/version/checksums and
pre-merge release acceptance, then creates immutable `vVERSION` GitHub release
assets `fdn-VERSION-PLATFORM.zip`. It publishes the draft only after all assets
exist. Existing releases/tags are never overwritten or rebound. `tools/publish`
bundles verified artifacts and never rebuilds. The upgrade channel uses GitHub's
independently served asset digest, then every checksum in the contained manifest;
it has no fallback to expiring Actions artifacts or unsigned arbitrary URLs.

`agent-cli upgrade check/plan/apply/status` separates release discovery, exact
source approval and native OS effects. The API authenticates Founder and signs
bounded permits; each native helper needs separately administrator-provisioned
policy/operator SSH access and the pinned Foundation CA. A fresh nonce-bound
30-second start lease rechecks current parties, key generations, pause and work
before any stop; the accepted exact maintenance job then proceeds from its
durable receipt under the original 15-minute authorization. These are permission
signatures, not release code signing/attestation. Never extend the account
provisioner into a generic updater/root shell.

Operational execution requests are private message conversations with explicit
target decisions and progress/results. Upgrade submission queues exact permits;
it must never call remote prepare/install from the sender CLI. Native consumers
are separately supervised outside the services they stop; Foundation uses the
root-private deterministic handler, agents their own peer-checked inbox.
Preserve current authority/generation/project epochs, task approval/spending/
pause/session fences, exact prepared decisions and non-forcing insistence.
Keep immediate central safety/authority state and narrow auth/diagnostic controls.

Test helper process/filesystem/network effects through existing injected runners,
temporary homes and synthetic release bytes. No local test registers services,
uses cloud credentials, changes a production identity or invokes paid inference.

No hosted job result is implied by a passing local run. No model execution,
desktop control or privileged service registration is implied by fixture results.
