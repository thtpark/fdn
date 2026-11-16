# Standalone native software package

This source package implements general-product installation, not a build runner,
artifact custodian, authority server, inbox supervisor, or Foundation updater.
No function executes recipe steps, compiles on a target, invokes a shell, or
installs provider/plugin hooks. It uses the existing domain software DTOs.

## Exported contracts

```go
CanonicalManifest(domain.SoftwareManifest) ([]byte, error)
ValidateManifest(domain.SoftwareManifest) error
SignRelease(domain.SoftwareManifest, privatePEM []byte) (keyID, signature string, err error)
VerifyRelease(domain.SoftwareRelease, publicPEM []byte) error
ValidateProposal(domain.SoftwareProposal) error
SignPermit(domain.SoftwarePermit, caPrivatePEM []byte) (string, error)
VerifyPermit(domain.SoftwarePermit, caPEM []byte, now time.Time) error
ValidateResult(domain.SoftwarePermit, domain.SoftwareResult) error
ValidateRecipe(domain.BuildRecipe) error

CreateBundle(io.Writer, root string, []domain.BuildOutput, epoch int64) ([]domain.SoftwarePayload, error)
InspectBundle(data []byte) ([]domain.SoftwarePayload, error)
InspectBundleReader(io.Reader, compressedLimit int64) ([]domain.SoftwarePayload, error)
VerifyPayloads([]domain.SoftwarePayload, domain.SoftwarePlatform) error
ExtractBundle(data []byte, destination string, expected []domain.SoftwarePayload) error
ExtractBundleReader(io.Reader, emptyRoot string, expected []domain.SoftwarePayload, compressedLimit int64) error

ReadPolicy(filename string) (Policy, error)
(Policy).Validate() error
NewNative(Policy) *Native
(*Native).Handle(context.Context, domain.SoftwareNativeRequest) (domain.SoftwareNativeReply, error)
(*Native).PendingResults() ([]domain.SoftwareResult, error)
(*Native).AcknowledgeResult(permitID, permitSHA256 string) error
```

`Native` exposes `Policy`, `Runner process.Runner`,
`Fetch func(context.Context, artifactID, outputPath string) error`,
`Lease func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error)`,
and `Now func() time.Time`. Fetch and Lease must be explicitly supplied; there is
no unauthenticated URL, CLI, SSH, shell, or inference fallback.

### Distribution and authorization

The distribution signer takes a single PKCS8 Ed25519 `PRIVATE KEY` PEM block;
verification takes a single PKIX Ed25519 `PUBLIC KEY` PEM block. The key ID is
lowercase SHA-256 of the public SPKI DER. Signatures use canonical raw-standard
base64. Canonical manifest JSON sorts platform, payload, and dependency
inventories without mutating the caller.

Release signatures use `foundation.software.distribution-release.v1` plus NUL;
permit signatures use the distinct
`foundation.native-software.authorization.v1` plus NUL. Authorization pins a
currently valid Ed25519 Foundation CA certificate, not a distribution key.
Native enrollment refuses reuse of the CA public key as the distribution key.
`VerifyPermit` verifies authorization; callers still must independently verify
the distribution signature with `VerifyRelease`.

Permits and their native challenges have at most 900 seconds of admission
authority. Original challenge expiry remains enforced even if a permit expires
later. Immediately before admission, a new unpredictable nonce and
SHA-256 of the **original permit signature** bind the existing
`upgrade.SignLease`/`upgrade.VerifyLease` 30-second CA proof. Requests have a
10-second monotonic bound. Inspection cannot consume a lease and still start
effects after its expiry. Already admitted fixed maintenance finishes without
renewing authorization; no interrupted external effect is automatically retried.

### Bundles and build-side extraction

Bundles are deterministic gzip/USTAR with explicit modes and relative regular
files only. Compressed bytes and total file bytes are each limited to 256 MiB;
there are at most 4,096 payloads across at most 16 native manifest platforms.
Paths, depth, metadata, permissions, duplicate/file-parent collisions, CRC,
trailing data, concatenated streams, sparse/extensions, symlinks, hard links,
and special nodes are checked. Only owner-readable modes without group/other
write or special bits are admitted.

`CreateBundle` requires a real protected root and paths: all ancestors must be
owned by the current account or root, without symlinks or group/other write.
Canonicalize a known legitimate OS path before calling; do not dereference
untrusted payload paths. Writer ownership remains with the caller.
`InspectBundle` checks complete bytes but does not check an artifact ID/digest;
the custodian must compare its compressed SHA-256 and size with the signed
platform. `VerifyPayloads` checks the complete exact signed inventory.

`ExtractBundle` creates a new owner-only destination and rejects **every**
existing destination, including an empty directory, file, or symlink. Parents
must be protected real paths. It never overwrites or follows links. The
streaming counterpart requires an already-created empty protected 0700 root.
On failure, unactivated evidence remains in that exact destination; there is no
implicit cleanup, adoption, or replay. These helpers can stage source-recipe
pinned dependencies in an owned builder workspace without native installation.

## Native policy

All paths are clean absolute non-root paths, with protected real ancestors.
`StateDir` must already be provisioned 0700 and owned by the receiver. Native
policy files reject unknown fields/trailing JSON and bind the originally read
bytes and parsed capability throughout resident use.

Required public fields:

