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
| `LIVE-001` | Separately opted-in real GitHub boundary check; excluded/skipped by default, not a passed live qualification |

This is a concrete catalog for the workflow introduced here, not an exhaustive
inventory of every test function in the repository. Other suite coverage remains
summarized below. New scenarios should add an explicit catalog entry alongside
their tests; planned/unimplemented cases must not be described as covered.

## Change-to-test map

| Change | Minimum related cases |
| --- | --- |
| DTO/input/CLI | Unknown/protected fields, bounded UTF-8 input, exact JSON envelope/exit code, every implemented leaf's offline help |
| Enrollment/grants | Expiry/replay, active sponsor/current authority, unique credential, Founder-only boundaries, revoked access |
| Tasks/policy | Named/independent eligibility, project epochs, invalidation, status gates, concurrent stale revisions, exact execution/verification claims/reports; verifier's current spec/result/discussion/epoch, independent own-provider selection, shared server slot, atomic feedback/rework approval, revoked authority/Founder pause and no self-acceptance |
| Discussions/messages | Creator-only edits, literal mention/body generation, requested answers, private recipients, no automatic protected replies |
| Persistence | Reopen/migration/future-schema rejection, transaction failures, immutable operation fingerprints, three-history bounds, integrity |
| Worker/controller | One slot across manual/service/session/execution/verification, onboarding once, no idle inference, partial results/questions, retry/backoff/budget, offline report replay and no duplicate inference/review/merge; prepared review reply, claim-ack recovery, uncertain verifier failure, discussion/mode/workspace cancellation, disabled-binding preservation and manual resolution |
| Providers/processes | Exact required flags, text tool isolation, prompt through stdin, private/bounded output, version/model pinning, cancellation of owned descendants, failed signed staging retains previous version; actual login PATH/bare command versions, profile preservation/collisions, current-version dispatch and real foreground-terminal input; missing-authentication to real Founder/delegate inbox before onboarding without inference, private/protected notice fields, exact replay across restart/lost ack, explicit pending failures, no notification spam or auth inferred from receipt acknowledgement |
| Sessions | Request != consent != explicit open, current scope/expiry/revocation, no public listener, restart retirement, cleanup fences |
| Autonomous control | Only Founder pauses/resumes; target/delegate/offline overrides denied; event-driven cancellation and server claim gate; version-1 server migration preserves identity; legacy unexplained manual pause fails closed |
| Persistent assistance | Only Founder issues/revokes; exact requester/target/purpose/mode/provider/project; no-project authentication bootstrap; target prepares unattended without local consent; disconnect/restart/expiry retains grant; role/identity/revocation closes streams; delegate is not assumed human |
| Native services | Recording adapters by default, raw scalar systemd paths, user-bus environment only for registration, Linux parser without starting a service |
| Guidance/docs | Valid skill names/frontmatter/descriptions; local links/command coverage; no legacy checkout dependency; hashes/version completeness; preserve home instructions/unrelated skills; prompt inclusion; isolated homes; native and Debian guide content |
| Release/install | Merged-release-only event/commit guards, pinned Actions, two matching builds, safe archive paths/types, Debian parent directories and real unprivileged extraction, trusted manifest, immutable/idempotent installation and state preservation |

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
