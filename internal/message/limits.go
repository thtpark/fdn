package message

import "time"

// These are per-session admission limits, not a fleet capacity qualification.
const (
	ProtocolVersion            = 2
	MaxRequestBytes            = 1 << 20
	MaxPayloadBytes            = 32 << 20
	MaxFrameBytes              = MaxPayloadBytes + (16 << 10)
	MaxPayloadDepth            = 32
	MaxCollectionElements      = 4096
	MaxPayloadNodes            = 65536
	MaxConcurrentHandlers      = 16
	MaxPendingCalls            = 64
	MaxPendingPings            = 16
	MaxQueuedFrames            = 16
	MaxQueuedBytes             = 64 << 20
	MaxControlFrames           = 64
	MaxQueryEntries            = 16
	MaxQueryBytes              = 8192
	MaxOperationBytes          = 128
	MaxTargetBytes             = 256
	MaxQueryKeyBytes           = 64
	MaxQueryValueBytes         = 512
	MaxPublicErrorMessageBytes = 1024
	HandshakeTimeout           = 5 * time.Second
	FrameReadTimeout           = 5 * time.Second
	WriteTimeout               = 400 * time.Millisecond
	TerminalWriteTimeout       = 200 * time.Millisecond
	shutdownTimeout            = WriteTimeout + TerminalWriteTimeout + 100*time.Millisecond
)
