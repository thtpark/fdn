# S2 typed message sessions

`message.NewSession(ctx, conn, handler)` takes ownership of an **already admitted**
`net.Conn`. Both ends initialize concurrently, including with `net.Pipe`. Two
simultaneous send/read handshake phases require exact protocol version **2** and
the SHA-256 of `payload.proto`; there is no legacy fallback. Protocol/API/SQL
versions are separate. Wire changes require a new protocol version. Payload
contract changes require both peers to upgrade together.

The session has no identity, authentication, operation authorization, discovery
timer, business poller, HTTP path, HTTP server, gRPC or net/rpc implementation.
An adapter must allowlist each `Operation`, bind its exact input/output types,
validate its narrow `Target`/`Query`, and apply current application authority.
Correlation IDs are connection-local and unrelated to durable operation IDs.

## API and lifetime

* `Call(ctx, Request)` multiplexes both directions. Calls run concurrently and
  may complete out of order. A non-nil remote error is a redacted `*domain.Fault`
  preserving a recognized code and retryability, never the original message.
  A successful empty result is an explicit protobuf `Result.payload` variant.
* `PublicError(err)` is an explicit exception for an **already-public**
  `domain.Fault` message approved by the calling boundary. It snapshots the
  recognized fault's message/code/retryability, not any surrounding error text.
  Unmarked domain faults and arbitrary backend errors remain redacted. Invalid
  UTF-8, NUL-containing or over-1,024-byte messages remain redacted, not truncated.
  Nil stays nil, and the local error chain is preserved. This opt-in also works
  for terminal `Shutdown` faults; it is not a private-text sanitizer.
* `Ping(ctx)` only exchanges priority ping/pong frames; it never invokes a
  handler. There are no automatic pings.
* `Close()` closes immediately, cancels incoming handler contexts and fails
  pending calls. It is idempotent.
* `Shutdown(err)` gives a small typed terminal fault priority, then closes. A
  responsive peer sees `Denied` rather than EOF for a permission-denied shutdown.
  Delivery cannot be guaranteed over an already broken or nonreading connection.
  It is safe to call from this session's own handler: terminal writing does not
  wait for handlers, and handler-context cancellation happens after that attempt.
* `Done()` closes on termination. `Err()` is nil before termination/shutdown
  begins and retains its first cause.
* The context passed to `NewSession` owns the **whole session**, not just the
  handshake. A caller's `Call`/`Ping` context only owns that operation.

An expired/cancelled call returns promptly locally. Its cancellation is never
sent before its request. Remote cancellation/deadline notification and the
cancelled result do not wait for a business handler to exit. An uncooperative
handler **retains its admission slot until it actually exits**; Go code is not
forcibly terminated. Cancelled, already-written calls remain counted as pending
until their cancellation result arrives or the connection closes.

Calls and pings each have a strictly increasing, direction-local ID sequence.
Skipped IDs are permitted; repeated/decreasing request IDs, unsolicited/duplicate
results and pongs fail closed. A late/duplicate cancel for a previously admitted
call is harmless and idempotent.

## Binary payload contract

`MarshalPayload` / `UnmarshalPayload` use standard protobuf descriptors,
`dynamicpb` and the standard deterministic protobuf binary marshaler. Neither
function JSON-marshals application data. `payload.proto` is directly compilable;
`payloadschema/fields.json` is its explicit field-number ledger. No hash, map
iteration order, or current Go declaration order assigns runtime field numbers.

All current domain DTOs and `domain.Page[T]` are supported, including embedded
`Command`, string aliases, nested DTOs, exact signed/unsigned 64-bit integers,
presence-bearing pointers, byte slices, lists, and string-keyed maps. Floats,
interfaces (including `map[string]any`), arrays, functions, channels, unsafe
types, and unregistered structs are rejected even when their values are empty.
A local input overlay can embed one registered DTO and explicitly shadow an
existing JSON-named field with a protobuf-compatible type/pointer. It inherits
the field number, explicit fields win over embedded fields, and no new/protected
fields are admitted this way.

Public transport DTOs are registered in `domain`, not API/configuration/platform
packages: local status/profile/health, message commands with optional expiry,
relay chunks, generation-fenced account operations, and bodyless update hints.
`domain.Page[domain.Update]` and root update lists use the same collection rules.

The deterministic schema rules are:

| Go value | Protobuf node |
| --- | --- |
| string / alias | `String { optional string value = 1; }` |
| bool | `Bool { optional bool value = 1; }` |
| signed integers | `Signed { optional sint64 value = 1; }` |
| unsigned integers | `Unsigned { optional uint64 value = 1; }` |
| bytes / retained `json.RawMessage` snapshot | `Bytes { optional bytes value = 1; }` |
| registered struct | `Go_Name`, message fields with ledger-assigned IDs |
| `domain.Page[T]` | `Page_T { List_T items = 1; String next_cursor = 2; }` |
| list | `List_T { repeated T items = 1; bool present = 2; }` |
| map | `Map_T { repeated Map_T_Entry items = 1; bool present = 2; }` |
| map entry | `optional string key = 1; T value = 2;` |
| pointer in a list/map | `Pointer_T { T value = 1; }` |

Root pointers are dereferenced and nil root pointers are errors. A struct pointer
field uses the child's node directly: missing means nil; a present zero child
means an explicit zero edit. Container/byte nodes preserve nil versus non-nil
empty values, including root `[]domain.Task{}` and an empty `Page.Items`.
Container-node `present=false` forbids items. Map keys are unique and sorted on
encode. Root/nested container instantiations follow the same named templates;
the checked-in contract materializes all current domain types and their common
root list/map/page forms.

Raw JSON requests/proposals must be strict-decoded into the operation's exact
input DTO **before** marshaling. `json.RawMessage` is only a declared bytes value
for retained snapshots; it is not a whole-request JSON compatibility tunnel.
Existing JSON journal/fingerprint formats are not changed by this package.

Unknown fields are rejected recursively, including server-owned/protected fields
not in the exact request descriptor. Duplicate singular fields/oneof variants,
invalid enum/boolean values, numeric overflow, malformed data and duplicate map
keys are errors. Decode failure does not partially mutate the target.

## Bounds and failure behavior

| Bound | Value |
| --- | --- |
| Request payload | 1 MiB |
| Response/codec payload | 32 MiB |
| Frame | 32 MiB + 16 KiB, unsigned 4-byte big-endian length prefix |
| Handler concurrency | 16 per session, no waiting handler queue |
| Pending calls / pings | 64 / 16 per direction |
| Business writer queue | 16 frames and 64 MiB |
| Reserved control queue | 64 small frames; terminal has a separate slot |
| Metadata | 128-byte operation, 256-byte target, 16 query entries / 8 KiB |
| Query key/value | 64 / 512 bytes |
| Opted-in public fault message | 1,024 UTF-8 bytes; no NUL |
| Payload nesting / collection / wire-field budget | 32 / 4,096 items / 65,536 |
| Schema cache | 1,024 concrete root types per process |
| Handshake / partial-frame reads | 5 seconds; fully idle reads have no timeout |
| Each write / terminal write | 400 / 200 milliseconds |
| Shutdown hard bound | 700 milliseconds |

Local admission/queue saturation returns `Unavailable`, not default data or
success. Incoming saturation returns a typed fault without blocking the reader.
If a response/control cannot be queued, the session attempts a bounded terminal
`Unavailable` fault rather than silently dropping it.

One reader and one bounded writer are independent of handler execution.
Cancellation, faults and ping/pong have reserved priority; after eight control
frames, an available business frame gets a turn. A frame already being written
cannot be preempted; a stalled write closes the session within its write bound.
Large/bulk transfers needing longer writes belong on a separate purpose-bound
stream, not this liveness-sensitive session.

These are per-connection resource bounds. Focused isolated tests do **not**
qualify 1,000 live identities or update p95/p99 fleet SLOs; durable update routing,
adapters, deployment and fleet benchmarking are outside this package.

## Reproduction and tests

From the repository root, with protobuf module **v1.36.12** and **protoc 36.2**:

```sh
go run ./tools/protocol --check
go test -race -count=1 ./internal/message
```

To deliberately add domain fields, run `go run ./tools/protocol --update-fields`
and then `go run ./tools/protocol`. Existing IDs are retained; removed fields are
reserved and IDs are never recycled. Review the generated contract as a clean
version change. Normal generation/check builds the pinned Go plugin locally in
`tools/protocol/.work`, verifies generated wire code and compiles `payload.proto`;
it installs nothing globally and removes its work directory. When the environment
requires project-local scratch space, set `TMPDIR`/`GOTMPDIR` to an existing,
separate project-owned directory before invoking Go.

The package tests cover every current domain DTO, deterministic schema output,
integer extremes, empty collections, edit presence/shadowing, malformed/protected
fields, duplicate IDs, full-duplex callers, idle ping, blocked-call cancellation,
deadline propagation, error redaction, pending-call failure, typed terminal denial,
exact opted-in public messages, version/schema rejection, handler/pending/queue
admission and stalled writes.
