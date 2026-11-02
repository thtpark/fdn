// Package filestore stores Foundation upload bytes separately from database
// metadata. A store uses an owned, real 0711 root and one 0711 directory per
// immutable file ID. Filenames retain their original safe single-component name.
// Neither directories nor files permit group or world enumeration or writes.
// On macOS only the verified root-owned /var -> private/var system alias is
// normalized; symlinks in application-controlled path components are rejected.
//
// The caller owns authorization, committed upload progress, hashes, alignment,
// publication and receipts. WriteChunk appends at that committed prefix, resumes
// a matching partial uncommitted tail, and syncs before returning. It never
// overwrites committed bytes. Database replay of older committed chunks belongs
// to the caller, not WriteChunk.
//
// Permissions are a projection of authoritative readers, not authorization.
// SetReaders replaces that projection; callers can revoke all readers before a
// database change and apply the exact committed readers afterward. Custom
// Permissions receive a process-local descriptor path pinned for the duration
// of Apply, not a path to retain or pass to another process. NativePermissions
// operates on the pinned descriptor directly. Native grants fail unless every
// ancestor provably permits traversal, without assuming group memberships or
// broadening private directories. PermissionFunc fixtures bypass this native
// qualification, not the store's owned-resource checks.
//
// Store operations serialize within one Store and detect replacement of resources
// already observed by that Store. A newly opened Store validates existing owned
// resources; persistent identity, immutable IDs and cross-process transaction
// serialization remain the caller's responsibility. The service OS account is
// trusted: Unix permissions do not isolate processes sharing that account.
package filestore
