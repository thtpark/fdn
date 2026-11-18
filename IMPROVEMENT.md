# Internal contribution, tools and software delivery

Foundation coordinates internal feature development and repair through its
existing tasks and registered computers. It does not rewrite a running
executable or grant a model a privileged repair shell.

This guide covers **new tools and existing tools**, as well as Foundation
itself. Read [COMMANDS.md](COMMANDS.md) for inputs,
[SCHEMA.md](SCHEMA.md) for authority, and [DEPLOYMENT.md](DEPLOYMENT.md) before
provisioning native installation policies.

## The workflow

```text
Feature request / enhancement / bug / eligible health incident
  -> improvement request in the owning project
  -> delegate triage and ordinary task assignment
  -> exact independent task approval
  -> source branch, implementation, tests and internal push
  -> independent review and exact integration
  -> source-pinned build tasks
  -> verified immutable internal release
  -> exact Founder-approved native rollout
  -> installed-byte and consumer/health verification
```

Upstream PR contribution is optional and separate. Founder can perform it, or
authorize any agent with that computer's own GitHub credentials. Internal
integration/release approval is not permission to publish externally.
Do not push private ancestors, messages or credentials along with a clean diff.
The official GitHub release branch still requires its normal reviewed PR and
passing checks; this feature does not bypass that policy.

## Who does what

| Action | Authority |
| --- | --- |
| Submit a feature, bug or tool request | Any active enrolled agent, with safe shared text |
| Register a repository/product or configure maintenance | Founder |
| Triage, assign, approve and independently review ordinary improvement tasks | Founder or current delegates of that exact project |
| Write/push a work branch | Its assigned approved task and current coding run |
| Integrate a contribution | An independent current verifier, exact source/result/base |
| Publish an internal release | Current project authority, independently accepted build tasks, separate distribution signer |
| Plan/apply installation | Founder plus independently provisioned native target policy |
| Enter protected credentials, change OS privilege, spend or publish upstream | Their separate explicit authority; task text is insufficient |

Building, downloading, signing, queued installation and verified installation
are different states. A task marked `done` certifies only its own approved
acceptance criteria. A source-only task does not certify fleet deployment.

## The built-in Foundation project

`project_foundation` is reserved for Foundation platform features and repairs.
Its display name is **Foundation**, and its owner is the actual Founder actor.
An existing ordinary project with that name is not adopted or renamed.

Founder initializes it when configuring improvement policy. It uses ordinary
project delegates and independent readiness/verification, **not Adhoc
self-approval**. Initializing it starts no provider, build, repository import,
native installation or new execution slot.

Unrelated tools/features stay in their owning projects. When an ordinary task
reveals a platform gap, submit a related Foundation request and continue
eligible original work. A genuine blocker remains recorded on that original
task. Related references do not reparent work, transfer project authority,
approve a retry, or mark the original task done.

Project separation is organization and authority, not private readership.
Shared request/task/note text must not contain private diagnostics, raw
transcripts, credentials, workspace paths or private source excerpts.
Repositories and artifact bytes have their own current reader checks.

## Configure bounded improvement capacity

Improvement admission is disabled until Founder explicitly configures it.
Example policy:

```json
{
  "enabled": true,
  "project_ids": [],
  "window_seconds": 3600,
  "slice_seconds": 300,
  "per_agent_seconds": 600,
  "fleet_seconds": 1800,
  "reason": "Reserve bounded platform improvement capacity while ordinary work remains primary"
}
```

```sh
agent-cli improvement policy
agent-cli --operation-id configure-improvement-001 improvement configure --input policy.json
agent-cli project show project_foundation
agent-cli project delegate project_foundation --input delegate.json
agent-cli improvement usage
```

Use `expected_revision` from the current policy for changes (0 before initial
configuration). Additional `project_ids` opt other internal-tool projects into
the **same aggregate allowance**; each tool does not create its own budget.
The Foundation project is always classified as improvement work.

The allowance covers preparation, triage, coding, builds, review and rework.
Admission conservatively charges the full reserved upper bound. Finishing
early releases scheduling, not that charge; restart, new IDs and policy
revision changes do not refund it. Counters use server-clock windows.
An allowance is not permission to incur new provider/cloud costs.

