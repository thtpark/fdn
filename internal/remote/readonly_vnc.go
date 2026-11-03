package remote

import (
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"net"
	"sync"

	"github.com/thtpark/fdn/internal/domain"
)

const (
	readOnlyVNCMaxText       = 1 << 20
	readOnlyVNCMaxDimension  = 16384
	readOnlyVNCMaxPixels     = 16 << 20
	readOnlyVNCMaxUpdate     = 64 << 20
	readOnlyVNCMaxRectangles = 4096
	readOnlyVNCMaxEncodings  = 256
	readOnlyVNCMaxCursor     = 1024

	readOnlyVNCRaw         int32 = 0
	readOnlyVNCCopyRect    int32 = 1
	readOnlyVNCCursor      int32 = -239
	readOnlyVNCDesktopSize int32 = -223
	readOnlyVNCLastRect    int32 = -224
)

// BridgeReadOnlyVNC is BridgeSharedVNC with keyboard and pointer input always
// disabled, regardless of viewer flags.
func BridgeReadOnlyVNC(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
	return BridgeSharedVNC(ctx, viewer, backend, nil)
}

// BridgeSharedVNC filters an existing viewer/backend pair; it never dials or
// listens. It owns both connections, whose Close methods must unblock I/O.
// Cancellation or completion closes both connections and joins all workers.
//
// Supported RFB versions are 3.3, 3.7 and 3.8, with None or opaque VNCAuth
// authentication, 8/16/32-bit pixels, and Raw, CopyRect, Cursor, DesktopSize and
// LastRect updates. Other security types (including encrypted transports) and
// unparseable extensions fail closed. Only pixel formats, filtered encodings
// and framebuffer requests reach the backend unconditionally; ClientInit is
// always shared. Before each complete keyboard/pointer event is written,
// inputAllowed must return true. A nil callback denies input. The caller owns
// callback synchronization and authority; the callback must return promptly and
// must not derive approval from UI flags. Revocation cannot retract events
// already being written.
// Clipboard messages are discarded in both directions. Desktop-resize and power
// requests are always discarded; unknown client messages fail.
//
// Framebuffer requests are serialized to keep pixel-format changes unambiguous;
// unsolicited/continuous updates are unsupported. Limits are 16,384 per screen
// dimension, 16 Mi pixels, 64 MiB per update, 4,096 rectangles per update,
// 1,024 per cursor dimension, 256 requested encodings, and 1 MiB per text field.
// LastRect permits a 65,535 rectangle-count sentinel within the same actual
// rectangle limit. Server desktop-size changes and cursor images are preserved.
func BridgeSharedVNC(ctx context.Context, viewer, backend io.ReadWriteCloser, inputAllowed func() bool) error {
	return bridgeFilteredVNC(ctx, viewer, backend, inputAllowed, readOnlyVNCHandshake)
}

// BridgeAuthenticatedVNC uses a target-owned, 1..8-byte classic VNC password to
// authenticate the backend once, without exposing its challenge, response,
// password or failure reason to the viewer. The caller must read the password
// locally under its private-file policy; this function never opens a file.
// Password bytes are used exactly as supplied, without trimming or truncation.
// Caller-owned password memory is not modified; derived key/response buffers
// are cleared after authentication.
//
// The viewer must already be Foundation-authenticated: it receives None security
// only after backend VNCAuth succeeds. None-only backends, encrypted/extended
// security and rejected authentication fail closed, without another submission.
// All BridgeSharedVNC filters, limits, input gating and joined teardown apply.
func BridgeAuthenticatedVNC(ctx context.Context, viewer, backend io.ReadWriteCloser, password []byte, inputAllowed func() bool) error {
	length := len(password)
	var key [8]byte
	if length >= 1 && length <= len(key) {
		for i, value := range password {
			key[i] = bits.Reverse8(value)
		}
	}
	defer clear(key[:])
	return bridgeFilteredVNC(ctx, viewer, backend, inputAllowed, func(viewer, backend io.ReadWriter) (*readOnlyVNCState, error) {
		defer clear(key[:])
		if length < 1 || length > len(key) {
			return nil, domain.Fail(domain.Invalid, "target-owned VNC password must contain 1..8 bytes")
		}
		return authenticatedVNCHandshake(viewer, backend, key[:])
	})
}

