// Package gitrepo validates and reads immutable, self-contained SHA-1 Git
// bundles without subprocesses, filesystem access, Git configuration or hooks.
//
// Bundle versions 2 and 3 and pack versions 2 and 3 are supported. Prerequisites,
// filters, other capabilities, other object formats, preset zlib dictionaries,
// duplicate objects and submodule gitlinks are rejected. Every advertised
// reference must identify a commit, so annotated-tag references are not
// supported; lightweight tags are. HEAD is accepted as a bundle pseudo-reference.
//
// Input is limited to 64 MiB, reconstructed object contents and inflated pack
// representations are each limited to 256 MiB, and every pack entry counts
// toward the 65,536-object limit. There may be at most 128 references. Inflation
// and delta instructions are checked before allocating reconstructed contents.
// Trees have at most 65,536 entries each, with safe UTF-8 component names of
// at most 255 bytes, paths of at most 4,096 bytes and depth of at most 128.
// Commit/tag headers are limited to 64 KiB and commits to 1,024 parents.
// File reads return only regular files, never directories or symbolic links.
//
// Fingerprint adds a cached SHA-256 Merkle binding of actual commit bytes,
// complete tree/blob contents and ordered parent history. It is independent of
// pack compression/order, advertised refs and unrelated objects. Each stored
// object is hashed once; shared trees are not flattened into exponentially many
// paths. Contains distinguishes absent commit locators before ancestry queries.
//
// Object hashes, graph closure and source-bundle metadata are integrity checks,
// not project authority, approval, authorship, signature validity, execution
// permission or build safety. Callers must enforce those separately.
// SHA-1 is checked as required by this Git format, without SHA1DC collision
// detection; callers must not treat it as a collision-resistant authority proof.
package gitrepo