From 0.3.48, exhausted/paced allowances retain a server-clock-relative work
deadline across restart. The next eligible window does not need a new message,
policy revision or service restart to wake the controller. An occupied slice
has a bounded eligibility recheck; no charge is refunded. A rejected
preparation reply fences its semantic source, not other eligible work.

Eligible improvement work uses spare capacity and a paced reserved share to
avoid starvation when ordinary work stays ready. Both kinds of work use the
same serial slot. Native cancellation, checkpoints, pending reports, uncertain
effects and Founder pauses remain enforced. A safe switch can be delayed by
an actual cleanup fence; never claim instantaneous cancellation.

Disabling policy fences new improvement admission and stops admitted runs
through existing guards. It does not erase history, reset retries, change
global modes, or release unproved work.

## Submit and triage requests

Request kinds are `feature_request`, `enhancement`, `bug_report`,
`incident_repair`, and `maintenance`. A feature needs no incident.

```json
{
  "project_id": "project_foundation",
  "kind": "feature_request",
  "title": "Add a reusable validation tool",
  "description": "Provide a native tool that validates the approved internal data format.",
  "acceptance_criteria": "Valid inputs succeed; malformed inputs fail explicitly; an independent consumer checks actual output and exit codes.",
  "related_task_id": "TASK_ID_IF_APPLICABLE"
}
```

```sh
agent-cli improvement create --input request.json
agent-cli improvement list --project project_foundation --state proposed
agent-cli improvement show IMPROVEMENT_ID
agent-cli improvement decide IMPROVEMENT_ID --input decision.json
```

A decision includes `expected_revision`, `decision`, and `reason`.
`accept` additionally names `assignee_id`; optional `source` binds an existing
owned repository/commit. It creates an ordinary unapproved task.
`defer`/`decline` assign no work. `duplicate` names an original request in the
same owning project. Exact replay never creates a second task.

In `ai`, current project delegates can triage and review readiness under the
configured allowance. Unclear requirements remain feedback/blockers, not
invented acceptance. Missing independent authority or capacity stays visible.
Eligible server health episodes may create one source-bound proposal; this
starts no inference, reset, login, retry or installation.

### Current clarification and terminal-task attention

Preparation remains tool-disabled. Its derived clarification review names only
actual same-task answers from requested respondents to current questions.
`task.waiting` remains recorded until the assignee reviews those answers; its
presence alone does not mean they are missing. A clear review must identify
every current answer in `answer_ids`; with no current clarification, that list
is empty. Historical questions, missing optional model catalogs and the absence
of a coding claim must not invent a new scope blocker. This review never grants
readiness: the controller checks current independent approval separately.
Malformed fully returned, unapplied replies retain their original bytes and
source fence before any task mutation. Older possibly applied decisions still
need exact operator recovery; they are not automatically adopted as unapplied.

On terminal or archived tasks, implicit attention caused only by the old
assignment is durably deferred without a provider call. The receipt pins the
observed task revision and comment generation. An explicit `@{AGENT_ID}` mention
still requests a normal response, including a deliberate post-completion
follow-up; it never reopens execution or bypasses a retained work fence.

## New and existing tools

Use one product/catalog and contribution/release workflow:

| Case | Path |
| --- | --- |
| New internal tool | Record the need; check reuse/extension first; assign an owning project; approve source work and registration; implement, test, document and release |
| Existing internal source | Verify owner/source/rights; import a clean approved baseline; preserve local edits/installations; improve through ordinary tasks |
| Authorized internal fork | Keep origin/license notices and a distinct internal product identity |
| Vendor-managed binary | Observe and request updates through its existing trusted mechanism; do not invent internally built provenance |
| Unknown owner/source | Leave observed/unmanaged until authority and lineage are resolved |

Kinds include `foundation`, `cli`, `script`, `library`, `service`, and
`application`. Libraries/scripts can be tested and packaged without compiling.
Library dependency changes belong to their consuming projects, not a global
executable installer.

```sh
agent-cli software register --input product.json
agent-cli software list --project PROJECT_ID
agent-cli software show PRODUCT_ID
agent-cli software observe --input observation.json
agent-cli software inventory
agent-cli software bind-consumer --input dependency.json
agent-cli software consumers --project PROJECT_ID
```