func bridgeFilteredVNC(ctx context.Context, viewer, backend io.ReadWriteCloser, inputAllowed func() bool, handshake func(io.ReadWriter, io.ReadWriter) (*readOnlyVNCState, error)) error {
	if viewer == nil || backend == nil {
		return domain.Fail(domain.Invalid, "read-only VNC requires viewer and backend connections")
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	closeBoth := sync.OnceValue(func() error {
		left := readOnlyVNCError(viewer.Close())
		right := readOnlyVNCError(backend.Close())
		return errors.Join(left, right)
	})
	done := make(chan error, 1)
	go func() {
		state, err := handshake(viewer, backend)
		if err != nil {
			done <- readOnlyVNCError(err)
			return
		}
		pumps := make(chan error, 2)
		go func() { pumps <- readOnlyVNCClient(workCtx, viewer, backend, state, inputAllowed) }()
		go func() { pumps <- readOnlyVNCServer(viewer, backend, state) }()
		first := readOnlyVNCError(<-pumps)
		cancel()
		_ = closeBoth()
		second := readOnlyVNCError(<-pumps)
		done <- errors.Join(first, second)
	}()
	var result error
	select {
	case result = <-done:
	case <-ctx.Done():
		_ = closeBoth()
		<-done
	}
	closeErr := closeBoth()
	if err := ctx.Err(); err != nil {
		return errors.Join(err, closeErr)
	}
	return errors.Join(result, closeErr)
}

func readOnlyVNCError(err error) error {
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed),
		errors.Is(err, io.ErrClosedPipe), errors.Is(err, context.Canceled):
		return nil
	case errors.Is(err, io.ErrUnexpectedEOF):
		return domain.Fail(domain.Invalid, "truncated read-only VNC protocol message")
	}
	var fault *domain.Fault
	if errors.As(err, &fault) {
		return fault
	}
	return domain.Fail(domain.Unavailable, "read-only VNC transport interrupted")
}

