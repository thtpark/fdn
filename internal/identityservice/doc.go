// Package identityservice owns a computer's Foundation signing keys in a
// dedicated, non-root OS account, separate from the provider/tool-runner UID.
// It does not launch providers or expose identity memory, private keys, signing
// operations, certificate issuance, or transport control to worker IPC clients.
//
// The native entrypoint is agent-identity --config PRIVATE_CONFIG, followed by:
//
//   - prepare --name NAME --output PUBLIC_FILE: offline exact-retry creation of
//     one SSH/auth device; only domain.PublicIdentity is exported.
//   - connect --profile APPROVED_PUBLIC_PROFILE: offline pinning of public CA,
//     SSH host keys and this device's assigned account, followed by signed
//     challenge activation over fdn-join. There is no bearer credential.
//   - serve: private state locking, device-mTLS owner WebSocket acquisition,
//     ephemeral Ed25519 owner proof, renewal and an owner-mTLS fdn-api proxy.
//
// Offline operations and serving share an exclusive account-private lock. State
// and configuration are identity-UID-owned 0700/0600. The separate IPC directory
// is identity-owned 0710 with the worker group; its socket is 0660. Provision
// explicit identity-account membership in that worker group before serving;
// native startup verifies membership, never invokes a privileged chgrp helper.
// No reverse group membership exposes identity state to the worker. Linux
// SO_PEERCRED and macOS LOCAL_PEERCRED must identify the configured worker UID
// and GID before any HTTP bytes are admitted. There is no runtime test-UID flag.
//
// GET /local/status returns public agent/identity and connection readiness;
// GET /local/profile returns public trust and identity only. Ordinary Foundation
// routes and Founder-authorized register/recover business operations are
// proxied with a positive path/method allowlist. Bearer and supplied identity
// headers are discarded. Activation/owner/certificate/local-control routes,
// unknown methods and noncanonical paths are denied before transport access.
//
// A fresh server challenge precedes every owner connection. Each certificate
// response supplies the next one-use renewal challenge. A continuous read loop
// answers WebSocket pings, allowing the server to prove a live conflicting
// owner rather than banning based on public metadata. The same in-memory owner
// key reconnects within one start, and each start creates a new owner key. Device
// certificates renew five minutes before expiry over limited fdn-join, then the
// owner WebSocket reconnects with that same in-memory key. Owner private keys,
// proofs and certificates are never written to state or returned over IPC.
//
// Confirmed revocation/duplicate bans retain fatal status and return permission
// denied, not unavailable, so worker execution guards stop owned work while
// preserving journals. Losing the owner channel closes this service's active
// upstream streams; loss of connectivity cannot extend the last owner
// certificate's authority past its expiry. There is no fallback to device-mTLS API access, a bearer,
// another identity, a local TCP listener or an unrelated SSH process.
//
// Remote, Cryptography and Boundary support deterministic rootless fixtures.
// Native commands always choose NativeBoundary. Native service/account planning
// is in platform.RenderIdentityServices and performs no host changes.
package identityservice