Registration declares owner/project, kind, source provenance, repository,
tracked recipe path (or platform recipe map), capabilities and redistribution
rights. Capabilities advertise interfaces, not tool grants.
Observations cannot claim `managed`: that state needs a native install receipt.
PATH presence does not prove ownership, permission, usability or source.
Do not probe every discovered executable, import private source automatically,
start provider authentication, enable plugins, or take over an OS manager.

## Hosted Git

Foundation stores immutable, verified self-contained Git bundles and
transactional ref metadata. It does not expose a shell, raw database writes,
peer file transfer or a general Git daemon beside its private keys.

Founder creates/imports a repository from a private retained upload:

```sh
agent-cli repository bundle --workspace WORKSPACE_REF --ref refs/heads/main --output initial.bundle
agent-cli file upload --path initial.bundle
agent-cli repository create --input repository.json
agent-cli repository link
git clone fdn::REPOSITORY_ID
```

The helper uses this computer's existing identity proxy. Place its managed
launcher on PATH as described by the normal per-user installation guide.
Unmanaged helper collisions are errors, never overwrite permission.
From 0.3.47 it accepts Git's canonical scheme-stripped repository argument;
the external clone URL remains exactly `fdn::REPOSITORY_ID`, without doubling
the scheme. Explicit direct CLI helper invocation may also use the full URL.

Set task `source` through a current project-authorized task change:

```json
{
  "kind": "source",
  "reason": "Bind the exact internal contribution",
  "source": {
    "repository_id": "REPOSITORY_ID",
    "commit": "EXACT_COMMIT",
    "ref": "refs/heads/work/AGENT_ID/feature"
  }
}
```

Execution prepares an owned source workspace without replacing existing work.
Ownership lives privately in `.git/foundation/source`, outside build inputs.
An exact owned legacy root marker is validated against its repository, commit,
work ref and real Git checkout before migration; its original bytes remain in
`.git/foundation/legacy-source`. Missing, conflicting or foreign ownership and
incomplete checkouts stay fenced, without replacing user files.
Coding children receive local `FDN_WORK_TASK`/`FDN_WORK_RUN`; these identify
the existing claim, not another identity or authority grant.
Push exactly the approved work ref:

```sh
git push origin HEAD:refs/heads/work/AGENT_ID/feature
```

Protected default refs cannot be pushed directly; force/delete and foreign
task pushes are rejected. Push journals retain exact old/new refs and upload
bytes through uncertainty. Do not invent new operations to reset a failure.

An independent verifier checks the exact reported source and submits
`repository merge REPOSITORY_ID --input merge.json` under its current
verification run. It binds task/spec/result/run, source ref/head, base,
candidate commit and candidate bundle. Only an exact reviewed fast-forward
or two-parent candidate may advance integration. Base/head drift fails.
Acceptance requires actual integration, not a success sentence or PR URL.

Limits: 64 MiB packed bundle, 256 MiB expanded objects, 65,536 objects,
128 refs, and 1 GiB retained bundles per repository. Prerequisites, filtered
bundles, unsupported object formats, annotated-tag refs and gitlinks are
rejected. These limits are not fleet-capacity qualification.

## Builds and immutable releases

An approved product recipe is a tracked JSON file at its exact source commit:

```json
{
  "format_version": 1,
  "version": "1.0.0",
  "platform": "linux-amd64",
  "timeout_seconds": 300,
  "profile": "archive",
  "steps": [{"args": ["go", "build", "-trimpath", "-o", "bin/tool", "./cmd/tool"]}],
  "outputs": [{"path": "bin/tool", "mode": 493}],
  "dependencies": []
}
```

Mode 493 is decimal 0755. Use 420 for 0644. Recipes execute under the existing
installing user and approved workspace; they are not root installation hooks.
Use actual tests as recipe steps and explicit outputs. Outputs must not
overwrite tracked or unexplained existing files.

```sh
agent-cli build create --input build-task.json
agent-cli build show BUILD_ID
# Inside that admitted coding task:
agent-cli build run BUILD_ID
agent-cli build download BUILD_ID --output review-build.tar.gz
agent-cli release create --input release.json
agent-cli release show RELEASE_ID
```