func readOnlyVNCRead(r io.Reader, data []byte) error {
	_, err := io.ReadFull(r, data)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func readOnlyVNCWrite(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func readOnlyVNCCopy(w io.Writer, r io.Reader, size int64) error {
	_, err := io.CopyN(w, r, size)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func readOnlyVNCVersion(banner [12]byte) (int, error) {
	switch string(banner[:]) {
	case "RFB 003.003\n":
		return 3, nil
	case "RFB 003.007\n":
		return 7, nil
	case "RFB 003.008\n":
		return 8, nil
	default:
		return 0, domain.Fail(domain.Invalid, "unsupported read-only VNC protocol version")
	}
}

func readOnlyVNCNegotiateVersion(viewer, backend io.ReadWriter) (int, error) {
	var banner [12]byte
	if err := readOnlyVNCRead(backend, banner[:]); err != nil {
		return 0, err
	}
	serverVersion, err := readOnlyVNCVersion(banner)
	if err != nil {
		return 0, err
	}
	if err := readOnlyVNCWrite(viewer, banner[:]); err != nil {
		return 0, err
	}
	if err := readOnlyVNCRead(viewer, banner[:]); err != nil {
		return 0, err
	}
	version, err := readOnlyVNCVersion(banner)
	if err != nil {
		return 0, err
	}
	if version > serverVersion {
		return 0, domain.Fail(domain.Invalid, "read-only VNC client selected a newer protocol version")
	}
	if err := readOnlyVNCWrite(backend, banner[:]); err != nil {
		return 0, err
	}
	return version, nil
}

func readOnlyVNCHandshake(viewer, backend io.ReadWriter) (*readOnlyVNCState, error) {
	version, err := readOnlyVNCNegotiateVersion(viewer, backend)
	if err != nil {
		return nil, err
	}
	security, err := readOnlyVNCSecurity(viewer, backend, version)
	if err != nil {
		return nil, err
	}
	if security == 2 {
		if err := readOnlyVNCOpaque(viewer, backend); err != nil {
			return nil, err
		}
		if err := readOnlyVNCOpaque(backend, viewer); err != nil {
			return nil, err
		}
	}
	if security == 2 || version == 8 {
		var result [4]byte
		if err := readOnlyVNCRead(backend, result[:]); err != nil {
			return nil, err
		}
		status := binary.BigEndian.Uint32(result[:])
		if status > 2 {
			return nil, domain.Fail(domain.Invalid, "unsupported read-only VNC authentication result")
		}
		if err := readOnlyVNCWrite(viewer, result[:]); err != nil {
			return nil, err
		}
		if status != 0 {
			if version == 8 {
				if err := readOnlyVNCText(viewer, backend); err != nil {
					return nil, err
				}
			}
			return nil, domain.Fail(domain.Denied, "read-only VNC authentication failed")
		}
	}
	return readOnlyVNCInitialize(viewer, backend)
}

func readOnlyVNCInitialize(viewer, backend io.ReadWriter) (*readOnlyVNCState, error) {
	var shared [1]byte
	if err := readOnlyVNCRead(viewer, shared[:]); err != nil {
		return nil, err
	}
	shared[0] = 1
	if err := readOnlyVNCWrite(backend, shared[:]); err != nil {
		return nil, err
	}
	var init [24]byte
	if err := readOnlyVNCRead(backend, init[:]); err != nil {
		return nil, err
	}
	width, height := binary.BigEndian.Uint16(init[0:2]), binary.BigEndian.Uint16(init[2:4])
	if !readOnlyVNCDimensions(width, height) {
		return nil, domain.Fail(domain.Invalid, "read-only VNC framebuffer exceeds screen limits")
	}
	pixelBytes, err := readOnlyVNCPixelFormat(init[4:20])
	if err != nil {
		return nil, err
	}
	nameSize := binary.BigEndian.Uint32(init[20:24])
	if nameSize > readOnlyVNCMaxText {
		return nil, domain.Fail(domain.Invalid, "read-only VNC desktop name exceeds text limit")
	}
	if err := readOnlyVNCWrite(viewer, init[:]); err != nil {
		return nil, err
	}
	if err := readOnlyVNCCopy(viewer, backend, int64(nameSize)); err != nil {
		return nil, err
	}
	return &readOnlyVNCState{pixelBytes: pixelBytes, width: width, height: height}, nil
}

func authenticatedVNCHandshake(viewer, backend io.ReadWriter, key []byte) (*readOnlyVNCState, error) {
	version, err := readOnlyVNCNegotiateVersion(viewer, backend)
	if err != nil {
		return nil, err
	}
	if err := authenticatedVNCBackend(backend, version, key); err != nil {
		return nil, err
	}
	if version == 3 {
		if err := readOnlyVNCWrite(viewer, []byte{0, 0, 0, 1}); err != nil {
			return nil, err
		}
	} else {
		if err := readOnlyVNCWrite(viewer, []byte{1, 1}); err != nil {
			return nil, err
		}
		var selected [1]byte
		if err := readOnlyVNCRead(viewer, selected[:]); err != nil {
			return nil, err
		}
		if selected[0] != 1 {
			return nil, authenticatedVNCFailure()
		}
	}
	if version == 8 {
		if err := readOnlyVNCWrite(viewer, []byte{0, 0, 0, 0}); err != nil {
			return nil, err
		}
	}
	return readOnlyVNCInitialize(viewer, backend)
}

func authenticatedVNCFailure() error {
	return domain.Fail(domain.Denied, "target-owned VNC authentication failed")
}

func authenticatedVNCBackend(backend io.ReadWriter, version int, key []byte) error {
	defer clear(key)
	if version == 3 {
		var security [4]byte
		if err := readOnlyVNCRead(backend, security[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(security[:]) != 2 {
			return authenticatedVNCFailure()
		}
	} else {
		var count [1]byte
		if err := readOnlyVNCRead(backend, count[:]); err != nil {
			return err
		}
		if count[0] == 0 {
			return authenticatedVNCFailure()
		}
		offered := make([]byte, int(count[0]))
		if err := readOnlyVNCRead(backend, offered); err != nil {
			return err
		}
		if !readOnlyVNCHasByte(offered, 2) {
			return authenticatedVNCFailure()
		}
		if err := readOnlyVNCWrite(backend, []byte{2}); err != nil {
			return err
		}
	}
	var challenge, response [16]byte
	defer clear(challenge[:])
	defer clear(response[:])
	if err := readOnlyVNCRead(backend, challenge[:]); err != nil {
		return err
	}
	block, err := des.NewCipher(key)
	clear(key)
	if err != nil {
		return authenticatedVNCFailure()
	}
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	if err := readOnlyVNCWrite(backend, response[:]); err != nil {
		return err
	}
	var result [4]byte
	if err := readOnlyVNCRead(backend, result[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result[:]) != 0 {
		return authenticatedVNCFailure()
	}
	return nil
}

func readOnlyVNCSecurity(viewer, backend io.ReadWriter, version int) (byte, error) {
	if version == 3 {
		var security [4]byte
		if err := readOnlyVNCRead(backend, security[:]); err != nil {
			return 0, err
		}
		value := binary.BigEndian.Uint32(security[:])
		if value != 0 && value != 1 && value != 2 {
			return 0, domain.Fail(domain.Denied, "read-only VNC rejects encrypted or extended security types")
		}
		if err := readOnlyVNCWrite(viewer, security[:]); err != nil {
			return 0, err
		}
		if value == 0 {
			if err := readOnlyVNCText(viewer, backend); err != nil {
				return 0, err
			}
			return 0, domain.Fail(domain.Denied, "read-only VNC backend rejected authentication")
		}
		return byte(value), nil
	}
	var count [1]byte
	if err := readOnlyVNCRead(backend, count[:]); err != nil {
		return 0, err
	}
	if count[0] == 0 {
		if err := readOnlyVNCWrite(viewer, count[:]); err != nil {
			return 0, err
		}
		if err := readOnlyVNCText(viewer, backend); err != nil {
			return 0, err
		}
		return 0, domain.Fail(domain.Denied, "read-only VNC backend rejected authentication")
	}
	offered := make([]byte, int(count[0]))
	if err := readOnlyVNCRead(backend, offered); err != nil {
		return 0, err
	}
	allowed := []byte{0}
	for _, value := range offered {
		if (value == 1 || value == 2) && !readOnlyVNCHasByte(allowed[1:], value) {
			allowed = append(allowed, value)
		}
	}
	if len(allowed) == 1 {
		return 0, domain.Fail(domain.Denied, "read-only VNC rejects encrypted or extended security types")
	}
	allowed[0] = byte(len(allowed) - 1)
	if err := readOnlyVNCWrite(viewer, allowed); err != nil {
		return 0, err
	}
	var selected [1]byte
	if err := readOnlyVNCRead(viewer, selected[:]); err != nil {
		return 0, err
	}
	if !readOnlyVNCHasByte(allowed[1:], selected[0]) {
		return 0, domain.Fail(domain.Denied, "read-only VNC client selected an unsupported security type")
	}
	if err := readOnlyVNCWrite(backend, selected[:]); err != nil {
		return 0, err
	}
	return selected[0], nil
}

func readOnlyVNCHasByte(values []byte, value byte) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func readOnlyVNCOpaque(dst io.Writer, src io.Reader) error {
	var opaque [16]byte
	if err := readOnlyVNCRead(src, opaque[:]); err != nil {
		clear(opaque[:])
		return err
	}
	err := readOnlyVNCWrite(dst, opaque[:])
	clear(opaque[:])
	return err
}

func readOnlyVNCText(dst io.Writer, src io.Reader) error {
	var length [4]byte
	if err := readOnlyVNCRead(src, length[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(length[:])
	if size > readOnlyVNCMaxText {
		return domain.Fail(domain.Invalid, "read-only VNC text exceeds limit")
	}
	if err := readOnlyVNCWrite(dst, length[:]); err != nil {
		return err
	}
	return readOnlyVNCCopy(dst, src, int64(size))
}

func readOnlyVNCPixelFormat(format []byte) (int, error) {
	pixelBits, depth := format[0], format[1]
	if (pixelBits != 8 && pixelBits != 16 && pixelBits != 32) || depth == 0 || depth > pixelBits ||
		format[2] > 1 || format[3] > 1 {
		return 0, domain.Fail(domain.Invalid, "unsupported read-only VNC pixel format")
	}
	if format[3] == 1 {
		var used uint64
		for i := range 3 {
			maximum := binary.BigEndian.Uint16(format[4+i*2 : 6+i*2])
			shift := format[10+i]
			mask := uint64(maximum) << shift
			if maximum == 0 || maximum&(maximum+1) != 0 || shift >= pixelBits ||
				mask>>pixelBits != 0 || used&mask != 0 {
				return 0, domain.Fail(domain.Invalid, "invalid read-only VNC true-colour format")
			}
			used |= mask
		}
		if bits.OnesCount64(used) > int(depth) {
			return 0, domain.Fail(domain.Invalid, "invalid read-only VNC true-colour depth")
		}
	}
	return int(pixelBits / 8), nil
}

func readOnlyVNCDimensions(width, height uint16) bool {
	return width != 0 && height != 0 && width <= readOnlyVNCMaxDimension &&
		height <= readOnlyVNCMaxDimension && uint64(width)*uint64(height) <= readOnlyVNCMaxPixels
}

type readOnlyVNCState struct {
	mu         sync.Mutex
	pixelBytes int
	width      uint16
	height     uint16
	pending    chan struct{}
}

func (s *readOnlyVNCState) waitIdle(ctx context.Context) error {
	s.mu.Lock()
	pending := s.pending
	s.mu.Unlock()
	if pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func readOnlyVNCClient(ctx context.Context, viewer io.Reader, backend io.Writer, state *readOnlyVNCState, inputAllowed func() bool) error {
	for {
		var kind [1]byte
		if _, err := io.ReadFull(viewer, kind[:]); err != nil {
			return err
		}
		switch kind[0] {
		case 0:
			var message [20]byte
			if err := readOnlyVNCRead(viewer, message[1:]); err != nil {
				return err
			}
			pixelBytes, err := readOnlyVNCPixelFormat(message[4:20])
			if err != nil {
				return err
			}
			if err := state.waitIdle(ctx); err != nil {
				return err
			}
			state.mu.Lock()
			state.pixelBytes = pixelBytes
			state.mu.Unlock()
			if err := readOnlyVNCWrite(backend, message[:]); err != nil {
				return err
			}
		case 2:
			if err := readOnlyVNCEncodings(backend, viewer); err != nil {
				return err
			}
		case 3:
			var message [10]byte
			message[0] = 3
			if err := readOnlyVNCRead(viewer, message[1:]); err != nil {
				return err
			}
			if message[1] > 1 {
				return domain.Fail(domain.Invalid, "invalid read-only VNC framebuffer request")
			}
			if err := state.waitIdle(ctx); err != nil {
				return err
			}
			state.mu.Lock()
			state.pending = make(chan struct{})
			state.mu.Unlock()
			if err := readOnlyVNCWrite(backend, message[:]); err != nil {
				return err
			}
		case 4:
			var message [8]byte
			message[0] = 4
			if err := readOnlyVNCRead(viewer, message[1:]); err != nil {
				return err
			}
			if message[1] > 1 {
				return domain.Fail(domain.Invalid, "invalid read-only VNC key event")
			}
			message[2], message[3] = 0, 0
			if inputAllowed != nil && inputAllowed() {
				if err := readOnlyVNCWrite(backend, message[:]); err != nil {
					return err
				}
			}
		case 5:
			var message [6]byte
			message[0] = 5
			if err := readOnlyVNCRead(viewer, message[1:]); err != nil {
				return err
			}
			if inputAllowed != nil && inputAllowed() {
				if err := readOnlyVNCWrite(backend, message[:]); err != nil {
					return err
				}
			}
		case 6:
			if err := readOnlyVNCCutText(viewer); err != nil {
				return err
			}
		case 250:
			if err := readOnlyVNCCopy(io.Discard, viewer, 3); err != nil {
				return err
			}
		case 251:
			var header [7]byte
			if err := readOnlyVNCRead(viewer, header[:]); err != nil {
				return err
			}
			if err := readOnlyVNCCopy(io.Discard, viewer, int64(header[5])*16); err != nil {
				return err
			}
		case 255:
			var message [12]byte
			message[0] = 255
			if err := readOnlyVNCRead(viewer, message[1:2]); err != nil {
				return err
			}
			if message[1] != 0 {
				return domain.Fail(domain.Denied, "unsupported read-only VNC extended input")
			}
			if err := readOnlyVNCRead(viewer, message[2:]); err != nil {
				return err
			}
			if binary.BigEndian.Uint16(message[2:4]) > 1 {
				return domain.Fail(domain.Invalid, "invalid read-only VNC extended key event")
			}
			if inputAllowed != nil && inputAllowed() {
				if err := readOnlyVNCWrite(backend, message[:]); err != nil {
					return err
				}
			}
		default:
			return domain.Fail(domain.Denied, "unsupported read-only VNC client message")
		}
	}
}

func readOnlyVNCEncodings(dst io.Writer, src io.Reader) error {
	var header [3]byte
	if err := readOnlyVNCRead(src, header[:]); err != nil {
		return err
	}
	count := int(binary.BigEndian.Uint16(header[1:3]))
	if count > readOnlyVNCMaxEncodings {
		return domain.Fail(domain.Invalid, "read-only VNC encoding list exceeds limit")
	}
	values := make([]byte, count*4)
	if err := readOnlyVNCRead(src, values); err != nil {
		return err
	}
	encodings := make([]int32, 0, 5)
	for i := range count {
		value := int32(binary.BigEndian.Uint32(values[i*4 : i*4+4]))
		switch value {
		case readOnlyVNCRaw, readOnlyVNCCopyRect, readOnlyVNCCursor, readOnlyVNCDesktopSize, readOnlyVNCLastRect:
			found := false
			for _, accepted := range encodings {
				found = found || accepted == value
			}
			if !found {
				encodings = append(encodings, value)
			}
		}
	}
	hasRaw := false
	for _, value := range encodings {
		hasRaw = hasRaw || value == readOnlyVNCRaw
	}
	if !hasRaw {
		encodings = append(encodings, readOnlyVNCRaw)
	}
	message := make([]byte, 4+len(encodings)*4)
	message[0] = 2
	binary.BigEndian.PutUint16(message[2:4], uint16(len(encodings)))
	for i, value := range encodings {
		binary.BigEndian.PutUint32(message[4+i*4:8+i*4], uint32(value))
	}
	return readOnlyVNCWrite(dst, message)
}

func readOnlyVNCCutText(src io.Reader) error {
	var header [7]byte
	if err := readOnlyVNCRead(src, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[3:7])
	if size > readOnlyVNCMaxText {
		return domain.Fail(domain.Invalid, "read-only VNC clipboard exceeds limit or uses an unsupported extension")
	}
	return readOnlyVNCCopy(io.Discard, src, int64(size))
}

func readOnlyVNCServer(viewer io.Writer, backend io.Reader, state *readOnlyVNCState) error {
	for {
		var kind [1]byte
		if _, err := io.ReadFull(backend, kind[:]); err != nil {
			return err
		}
		switch kind[0] {
		case 0:
			if err := readOnlyVNCFramebuffer(viewer, backend, state); err != nil {
				return err
			}
		case 1:
			var header [6]byte
			header[0] = 1
			if err := readOnlyVNCRead(backend, header[1:]); err != nil {
				return err
			}
			first, count := uint32(binary.BigEndian.Uint16(header[2:4])), uint32(binary.BigEndian.Uint16(header[4:6]))
			if first+count > 65536 {
				return domain.Fail(domain.Invalid, "read-only VNC colour map exceeds limit")
			}
			if err := readOnlyVNCWrite(viewer, header[:]); err != nil {
				return err
			}
			if err := readOnlyVNCCopy(viewer, backend, int64(count)*6); err != nil {
				return err
			}
		case 2:
			if err := readOnlyVNCWrite(viewer, kind[:]); err != nil {
				return err
			}
		case 3:
			if err := readOnlyVNCCutText(backend); err != nil {
				return err
			}
		case 250:
			if err := readOnlyVNCCopy(io.Discard, backend, 3); err != nil {
				return err
			}
		default:
			return domain.Fail(domain.Invalid, "unsupported read-only VNC server message")
		}
	}
}

func readOnlyVNCRectBounds(x, y, width, height, screenWidth, screenHeight uint16) bool {
	return uint32(x)+uint32(width) <= uint32(screenWidth) &&
		uint32(y)+uint32(height) <= uint32(screenHeight)
}

func readOnlyVNCFramebuffer(viewer io.Writer, backend io.Reader, state *readOnlyVNCState) error {
	var header [4]byte
	if err := readOnlyVNCRead(backend, header[1:]); err != nil {
		return err
	}
	count := int(binary.BigEndian.Uint16(header[2:4]))
	if count > readOnlyVNCMaxRectangles && count != 65535 {
		return domain.Fail(domain.Invalid, "read-only VNC rectangle count exceeds limit")
	}
	state.mu.Lock()
	width, height, pixelBytes, pending := state.width, state.height, state.pixelBytes, state.pending
	state.mu.Unlock()
	if pending == nil {
		return domain.Fail(domain.Invalid, "read-only VNC does not support unsolicited framebuffer updates")
	}
	if err := readOnlyVNCWrite(viewer, header[:]); err != nil {
		return err
	}
	remaining := int64(readOnlyVNCMaxUpdate)
	for i := 0; i < count; i++ {
		if i >= readOnlyVNCMaxRectangles {
			return domain.Fail(domain.Invalid, "read-only VNC rectangle count exceeds limit")
		}
		var rect [12]byte
		if err := readOnlyVNCRead(backend, rect[:]); err != nil {
			return err
		}
		x, y := binary.BigEndian.Uint16(rect[0:2]), binary.BigEndian.Uint16(rect[2:4])
		w, h := binary.BigEndian.Uint16(rect[4:6]), binary.BigEndian.Uint16(rect[6:8])
		encoding := int32(binary.BigEndian.Uint32(rect[8:12]))
		var size int64
		var copyRect [4]byte
		switch encoding {
		case readOnlyVNCRaw, readOnlyVNCCopyRect:
			if !readOnlyVNCRectBounds(x, y, w, h, width, height) {
				return domain.Fail(domain.Invalid, "read-only VNC rectangle exceeds framebuffer bounds")
			}
			if encoding == readOnlyVNCRaw {
				size = int64(w) * int64(h) * int64(pixelBytes)
			} else {
				if err := readOnlyVNCRead(backend, copyRect[:]); err != nil {
					return err
				}
				sourceX, sourceY := binary.BigEndian.Uint16(copyRect[0:2]), binary.BigEndian.Uint16(copyRect[2:4])
				if !readOnlyVNCRectBounds(sourceX, sourceY, w, h, width, height) {
					return domain.Fail(domain.Invalid, "read-only VNC CopyRect source exceeds framebuffer bounds")
				}
				size = 4
			}
		case readOnlyVNCCursor:
			if w > readOnlyVNCMaxCursor || h > readOnlyVNCMaxCursor || (w == 0) != (h == 0) ||
				(w != 0 && (x >= w || y >= h)) {
				return domain.Fail(domain.Invalid, "read-only VNC cursor exceeds bounds")
			}
			size = int64(w)*int64(h)*int64(pixelBytes) + int64((uint32(w)+7)/8)*int64(h)
		case readOnlyVNCDesktopSize:
			if !readOnlyVNCDimensions(w, h) {
				return domain.Fail(domain.Invalid, "read-only VNC desktop resize exceeds screen limits")
			}
			width, height = w, h
		case readOnlyVNCLastRect:
		default:
			return domain.Fail(domain.Invalid, "unsupported read-only VNC framebuffer encoding")
		}
		if size > remaining {
			return domain.Fail(domain.Invalid, "read-only VNC framebuffer update exceeds byte limit")
		}
		remaining -= size
		if err := readOnlyVNCWrite(viewer, rect[:]); err != nil {
			return err
		}
		if encoding == readOnlyVNCCopyRect {
			if err := readOnlyVNCWrite(viewer, copyRect[:]); err != nil {
				return err
			}
		} else if err := readOnlyVNCCopy(viewer, backend, size); err != nil {
			return err
		}
		if encoding == readOnlyVNCLastRect {
			break
		}
	}
	state.mu.Lock()
	state.width, state.height = width, height
	state.pending = nil
	close(pending)
	state.mu.Unlock()
	return nil
}