| Fields | Meaning |
| --- | --- |
| `FormatVersion` | 1; omitted/zero normalizes to 1 |
| `InstanceID`, `TargetID`, `BindingID`, `Generation` | Exact enrolled computer; positive generation |
| `ProductID`, `Kind`, `Platform` | Exact product and Linux/macOS amd64/arm64 platform |
| `User`, `UID`, `Home` | Existing non-root installing account, actual UID and passwd HOME |
| `PublicKeyFile`, `CAFile` | Separate distribution public key and Foundation CA pins |
| `StateDir`, `Prefix`, `CurrentReceipt` | Evidence separate from immutable activation; receipt defaults to `StateDir/current.json` and cannot be redirected |
| `Method`, `Manager` | Fixed native profile below |
| `BackupRecipient`, `BackupPaths` | Pinned public X25519 age recipient and up to 16 exact existing application data paths |
| `Service`, `Executable`, `Args` | Optional fixed service name, relative signed executable and up to 32 literal arguments |
| `Package`, `PackageFile`, `PackageRoot`, `AllowPrivileged` | Debian-only capability |
| `Adoption`, `Current`, `Dependencies` | Optional explicit baseline/receipt enrollment and exact dependency receipt anchors |

* **archive**: current installing user only, inside actual HOME, manager `none`,
  no service/process hooks. There is no authority to discover or kill arbitrary
  running portable programs. Explicit data snapshots must remain stable.
* **user-service**: current installing user only, inside HOME; `systemd-user`
  on Linux with that user's existing session bus, or `launchd` on macOS.
  Native writes only a fixed generated user unit/plist. The policy's relative
  executable must be a signed executable payload. Unknown definitions, overrides,
  live PID replacements, or unavailable managers fail closed. Existing inactive
  and disabled intent is preserved; no root, runuser, or sudo is used.
* **debian**: actual Linux root and an independently root-provisioned policy
  loaded by `ReadPolicy`, with `AllowPrivileged`, exact `Package` and single
  signed `PackageFile`. `Prefix` and distinct `PackageRoot` are isolated
  application directories beneath `/opt`, never dpkg/OS configuration roots.
  Fixed dpkg-deb inspection verifies package/version/architecture and bounded
  root-owned regular data under only `PackageRoot`. Downloaded or incumbent
  maintainer scripts, triggers, conffiles, dependency/package replacement hooks,
  alternate roots, force flags, and dpkg invocation hooks are refused.
  Fixed `dpkg --no-triggers --install` has no apt/download fallback.
  Optional systemd service uses the exact policy service name and executes as
  the non-root installing UID with `NoNewPrivileges`, never as root.

`Adoption *Baseline` pins a signed release and separate exact existing `Path`.
Debian adoption also pins local signed `PackageFile` bytes and registered
package state. Original unmanaged bytes remain; only the explicitly approved
fixed service definition may be changed. `Current *Baseline` is explicit
managed-policy enrollment and requires `Path == Prefix`, exact native
`ReceiptFile` and its SHA-256. There is no implicit legacy migration.
`DependencyPolicy` pins `Reference`, `Prefix`, `ReceiptFile`, `ReceiptSHA256`,
and `PolicySHA256`; prerequisites are verified, never installed automatically.

## Requests, receipts, and replay

Native request actions are `inspect`, `challenge`, `prepare`, `apply`, and
`status`. Every request has an operation ID. Prepare/apply alone carry a permit,
and their request ID must equal `permit.Proposal.Challenge.ID`, **not permit ID**.
`ProductID` is optional but, when present, must match this policy.

Inspection reports `observed`; challenges report `planned` with current
`absent`, `managed`, or `adoptable` state. An exact challenge replay retains its
nonce/expiry and cannot bind a changed source, native PID, receipt, or policy.
Prepare stages and verifies all bytes before any stop. New installation proves
absence. Managed upgrades strictly advance product revision without changing
profile; same-revision adoption requires the identical signed baseline.

Activation creates a sealed side-by-side directory under `Prefix/versions`
and an atomic relative `current` symlink. Effective file modes are declared
modes with write bits removed; version directories are 0555. Old versions are
never rewritten. Debian package-manager ownership remains separate from these
immutable directories.

Cold snapshots are gzip/tar encrypted directly to the pinned off-host public
age recipient. Target code never generates or receives its private key. Required
files, original current receipt, activation metadata, and stable application
bytes cannot be silently omitted. Symlinks/hard links/special nodes in backup
data are refused. Snapshots are bounded to 1 GiB/32,768 nodes.

Durable private jobs, prepared proofs, encrypted backups, `Receipt`, current
receipt pointer, permit references, and result outbox preserve exact source and
native uncertainty. Final result states are `installed`, `failed`, and
`operator-required`. The full verified receipt is the metadata-only recovery
commit point. Its replay never fetches, obtains another lease, invokes a manager,
or installs/rolls back a newer current version. An interrupted phase without that
proof is operator-required even after expiry. Acknowledgement marks only exact
result delivery; it never deletes evidence or clears an execution fence.

## Foundation boundary and evidence limits

Release/permit validation recognizes reserved `Kind == "foundation"` binding
`TargetID == InstanceID`, empty binding, generation 1. **Native policy enrollment
for Foundation is explicitly refused with a prerequisite error.** The parent
must route that product through its existing separately provisioned Foundation
updater bridge, including root-only inbox/leases, fixed Foundation/paired worker
services, server-first quiescence, and complete server/file-store backups.
There is no successful-looking fallback or generic root callback here.

`Result.ResumeSafe` is always false. The parent receiver must independently
establish unchanged Foundation work/session/report fences before requesting any
prior-AI restoration. Native byte/lifecycle verification alone does not prove it.

Tests use real project-local disposable files, independent synthetic keys and
archives, fake fixed managers, and synthetic privileged host context. They
perform no actual service registration, dpkg installation, root operation,
provider call, or target build. Passing tests is source-bound package evidence,
not live Linux/macOS/Debian qualification, off-host restore acceptance, or
production/security certification.