Build creation binds an existing task/product/platform to the source recipe
and invalidates prior readiness. Its result must be uploaded and natively
checked before task completion. An interrupted running build is uncertain,
not automatic compiler retry. Lost result responses reuse compiled bytes.
Successful artifact submission is separate from the provider's final task report.
After native cleanup and remote custody have been inspected, only a fresh normally
authorized execution of the unchanged task/specification/source/product/builder
may reuse an already submitted build. `build run` checks the retained original
run and immutable bytes before returning without compilation, upload or a new
build journal. Completion records the current report run; the build's original
run, builder, artifact and payloads remain unchanged. This grants no retry,
quota refund or additional slot. Missing/unverified custody and unknown partial
outputs remain blockers; preserve their journals rather than adopting them.
Stopped unsafe attempts still require exact prior-run/next-attempt retry approval.
For deadline-bearing runs, `build run` checks the exact admitted reservation using
fresh server-observed time and a conservative request-start monotonic deadline,
not the builder's wall clock.
Native steps retain bounded owner-only stdout/stderr and step/result metadata
in private worker build evidence, bound to the exact build/run/step reference
reported on failure. Overflow, persistence and process cleanup errors fail the
build; partial evidence is not success. Inspect retained evidence locally;
never publish raw logs or clear a running journal to repeat a compiler.
Independent reviewers can download submitted custody bytes before release
publication with `build download`; this verifies hashes and syncs an owner-only
new file, but does not accept a task. On failure, partial bytes remain explicit
evidence; inspect them and choose a new destination rather than overwriting.

The tracked Foundation recipes are:

- `software/recipes/foundation-linux-amd64.json`: the Foundation server Debian
  payload, one exact role package.
- `software/recipes/agent-linux-amd64.json`: the Agent Debian payload, separately
  registered in its Agent-core product.
- `software/recipes/foundation-darwin-arm64.json`: all four core executables and
  license notices; the complete version-matched guide is embedded in the binaries.

They use `go run ./tools/package --internal-platform PLATFORM`, which requires a
clean exact source checkout and compares two native binary builds. This creates
unsigned task payloads, not an official GitHub publication or an installation.
The ordinary packager still requires its exact merged release event.
Register separate Foundation-server and Agent-core products/releases at the
same reviewed source so existing fixed-role Debian receivers need no exception.
Verify the server release before applying the dependent Agent release.

Both core products use manifest kind `foundation`; this is a product family,
not the target's native role. From 0.3.49, only a challenge targeting the exact
Foundation instance uses an empty enrolled binding and generation 1, and it
must carry a core release. Core Agent challenges retain their current enrolled
computer binding and positive generation, including independent Founder devices.
Generic tools cannot target the Foundation instance. Native role/product,
source, policy, signature, nonce, backup and expiry checks still apply.

Builds are native-host-only by default. A Founder-registered product can opt into
one exact pair with the capability label
`cross-build:linux-amd64:darwin-arm64`. The build still requires its actual
approved task/run/workspace, and records the real build host rather than claiming
native target testing. Before accepting a cross-build, an independent verifier
must exercise its unchanged payload on the target OS and check version/source,
guide, executable architecture and representative consumer behavior. The label
is no execution, installation, spending or OS-permission grant.

All declared platforms must have independently accepted current builds before
publication. A builder cannot promote itself. Artifacts enter explicit
immutable registry custody: originator deletion, transfer cleanup or later
revocation cannot remove retained source/release bytes. Existing file grants
cannot broaden registry readership.

Internal manifests bind source, product, release revision, platform payloads,
recipes/dependencies and hashes. A separately provisioned distribution key
signs them; it must not reuse the enrollment/permission CA.
Signing proves publishing authority and bytes, not benign code, independent
build attestation or installation permission.

Consumers pin exact release/platform/digest and purpose (`build`, `runtime`,
`workspace`). Builds stage declared dependency bytes rather than resolve
`latest` during retained work. Verify actual CLI/API/library behavior and
representative consumer workflows.

## Plan and apply native installation

Installation remains opt-in. A native administrator provisions a fixed
product/target policy, trusted distribution public key, Foundation CA,
installation profile, lifecycle, backup paths and independent receiver.
Founder project authority does not provision OS privilege.

```sh
agent-cli software plan --input targets.json --output private-plan.json
agent-cli software status --input private-plan.json
agent-cli software apply --input private-plan.json
agent-cli software status --input private-plan.json
```

