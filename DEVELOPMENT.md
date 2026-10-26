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
| Native deployment | `internal/platform/`, `cmd/agent-identity/`, `internal/distribution/`, `tools/package/`, `scripts/` | Exact preprovisioned two-UID service plan/install/status, fixed native definitions/privilege drops, recording lifecycle tests, deterministic packages and trusted installation |
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

The controller owns execution/verification admission and reports. A provider
adapter supplies structured observations/results; it does not receive authority
to accept its own work or start another AI worker. Independent delegated review
uses the same durable coding journal/claim/report/outbox path, its own local
provider/workspace, and current server-bound result/discussion/project fences.
New scenarios should extend deterministic test-provider plans and isolated Git/
Git-host fixtures rather than add task-title, provider or repository branches
to production policy. All providers must prove non-prompting local
approval behavior before enabling coding; text calls must remain tool-disabled.

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

Server, local-worker and guidance formats have separate responsibilities.
`PRAGMA user_version` tracks each SQLite schema. The current API/schema contract
version is **2**; native message protocol is **2**, server SQLite migration is
**4**, local SQLite remains **1**.
Read [SCHEMA.md](SCHEMA.md) before changing either database.

- Add ordered, idempotence-tested migrations; do not rewrite a shipped migration.
- Reject databases newer than the binary rather than resetting them. Restore
  recognizes manifest schema 1/2 separately from SQL4, rejects future formats,
  and quarantines all restored identity/owner/key epochs before publication.
  Test root-private sole-legacy-Founder adoption preserving ID/history, regular
  fresh-key reapproval, retired credential denial and unchanged mode/work fences.
- Test migration of representative prior state, unknown/future versions, crash
  recovery and reopen/integrity. Preserve existing identities, operation IDs,
  generations, private bindings, knowledge, journals and outbox.
- Do not assume binary rollback can read an upgraded schema. Define and verify
  a compatibility/restore path before deployment.
- Version an incompatible API/DTO and document upgrade order. Do not add a hidden
  success-shaped fallback for an older client. Migration 3 retains old agents,
  tasks and operation evidence but does not authorize their old bearer files.
  Founder explicitly binds the designated original non-Founder agent with
  `agent register --existing-agent`; the runner then uses `connect --migrate`.
  A legacy Founder needs the root-local recovery route before it can register
  others. Preserve worker SQLite 1, knowledge, journals, outbox and bindings.
- Keep long-term keys in the identity-service UID, never provider context or the
  runner HOME. Validate kernel peer UID against certificate instance/agent/key/
  owner generations and recheck the fence inside authoritative transactions.
  The root provisioner is a fixed local account operation surface, not a root
  command runner; the forced SSH gateway selects only API or join stdio.
  Root provision/disable RPCs carry positive identity key generation, retained
  with per-account inventory/tombstones. Test stale/same-generation-key-change
  rejection before OS commands, exact retries, disable-before-provision, fresh
  higher-generation UID/name retention and current registry/returned/post-bind
  tuple checks; do not add a generation override CLI or reset root inventory.
- Duplicate-owner tests must prove different live owner keys and a fresh
  incumbent authenticated binary ping before the whole-identity ban, including Founder.
  Same-key reconnect, dead owner, CLI traffic and unproved attempts must not
  ban. Recovery rotates keys/generations explicitly and cannot revive old
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

No hosted job result is implied by a passing local run. No model execution,
desktop control or privileged service registration is implied by fixture results.
