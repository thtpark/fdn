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

The existing compiled native binaries and pinned loopback Foundation TLS helper
start a Founder, a non-delegate assignee, and a delegate with separate HOME/state.
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

## Opt-in: live GitHub boundary only

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