Targets identify the exact enrolled computer/binding (or this Foundation
instance), not a display name. The plan pins native observed current
installation/absence, generation, policy, release and archive digests.
Retain the plan and its `.agekey` privately and off targets.

Pending planning is not a fresh observation. Status submits no new controls.
Resume planning with the original input/output/operation/key. Apply records
an exact temporary pause and queues one dependent target at a time.
Only an actual verified predecessor permits the next submission.
Queued or staged is not installed.

Native receivers stage/verify bytes before stop, obtain a fresh nonce-bound
start lease, preserve cold encrypted recovery evidence, and perform only fixed
policy operations. They never compile source. Existing-tool adoption requires
its verified baseline and explicit policy; unknown installations are not
treated as absent or overwritten.

Preserve current modes, identities, provider authentication, user data,
workspaces, journals/outbox and uncertainty. Restore only unchanged prior
AI intent after exact verified results and independent fences permit.
Newer/manual Founder choices win. Installation success remains recorded when
automation resumption is blocked.

Expired or changed-source authorization fails before native admission.
Uncertain native effects require original status/receipts, not another apply,
new permit, force clear or blind rollback. A binary rollback is not a database
rollback. Stage a Foundation release before stopping the Foundation that
hosts its artifacts; retain an independent operator recovery path.

From 0.3.46, paired Foundation/Agent `prepared` records a successfully admitted
stop and encrypted cold backup, unlike generic tool payload staging. A long
backup may cross the admission deadline; only that exact original job may
finish, after current signature/CA/policy and actual private backup-byte checks.
No new permission, lease, stop or backup is obtained. Uncertain phases cannot
use this completion path, and generic tool first admission remains expiry-bound.

### Failed paired-Agent backup before installation

From 0.3.50, each paired native backup retains owner-only `backup-command`
metadata, its actual started PID, bounded stderr (8 KiB), exit/cancellation and
process-join outcome, plus final stream/persistence outcome. Overflow and failed
evidence persistence remain failures. Inspect these private files locally; never
publish raw diagnostics or call a partial encrypted archive a verified backup.

An explicitly authorized native operator can abort only an exact failed Agent
`stopping` attempt before installation:

```sh
agent-service --operation-id recovery-original-001 software-recover \
  --updater-policy /APPROVED/updater.json --input inspected-recovery.json
```

The private input identifies `id` (original native challenge/job ID),
`expected_journal_sha256`, `expected_permit_sha256`, and a non-secret `reason`.
The original receiver and worker must be joined. Only the original baseline
identity may be restored, with explicit Founder/operator authority, to prove
the current enrolled generation and report the failed outcome; this is not
permission to start work or bypass the pause.

Recovery checks the existing native locks, exact protected journal/permit,
current pinned CA and distribution signature, policy/product/target, current
enrollment, unchanged paired executable baseline, manual mode, empty reports
and invocations, inactive session fence and joined daemon/work locks. It refuses
prepared, installing, verified, unknown, foreign or replaced resources. A
prepared valid cold backup still uses its original normal completion path.

The operation preserves the original journal and partial archive, records a
source-bound failed pre-install outcome and reports it through the target's own
authenticated mailbox. It never installs, repeats a stop/backup, starts services,
resumes automation, renews permission or clears an application/session fence.
The partial archive digest is evidence of retained bytes, not backup validity.
Exact operation replay settles only that same outcome, including a lost reply.
Fresh diagnostics and a separately authorized new native plan are required
before another installation attempt. Native operator access is not independent
project authority; a recovery helper must be explicitly authorized and verified
against its actual reviewed source, never relabelled as a published release.

## Releases, backups and evidence limits

Official GitHub releases include companion source archive, Git bundle and
source manifest, alongside unchanged platform ZIPs. Their source identity and
asset digests are verified by the guarded publisher. Source delivery is not an
offline Go/toolchain dependency cache.

Hosted source and release bytes use the existing Foundation file store and
snapshot/rehydration machinery; registry metadata is in ordered server SQL12.
Native backup policies must cover the actual file store and separately
provisioned signing/recovery material. Restoring identities still quarantines
old keys and never revives old install permits or automatically resumes work.

Local scenarios use disposable files, real Git and deterministic native/provider
fixtures. They do not certify live provider quality, privileged Linux/macOS
services, multi-UID host isolation, a production restore, notarization,
hardware attestation or a 1,000-agent fleet.
