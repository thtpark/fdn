---
name: foundation-develop
description: "Use when developing Foundation Go code, extending its CLI/API/schema/providers, updating documentation or skills, validating local tests, reviewing migration/recovery behavior, or preparing a source-bound main/release pull request."
---

# Develop using this repository only

Read `guide show DEVELOPMENT.md`, `guide show SCHEMA.md`,
`guide show TEST_PLAN.md` and `guide show IMPLEMENTATION_PLAN.md`.
The checkout's root documents are canonical; no old source tree is required.

1. Establish the current request/contract, source state and related tests.
   Preserve user changes, operational identities and retained evidence.
2. Find existing helpers first. Keep server authority transactional, DTO/input
   validation narrow, errors explicit, and transport/provider/native boundaries
   testable. Do not enforce security only in a friendly CLI.
3. Investigate every affected surface: domain models, API/client, CLI help/input/
   exit shape, server policy, local journal/controller/manual path, providers/
   cancellation, native deployment, embedded docs/skills and packages.
4. Preserve exact operation replay, generation/epoch fences, completed-vs-done,
   creator/recipient ownership and the one-slot rule. New adapters must prove
   non-prompting coding approval and tool-disabled text calls before enabling.
5. Add ordered compatibility-tested migrations, never rewrite shipped schemas
   or reset state. Future schema versions fail closed. A binary rollback is not
   automatically a compatible database rollback.
6. Update command/reference/skill behavior together. Skills need matching names,
   meaningful trigger descriptions and complete current procedures. Documentation
   must distinguish implemented capability, planned work and evidence limits.
7. Use the smallest related tests, then `bash scripts/test.sh all`. Independent
   selectors are static, unit, integration, e2e and stress. Tests use isolated
   state/HOME/ports and deterministic providers; no VM, container, root/host
   service registration, real credentials or paid inference.
8. For guidance changes, verify frontmatter, links, CLI coverage, embedded
   catalog/source hashes, preservation of home instructions/unrelated skills,
   both discovery locations, explicit prompt context and package payloads.
9. PRs into main run separate Linux/macOS suites. Real packaging only follows
   an authorized PR merge into release on the exact source commit. Never
   fabricate a local Actions event or build/test on a deployment target.
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
