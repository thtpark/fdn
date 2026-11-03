package remote

import (
	"bytes"
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type vncFixtureConn struct {
	net.Conn
	active atomic.Int32
	closed atomic.Int32
}

func (c *vncFixtureConn) Read(p []byte) (int, error) {
	c.active.Add(1)
	defer c.active.Add(-1)
	return c.Conn.Read(p)
}

func (c *vncFixtureConn) Write(p []byte) (int, error) {
	c.active.Add(1)
	defer c.active.Add(-1)
	return c.Conn.Write(p)
}

func (c *vncFixtureConn) Close() error {
	c.closed.Add(1)
	return c.Conn.Close()
}

type vncFixture struct {
	viewer  net.Conn
	backend net.Conn
	left    *vncFixtureConn
	right   *vncFixtureConn
	cancel  context.CancelFunc
	done    chan error
	stopped bool
}

func newVNCFixture(t *testing.T, bridge func(context.Context, io.ReadWriteCloser, io.ReadWriteCloser) error) *vncFixture {
	t.Helper()
	left, viewer := net.Pipe()
	right, backend := net.Pipe()
	f := &vncFixture{
		viewer: viewer, backend: backend, left: &vncFixtureConn{Conn: left}, right: &vncFixtureConn{Conn: right},
		done: make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	for _, peer := range []net.Conn{viewer, backend} {
		if err := peer.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal("could not bound protocol fixture I/O")
		}
	}
	go func() { f.done <- bridge(ctx, f.left, f.right) }()
	t.Cleanup(func() {
		cancel()
		viewer.Close()
		backend.Close()
		if !f.stopped {
			select {
			case <-f.done:
			case <-time.After(5 * time.Second):
				t.Error("VNC bridge did not join during fixture cleanup")
			}
		}
	})
	return f
}

func vncFixtureWrite(conn io.Writer, data []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		n, err := conn.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	return done
}

func vncFixtureWaitWrite(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("protocol fixture write failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("protocol fixture write did not finish")
	}
}

func vncFixtureExpect(t *testing.T, conn io.Reader, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal("protocol fixture read failed")
	}
	if !bytes.Equal(got, want) {
		t.Fatal("unexpected protocol bytes; payload intentionally omitted")
	}
}

func (f *vncFixture) transfer(t *testing.T, src, dst net.Conn, data, want []byte) {
	t.Helper()
	done := vncFixtureWrite(src, data)
	vncFixtureExpect(t, dst, want)
	vncFixtureWaitWrite(t, done)
}

func (f *vncFixture) wait(t *testing.T) error {
	t.Helper()
	var err error
	select {
	case err = <-f.done:
		f.stopped = true
	case <-time.After(5 * time.Second):
		t.Fatal("VNC bridge did not terminate")
	}
	for _, endpoint := range []*vncFixtureConn{f.left, f.right} {
		if endpoint.closed.Load() != 1 || endpoint.active.Load() != 0 {
			t.Fatal("VNC bridge returned without exactly-once, joined connection teardown")
		}
	}
	return err
}

func (f *vncFixture) closeCleanly(t *testing.T) {
	t.Helper()
	f.viewer.Close()
	if err := f.wait(t); err != nil {
		t.Fatalf("clean VNC disconnect failed with %s", domain.AsFault(err).Code)
	}
}

func vncFixtureBanner(version int) []byte {
	return []byte(fmt.Sprintf("RFB 003.%03d\n", version))
}

func vncFixtureFormat(pixelBits byte) []byte {
	format := make([]byte, 16)
	format[0], format[1], format[3] = pixelBits, pixelBits, 1
	switch pixelBits {
	case 8:
		format[5], format[7], format[9] = 7, 7, 3
		format[10], format[11] = 5, 2
	case 16:
		format[5], format[7], format[9] = 31, 63, 31
		format[10], format[11] = 11, 5
	case 32:
		format[1] = 24
		format[5], format[7], format[9] = 255, 255, 255
		format[10], format[11] = 16, 8
	}
	return format
}

func vncFixtureInit(width, height uint16, format []byte) []byte {
	name := []byte("fixture-screen")
	message := make([]byte, 24+len(name))
	binary.BigEndian.PutUint16(message[0:2], width)
	binary.BigEndian.PutUint16(message[2:4], height)
	copy(message[4:20], format)
	binary.BigEndian.PutUint32(message[20:24], uint32(len(name)))
	copy(message[24:], name)
	return message
}

func (f *vncFixture) versions(t *testing.T, serverVersion, clientVersion int) {
	t.Helper()
	f.transfer(t, f.backend, f.viewer, vncFixtureBanner(serverVersion), vncFixtureBanner(serverVersion))
	f.transfer(t, f.viewer, f.backend, vncFixtureBanner(clientVersion), vncFixtureBanner(clientVersion))
}

func (f *vncFixture) security(t *testing.T, version int, security byte) {
	t.Helper()
	if version == 3 {
		value := []byte{0, 0, 0, security}
		f.transfer(t, f.backend, f.viewer, value, value)
	} else {
		f.transfer(t, f.backend, f.viewer, []byte{5, 19, 2, 18, 1, 2}, []byte{2, 2, 1})
		f.transfer(t, f.viewer, f.backend, []byte{security}, []byte{security})
	}
}

func (f *vncFixture) opaqueAuth(t *testing.T) {
	t.Helper()
	challenge := []byte("challenge-bytes!")
	response := []byte("opaque-response!")
	if len(challenge) != 16 || len(response) != 16 {
		t.Fatal("invalid opaque authentication fixture")
	}
	f.transfer(t, f.backend, f.viewer, challenge, challenge)
	f.transfer(t, f.viewer, f.backend, response, response)
}

func (f *vncFixture) handshake(t *testing.T, serverVersion, clientVersion int, security byte, init []byte) {
	t.Helper()
	f.versions(t, serverVersion, clientVersion)
	f.security(t, clientVersion, security)
	if security == 2 {
		f.opaqueAuth(t)
	}
	if security == 2 || clientVersion == 8 {
		f.transfer(t, f.backend, f.viewer, []byte{0, 0, 0, 0}, []byte{0, 0, 0, 0})
	}
	f.transfer(t, f.viewer, f.backend, []byte{0}, []byte{1})
	if init == nil {
		init = vncFixtureInit(8, 8, vncFixtureFormat(32))
	}
	f.transfer(t, f.backend, f.viewer, init, init)
}

func vncFixtureRequest() []byte {
	return []byte{3, 0, 0, 0, 0, 0, 0, 2, 0, 2}
}

func (f *vncFixture) request(t *testing.T) {
	t.Helper()
	request := vncFixtureRequest()
	f.transfer(t, f.viewer, f.backend, request, request)
}

func vncFixtureEncodings(values ...int32) []byte {
	message := make([]byte, 4+4*len(values))
	message[0] = 2
	binary.BigEndian.PutUint16(message[2:4], uint16(len(values)))
	for i, value := range values {
		binary.BigEndian.PutUint32(message[4+i*4:8+i*4], uint32(value))
	}
	return message
}

func vncFixturePixelFormat(format []byte) []byte {
	return append(make([]byte, 4), format...)
}

func vncFixtureCutText(kind byte, text []byte) []byte {
	message := make([]byte, 8+len(text))
	message[0] = kind
	binary.BigEndian.PutUint32(message[4:8], uint32(len(text)))
	copy(message[8:], text)
	return message
}

func vncFixtureRect(encoding int32, x, y, width, height uint16, payload []byte) []byte {
	message := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint16(message[0:2], x)
	binary.BigEndian.PutUint16(message[2:4], y)
	binary.BigEndian.PutUint16(message[4:6], width)
	binary.BigEndian.PutUint16(message[6:8], height)
	binary.BigEndian.PutUint32(message[8:12], uint32(encoding))
	copy(message[12:], payload)
	return message
}

func vncFixtureFrame(rectangles ...[]byte) []byte {
	message := make([]byte, 4)
	binary.BigEndian.PutUint16(message[2:4], uint16(len(rectangles)))
	for _, rect := range rectangles {
		message = append(message, rect...)
	}
	return message
}

func vncFixtureInputs() (key, pointer, extended, resize, power []byte) {
	key = []byte{4, 1, 0, 0, 0, 0, 0, 65}
	pointer = []byte{5, 1, 0, 1, 0, 2}
	extended = []byte{255, 0, 0, 1, 0, 0, 0, 65, 0, 0, 0, 30}
	resize = append([]byte{251, 0, 0, 8, 0, 8, 1, 0}, bytes.Repeat([]byte{3}, 16)...)
	power = []byte{250, 0, 1, 2}
	return
}

func vncFixtureJoin(messages ...[]byte) []byte {
	var joined []byte
	for _, message := range messages {
		joined = append(joined, message...)
	}
	return joined
}

func (f *vncFixture) failPacket(t *testing.T, src net.Conn, data []byte, truncated bool, code domain.Code) error {
	t.Helper()
	drained := make(chan struct{}, 2)
	for _, peer := range []net.Conn{f.viewer, f.backend} {
		go func() {
			_, _ = io.Copy(io.Discard, peer)
			drained <- struct{}{}
		}()
	}
	written := make(chan struct{}, 1)
	go func() {
		_, _ = src.Write(data)
		if truncated {
			src.Close()
		}
		written <- struct{}{}
	}()
	err := f.wait(t)
	if err == nil || domain.AsFault(err).Code != code {
		t.Fatalf("protocol rejection: wanted %s, got %s", code, domain.AsFault(err).Code)
	}
	for range 3 {
		select {
		case <-drained:
		case <-written:
		case <-time.After(5 * time.Second):
			t.Fatal("protocol rejection left fixture I/O blocked")
		}
	}
	return err
}

func TestBridgeReadOnlyVNCHandshake(t *testing.T) {
	for _, version := range []int{3, 7, 8} {
		for _, security := range []byte{1, 2} {
			t.Run(fmt.Sprintf("3.%d/security-%d", version, security), func(t *testing.T) {
				f := newVNCFixture(t, BridgeReadOnlyVNC)
				f.handshake(t, version, version, security, nil)
				f.request(t)
				frame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 2, 2, bytes.Repeat([]byte{3, 6, 250, 255}, 4)))
				f.transfer(t, f.backend, f.viewer, frame, frame)
				f.closeCleanly(t)
			})
		}
	}
	for _, version := range []int{3, 7} {
		t.Run(fmt.Sprintf("3.8-negotiates-3.%d", version), func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			f.handshake(t, 8, version, 1, nil)
			f.closeCleanly(t)
		})
	}
}

func TestBridgeReadOnlyVNCBlocksInputClipboardResizeAndPower(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, nil)
	key, pointer, extended, resize, power := vncFixtureInputs()
	marker := vncFixtureEncodings(0)
	blocked := vncFixtureJoin(key, pointer, extended, resize, power,
		[]byte{251, 0, 0, 8, 0, 8, 0, 0},
		vncFixtureCutText(6, vncFixtureJoin(marker, key, []byte{255})),
		vncFixtureCutText(6, nil), marker)
	f.transfer(t, f.viewer, f.backend, blocked, marker)
	server := vncFixtureJoin(vncFixtureCutText(3, []byte{2, 0, 6, 255}),
		vncFixtureCutText(3, nil), power, []byte{2})
	f.transfer(t, f.backend, f.viewer, server, []byte{2})
	f.closeCleanly(t)
}

func TestBridgeSharedVNCInputAuthorityChanges(t *testing.T) {
	var allowed atomic.Bool
	var calls atomic.Int32
	f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
		return BridgeSharedVNC(ctx, viewer, backend, func() bool {
			calls.Add(1)
			return allowed.Load()
		})
	})
	f.handshake(t, 8, 8, 2, nil)
	key, pointer, extended, resize, power := vncFixtureInputs()
	marker := vncFixtureEncodings(0)
	messages := vncFixtureJoin(key, pointer, extended, resize, power, vncFixtureCutText(6, []byte("not shared")), marker)
	for i, permit := range []bool{false, true, false} {
		allowed.Store(permit)
		want := marker
		if permit {
			want = vncFixtureJoin(key, pointer, extended, marker)
		}
		f.transfer(t, f.viewer, f.backend, messages, want)
		if calls.Load() != int32((i+1)*3) {
			t.Fatal("input authority was cached, skipped, or consulted for a non-input message")
		}
		f.request(t)
		frame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 1, 1, []byte{1, 2, 3, 4}))
		f.transfer(t, f.backend, f.viewer, frame, frame)
		f.transfer(t, f.backend, f.viewer, vncFixtureJoin(vncFixtureCutText(3, []byte("not shared")), power, []byte{2}), []byte{2})
	}
	f.closeCleanly(t)
}

func TestBridgeSharedVNCNilAuthorityDeniesInput(t *testing.T) {
	f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
		return BridgeSharedVNC(ctx, viewer, backend, nil)
	})
	f.handshake(t, 8, 8, 1, nil)
	key, pointer, extended, _, _ := vncFixtureInputs()
	marker := vncFixtureEncodings(0)
	f.transfer(t, f.viewer, f.backend, vncFixtureJoin(key, pointer, extended, marker), marker)
	f.closeCleanly(t)
}

func TestBridgeSharedVNCChecksAuthorityAfterCompleteEvent(t *testing.T) {
	var allowed atomic.Bool
	var calls atomic.Int32
	f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
		return BridgeSharedVNC(ctx, viewer, backend, func() bool {
			calls.Add(1)
			return allowed.Load()
		})
	})
	f.handshake(t, 8, 8, 1, nil)
	key, pointer, extended, _, _ := vncFixtureInputs()
	marker := vncFixtureEncodings(0)
	for i, event := range [][]byte{key, pointer, extended} {
		allowed.Store(true)
		vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, event[:3]))
		if calls.Load() != int32(i) {
			t.Fatal("input authority was sampled before the event was complete")
		}
		allowed.Store(false)
		f.transfer(t, f.viewer, f.backend, vncFixtureJoin(event[3:], marker), marker)
		if calls.Load() != int32(i+1) {
			t.Fatal("input authority was not sampled after completing the event")
		}
	}
	f.closeCleanly(t)
}

func TestBridgeReadOnlyVNCEncodingFilterAndScreenUpdates(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, nil)
	requested := vncFixtureEncodings(7, 16, -309, -308, 1, -239, -223, -224, 0, 0, 6)
	filtered := vncFixtureEncodings(1, -239, -223, -224, 0)
	f.transfer(t, f.viewer, f.backend, requested, filtered)
	f.request(t)
	frame := vncFixtureFrame(
		vncFixtureRect(-223, 0, 0, 10, 6, nil),
		vncFixtureRect(0, 0, 0, 2, 2, bytes.Repeat([]byte{0, 3, 6, 250}, 4)),
		vncFixtureRect(1, 2, 0, 1, 1, []byte{0, 1, 0, 0}),
		vncFixtureRect(-239, 1, 0, 2, 1, []byte{3, 6, 0, 255, 250, 2, 1, 0, 0xc0}),
		vncFixtureRect(-224, 0, 0, 0, 0, nil),
	)
	binary.BigEndian.PutUint16(frame[2:4], 65535)
	f.transfer(t, f.backend, f.viewer, vncFixtureJoin(frame, vncFixtureCutText(3, []byte{2, 0, 255}), []byte{2}),
		vncFixtureJoin(frame, []byte{2}))
	f.request(t)
	edge := vncFixtureFrame(vncFixtureRect(0, 9, 5, 1, 1, []byte{1, 2, 3, 4}), vncFixtureRect(-239, 0, 0, 0, 0, nil))
	f.transfer(t, f.backend, f.viewer, edge, edge)
	f.closeCleanly(t)
}

func TestBridgeReadOnlyVNCEncodingFallbackAndEmptyUpdate(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, nil)
	for _, requested := range [][]byte{vncFixtureEncodings(), vncFixtureEncodings(7, 16, -309), vncFixtureEncodings(-239, -239)} {
		want := vncFixtureEncodings(0)
		if bytes.Equal(requested, vncFixtureEncodings(-239, -239)) {
			want = vncFixtureEncodings(-239, 0)
		}
		f.transfer(t, f.viewer, f.backend, requested, want)
	}
	f.request(t)
	empty := vncFixtureFrame()
	f.transfer(t, f.backend, f.viewer, empty, empty)
	f.closeCleanly(t)
}

func TestBridgeReadOnlyVNCPixelFormatChangesAndColourMap(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, nil)
	f.request(t)
	oldFrame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 2, 2, bytes.Repeat([]byte{1, 3, 6, 255}, 4)))
	prefix := len(oldFrame) - 15
	f.transfer(t, f.backend, f.viewer, oldFrame[:prefix], oldFrame[:prefix])
	format16 := vncFixturePixelFormat(vncFixtureFormat(16))
	vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, format16))
	remaining := vncFixtureWrite(f.backend, oldFrame[prefix:])
	vncFixtureExpect(t, f.viewer, oldFrame[prefix:])
	vncFixtureExpect(t, f.backend, format16)
	vncFixtureWaitWrite(t, remaining)
	f.request(t)
	newFrame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 2, 2, []byte{1, 3, 6, 255, 2, 0, 5, 4}))
	f.transfer(t, f.backend, f.viewer, newFrame, newFrame)
	indexed := make([]byte, 16)
	indexed[0], indexed[1] = 8, 8
	format8 := vncFixturePixelFormat(indexed)
	f.transfer(t, f.viewer, f.backend, format8, format8)
	palette := []byte{1, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255}
	f.transfer(t, f.backend, f.viewer, palette, palette)
	f.request(t)
	indexedFrame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 2, 2, []byte{0, 1, 1, 0}))
	f.transfer(t, f.backend, f.viewer, indexedFrame, indexedFrame)
	f.closeCleanly(t)
}

func TestBridgeReadOnlyVNCSerializesRequests(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, nil)
	f.request(t)
	next := vncFixtureWrite(f.viewer, vncFixtureRequest())
	vncFixtureWaitWrite(t, next)
	empty := vncFixtureFrame()
	f.transfer(t, f.backend, f.viewer, empty, empty)
	vncFixtureExpect(t, f.backend, vncFixtureRequest())
	f.transfer(t, f.backend, f.viewer, empty, empty)
	f.closeCleanly(t)
}

func TestBridgeReadOnlyVNCRejectsHandshake(t *testing.T) {
	text := func(value string) []byte {
		message := make([]byte, 4)
		binary.BigEndian.PutUint32(message, uint32(len(value)))
		return append(message, value...)
	}
	largeText := make([]byte, 4)
	binary.BigEndian.PutUint32(largeText, readOnlyVNCMaxText+1)
	longName := vncFixtureInit(8, 8, vncFixtureFormat(32))
	binary.BigEndian.PutUint32(longName[20:24], readOnlyVNCMaxText+1)
	cases := []struct {
		name      string
		stage     string
		viewer    bool
		data      []byte
		truncated bool
		code      domain.Code
	}{
		{"unsupported-server-version", "server-banner", false, []byte("RFB 004.000\n"), false, domain.Invalid},
		{"nonstandard-server-version", "server-banner", false, []byte("RFB 003.889\n"), false, domain.Invalid},
		{"truncated-server-version", "server-banner", false, []byte("RFB 003."), true, domain.Invalid},
		{"unsupported-viewer-version", "viewer-banner", true, []byte("RFB 003.009\n"), false, domain.Invalid},
		{"malformed-viewer-version", "viewer-banner", true, []byte("RFB 003.008!"), false, domain.Invalid},
		{"truncated-viewer-version", "viewer-banner", true, []byte("RFB 003."), true, domain.Invalid},
		{"newer-viewer-version", "viewer-banner-3", true, vncFixtureBanner(8), false, domain.Invalid},
		{"encrypted-security-3.3", "security-3", false, []byte{0, 0, 0, 19}, false, domain.Denied},
		{"extended-security-3.3", "security-3", false, []byte{0, 0, 0, 16}, false, domain.Denied},
		{"security-failure-3.3", "security-3", false, append([]byte{0, 0, 0, 0}, text("private-reason-marker")...), false, domain.Denied},
		{"encrypted-security-3.8", "security-8", false, []byte{3, 18, 19, 16}, false, domain.Denied},
		{"truncated-security-list", "security-8", false, []byte{2, 1}, true, domain.Invalid},
		{"security-failure-3.8", "security-8", false, append([]byte{0}, text("private-reason-marker")...), false, domain.Denied},
		{"oversize-security-failure", "security-8", false, append([]byte{0}, largeText...), false, domain.Invalid},
		{"encrypted-security-selection", "selection-8", true, []byte{19}, false, domain.Denied},
		{"unoffered-security-selection", "selection-8", true, []byte{2}, false, domain.Denied},
		{"truncated-challenge", "challenge", false, []byte("partial"), true, domain.Invalid},
		{"truncated-response", "response", true, []byte("partial"), true, domain.Invalid},
		{"truncated-security-result", "auth-result", false, []byte{0, 0}, true, domain.Invalid},
		{"unsupported-security-result", "auth-result", false, []byte{0, 0, 0, 3}, false, domain.Invalid},
		{"auth-failure-3.8", "auth-result", false, append([]byte{0, 0, 0, 1}, text("private-reason-marker")...), false, domain.Denied},
		{"none-failure-3.8", "none-result", false, append([]byte{0, 0, 0, 1}, text("private-reason-marker")...), false, domain.Denied},
		{"oversize-auth-failure", "auth-result", false, append([]byte{0, 0, 0, 1}, largeText...), false, domain.Invalid},
		{"truncated-auth-failure", "auth-result", false, []byte{0, 0, 0, 1, 0, 0, 0, 5, 'x'}, true, domain.Invalid},
		{"truncated-server-init", "init", false, vncFixtureInit(8, 8, vncFixtureFormat(32))[:10], true, domain.Invalid},
		{"truncated-desktop-name", "init", false, vncFixtureInit(8, 8, vncFixtureFormat(32))[:26], true, domain.Invalid},
		{"oversize-desktop-name", "init", false, longName, false, domain.Invalid},
		{"oversize-dimension", "init", false, vncFixtureInit(16385, 1, vncFixtureFormat(32)), false, domain.Invalid},
		{"oversize-pixels", "init", false, vncFixtureInit(4097, 4097, vncFixtureFormat(32)), false, domain.Invalid},
		{"empty-framebuffer", "init", false, vncFixtureInit(0, 8, vncFixtureFormat(32)), false, domain.Invalid},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			switch item.stage {
			case "server-banner":
			case "viewer-banner":
				f.transfer(t, f.backend, f.viewer, vncFixtureBanner(8), vncFixtureBanner(8))
			case "viewer-banner-3":
				f.transfer(t, f.backend, f.viewer, vncFixtureBanner(3), vncFixtureBanner(3))
			case "security-3":
				f.versions(t, 3, 3)
			default:
				f.versions(t, 8, 8)
				switch item.stage {
				case "security-8":
				case "selection-8":
					f.transfer(t, f.backend, f.viewer, []byte{1, 1}, []byte{1, 1})
				case "none-result", "init":
					f.security(t, 8, 1)
					if item.stage == "init" {
						f.transfer(t, f.backend, f.viewer, []byte{0, 0, 0, 0}, []byte{0, 0, 0, 0})
						f.transfer(t, f.viewer, f.backend, []byte{0}, []byte{1})
					}
				default:
					f.security(t, 8, 2)
					if item.stage == "response" {
						f.transfer(t, f.backend, f.viewer, bytes.Repeat([]byte{255}, 16), bytes.Repeat([]byte{255}, 16))
					} else if item.stage == "auth-result" {
						f.opaqueAuth(t)
					}
				}
			}
			src := f.backend
			if item.viewer {
				src = f.viewer
			}
			err := f.failPacket(t, src, item.data, item.truncated, item.code)
			for _, secret := range []string{"private-reason-marker", "opaque-response!", "challenge-bytes!"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("authentication data escaped through a protocol error")
				}
			}
		})
	}
	for _, version := range []int{3, 7} {
		t.Run(fmt.Sprintf("auth-failure-3.%d", version), func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			f.versions(t, version, version)
			f.security(t, version, 2)
			f.opaqueAuth(t)
			f.failPacket(t, f.backend, []byte{0, 0, 0, 1}, false, domain.Denied)
		})
	}
}

func TestBridgeReadOnlyVNCPixelFormatValidation(t *testing.T) {
	for _, item := range []struct {
		name   string
		offset int
		value  byte
	}{
		{"unsupported-bits", 0, 24},
		{"zero-depth", 1, 0},
		{"oversize-depth", 1, 33},
		{"insufficient-depth", 1, 8},
		{"invalid-endianness", 2, 2},
		{"invalid-colour-mode", 3, 2},
		{"zero-maximum", 5, 0},
		{"invalid-maximum", 5, 254},
		{"overlapping-channels", 10, 8},
		{"overflowing-channel", 10, 31},
		{"overflowing-shift", 10, 255},
	} {
		for _, initial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/initial-%t", item.name, initial), func(t *testing.T) {
				f := newVNCFixture(t, BridgeReadOnlyVNC)
				format := vncFixtureFormat(32)
				format[item.offset] = item.value
				src, message := f.viewer, vncFixturePixelFormat(format)
				if initial {
					f.versions(t, 3, 3)
					f.security(t, 3, 1)
					f.transfer(t, f.viewer, f.backend, []byte{0}, []byte{1})
					src, message = f.backend, vncFixtureInit(8, 8, format)
				} else {
					f.handshake(t, 8, 8, 1, nil)
				}
				f.failPacket(t, src, message, false, domain.Invalid)
			})
		}
	}
	for _, pixelBits := range []byte{8, 16, 32} {
		t.Run(fmt.Sprintf("valid-big-endian-%d", pixelBits), func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			format := vncFixtureFormat(pixelBits)
			format[2] = 1
			f.handshake(t, 8, 8, 1, vncFixtureInit(8, 8, format))
			f.request(t)
			frame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 1, 1, bytes.Repeat([]byte{3}, int(pixelBits/8))))
			f.transfer(t, f.backend, f.viewer, frame, frame)
			f.closeCleanly(t)
		})
	}
}

func TestBridgeReadOnlyVNCRejectsMalformedMessages(t *testing.T) {
	textHeader := func(kind byte, size uint32) []byte {
		message := make([]byte, 8)
		message[0] = kind
		binary.BigEndian.PutUint32(message[4:8], size)
		return message
	}
	tooManyRectangles := vncFixtureFrame()
	binary.BigEndian.PutUint16(tooManyRectangles[2:4], readOnlyVNCMaxRectangles+1)
	cases := []struct {
		name      string
		server    bool
		request   bool
		data      []byte
		truncated bool
		code      domain.Code
	}{
		{"unknown-client", false, false, []byte{150}, false, domain.Denied},
		{"unknown-extended-input", false, false, []byte{255, 1}, false, domain.Denied},
		{"invalid-key-flag", false, false, []byte{4, 2, 0, 0, 0, 0, 0, 65}, false, domain.Invalid},
		{"invalid-extended-key-flag", false, false, []byte{255, 0, 0, 2, 0, 0, 0, 65, 0, 0, 0, 1}, false, domain.Invalid},
		{"invalid-incremental", false, false, []byte{3, 2, 0, 0, 0, 0, 0, 1, 0, 1}, false, domain.Invalid},
		{"oversize-encodings", false, false, []byte{2, 0, 1, 1}, false, domain.Invalid},
		{"oversize-client-clipboard", false, false, textHeader(6, readOnlyVNCMaxText+1), false, domain.Invalid},
		{"extended-client-clipboard", false, false, textHeader(6, 0xffffffff), false, domain.Invalid},
		{"truncated-pixel-format", false, false, []byte{0, 0, 0, 0, 32}, true, domain.Invalid},
		{"truncated-encodings-header", false, false, []byte{2, 0, 0}, true, domain.Invalid},
		{"truncated-encoding-list", false, false, []byte{2, 0, 0, 2, 0, 0, 0, 0}, true, domain.Invalid},
		{"truncated-request", false, false, []byte{3, 0, 0}, true, domain.Invalid},
		{"truncated-key", false, false, []byte{4, 1, 0}, true, domain.Invalid},
		{"truncated-pointer", false, false, []byte{5, 1, 0}, true, domain.Invalid},
		{"truncated-client-clipboard-header", false, false, []byte{6, 0}, true, domain.Invalid},
		{"truncated-client-clipboard", false, false, append(textHeader(6, 5), 'x'), true, domain.Invalid},
		{"truncated-resize-header", false, false, []byte{251, 0, 0}, true, domain.Invalid},
		{"truncated-resize-screens", false, false, []byte{251, 0, 0, 8, 0, 8, 1, 0, 0}, true, domain.Invalid},
		{"truncated-power", false, false, []byte{250, 0}, true, domain.Invalid},
		{"truncated-extended-subtype", false, false, []byte{255}, true, domain.Invalid},
		{"truncated-extended-key", false, false, []byte{255, 0, 0}, true, domain.Invalid},
		{"unknown-server", true, false, []byte{248}, false, domain.Invalid},
		{"unsolicited-update", true, false, vncFixtureFrame(), false, domain.Invalid},
		{"oversize-server-clipboard", true, false, textHeader(3, readOnlyVNCMaxText+1), false, domain.Invalid},
		{"extended-server-clipboard", true, false, textHeader(3, 0x80000004), false, domain.Invalid},
		{"invalid-colour-map", true, false, []byte{1, 0, 255, 255, 0, 2}, false, domain.Invalid},
		{"truncated-colour-map-header", true, false, []byte{1, 0, 0}, true, domain.Invalid},
		{"truncated-colour-map", true, false, []byte{1, 0, 0, 0, 0, 1, 0}, true, domain.Invalid},
		{"truncated-server-clipboard-header", true, false, []byte{3, 0}, true, domain.Invalid},
		{"truncated-server-clipboard", true, false, append(textHeader(3, 5), 'x'), true, domain.Invalid},
		{"truncated-server-power", true, false, []byte{250, 0}, true, domain.Invalid},
		{"truncated-update-header", true, true, []byte{0, 0, 0}, true, domain.Invalid},
		{"oversize-rectangle-count", true, true, tooManyRectangles, false, domain.Invalid},
		{"unsupported-encoding", true, true, vncFixtureFrame(vncFixtureRect(7, 0, 0, 1, 1, nil)), false, domain.Invalid},
		{"unsupported-pseudo-encoding", true, true, vncFixtureFrame(vncFixtureRect(-308, 0, 0, 1, 1, nil)), false, domain.Invalid},
		{"truncated-rectangle", true, true, []byte{0, 0, 0, 1, 0, 0, 0}, true, domain.Invalid},
		{"truncated-pixels", true, true, vncFixtureFrame(vncFixtureRect(0, 0, 0, 1, 1, []byte{0, 1, 2})), true, domain.Invalid},
		{"out-of-bounds-pixels", true, true, vncFixtureFrame(vncFixtureRect(0, 8, 0, 1, 1, nil)), false, domain.Invalid},
		{"overflowing-rectangle-coordinate", true, true, vncFixtureFrame(vncFixtureRect(0, 65535, 0, 2, 1, nil)), false, domain.Invalid},
		{"truncated-CopyRect", true, true, vncFixtureFrame(vncFixtureRect(1, 0, 0, 1, 1, []byte{0, 0})), true, domain.Invalid},
		{"out-of-bounds-CopyRect", true, true, vncFixtureFrame(vncFixtureRect(1, 0, 0, 1, 1, []byte{0, 8, 0, 0})), false, domain.Invalid},
		{"oversize-desktop-resize", true, true, vncFixtureFrame(vncFixtureRect(-223, 0, 0, 16385, 1, nil)), false, domain.Invalid},
		{"oversize-desktop-pixels", true, true, vncFixtureFrame(vncFixtureRect(-223, 0, 0, 4097, 4097, nil)), false, domain.Invalid},
		{"oversize-cursor", true, true, vncFixtureFrame(vncFixtureRect(-239, 0, 0, 1025, 1, nil)), false, domain.Invalid},
		{"invalid-cursor-hotspot", true, true, vncFixtureFrame(vncFixtureRect(-239, 1, 0, 1, 1, nil)), false, domain.Invalid},
		{"invalid-empty-cursor", true, true, vncFixtureFrame(vncFixtureRect(-239, 0, 0, 0, 1, nil)), false, domain.Invalid},
		{"truncated-cursor-mask", true, true, vncFixtureFrame(vncFixtureRect(-239, 0, 0, 1, 1, []byte{0, 1, 2, 3})), true, domain.Invalid},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			f.handshake(t, 8, 8, 1, nil)
			if item.request {
				f.request(t)
			}
			src := f.viewer
			if item.server {
				src = f.backend
			}
			f.failPacket(t, src, item.data, item.truncated, item.code)
		})
	}
}

func TestBridgeReadOnlyVNCBoundedTextAndEncodingBoundary(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, nil)
	text := bytes.Repeat([]byte{3}, readOnlyVNCMaxText)
	marker := vncFixtureEncodings(0)
	f.transfer(t, f.viewer, f.backend, vncFixtureJoin(vncFixtureCutText(6, text), marker), marker)
	f.transfer(t, f.backend, f.viewer, vncFixtureJoin(vncFixtureCutText(3, text), []byte{2}), []byte{2})
	encodings := make([]int32, readOnlyVNCMaxEncodings)
	for i := range encodings {
		encodings[i] = 7
	}
	f.transfer(t, f.viewer, f.backend, vncFixtureEncodings(encodings...), marker)
	f.closeCleanly(t)
}

func TestBridgeReadOnlyVNCLastRectBoundary(t *testing.T) {
	for _, terminated := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminated-%t", terminated), func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			f.handshake(t, 8, 8, 1, nil)
			f.request(t)
			frame := []byte{0, 0, 255, 255}
			for range readOnlyVNCMaxRectangles - 1 {
				frame = append(frame, vncFixtureRect(0, 0, 0, 0, 0, nil)...)
			}
			if terminated {
				frame = append(frame, vncFixtureRect(-224, 0, 0, 0, 0, nil)...)
				f.transfer(t, f.backend, f.viewer, frame, frame)
				f.closeCleanly(t)
			} else {
				frame = append(frame, vncFixtureRect(0, 0, 0, 0, 0, nil)...)
				f.failPacket(t, f.backend, frame, false, domain.Invalid)
			}
		})
	}
}

type vncFixtureZeroReader struct{}

func (vncFixtureZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestBridgeReadOnlyVNCUpdateByteLimit(t *testing.T) {
	f := newVNCFixture(t, BridgeReadOnlyVNC)
	f.handshake(t, 8, 8, 1, vncFixtureInit(4096, 4096, vncFixtureFormat(32)))
	f.request(t)
	first := vncFixtureFrame(vncFixtureRect(0, 0, 0, 4096, 4096, nil))
	binary.BigEndian.PutUint16(first[2:4], 2)
	stream := io.MultiReader(bytes.NewReader(first),
		io.LimitReader(vncFixtureZeroReader{}, readOnlyVNCMaxUpdate),
		bytes.NewReader(vncFixtureRect(0, 0, 0, 1, 1, nil)))
	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(f.backend, stream)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(io.Discard, f.viewer)
		finished <- struct{}{}
	}()
	if err := f.wait(t); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("oversize aggregate framebuffer update was not rejected")
	}
	for range 2 {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("oversize framebuffer fixture did not join")
		}
	}
}

func TestBridgeReadOnlyVNCCancellationJoinsEveryPhase(t *testing.T) {
	for _, phase := range []string{
		"initial-read", "partial-version", "handshake-viewer-write", "auth-response-write",
		"idle-streams", "client-backend-write", "pixel-format-wait", "request-wait",
		"server-viewer-write", "server-pixel-read",
	} {
		t.Run(phase, func(t *testing.T) {
			f := newVNCFixture(t, BridgeReadOnlyVNC)
			switch phase {
			case "initial-read":
			case "partial-version":
				vncFixtureWaitWrite(t, vncFixtureWrite(f.backend, []byte("RFB 003.")))
			case "handshake-viewer-write":
				vncFixtureWaitWrite(t, vncFixtureWrite(f.backend, vncFixtureBanner(8)))
			case "auth-response-write":
				f.versions(t, 8, 8)
				f.security(t, 8, 2)
				challenge := bytes.Repeat([]byte{3}, 16)
				f.transfer(t, f.backend, f.viewer, challenge, challenge)
				vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, bytes.Repeat([]byte{6}, 16)))
			default:
				f.handshake(t, 8, 8, 1, nil)
				switch phase {
				case "client-backend-write":
					vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, vncFixturePixelFormat(vncFixtureFormat(16))))
				case "pixel-format-wait":
					f.request(t)
					vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, vncFixturePixelFormat(vncFixtureFormat(16))))
				case "request-wait":
					f.request(t)
					vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, vncFixtureRequest()))
				case "server-viewer-write":
					f.request(t)
					vncFixtureWaitWrite(t, vncFixtureWrite(f.backend, []byte{0, 0, 0, 1}))
				case "server-pixel-read":
					f.request(t)
					header := vncFixtureFrame(vncFixtureRect(0, 0, 0, 1, 1, nil))
					f.transfer(t, f.backend, f.viewer, header, header)
				}
			}
			f.cancel()
			if err := f.wait(t); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation did not retain its context error")
			}
		})
	}
}

func TestBridgeReadOnlyVNCDeadlineAndBackendEOF(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
			bounded, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
			defer cancel()
			return BridgeReadOnlyVNC(bounded, viewer, backend)
		})
		if err := f.wait(t); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("deadline did not retain its context error")
		}
	})
	t.Run("backend-EOF", func(t *testing.T) {
		f := newVNCFixture(t, BridgeReadOnlyVNC)
		f.handshake(t, 8, 8, 1, nil)
		f.backend.Close()
		if err := f.wait(t); err != nil {
			t.Fatal("clean backend EOF was not accepted")
		}
	})
	t.Run("missing-connections", func(t *testing.T) {
		err := BridgeReadOnlyVNC(context.Background(), nil, nil)
		if err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("missing connections were accepted")
		}
	})
}

type vncFixtureShortWriter struct {
	io.ReadWriteCloser
}

func (vncFixtureShortWriter) Write([]byte) (int, error) {
	return 0, nil
}

type vncFixtureFailedCloser struct {
	io.ReadWriteCloser
}

func (c vncFixtureFailedCloser) Close() error {
	_ = c.ReadWriteCloser.Close()
	return errors.New("private-transport-marker")
}

func TestBridgeReadOnlyVNCTransportErrorsStayOpaque(t *testing.T) {
	t.Run("short-write", func(t *testing.T) {
		f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
			return BridgeReadOnlyVNC(ctx, vncFixtureShortWriter{viewer}, backend)
		})
		f.failPacket(t, f.backend, vncFixtureBanner(8), false, domain.Unavailable)
	})
	t.Run("close-error", func(t *testing.T) {
		f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
			return BridgeReadOnlyVNC(ctx, viewer, vncFixtureFailedCloser{backend})
		})
		f.handshake(t, 8, 8, 1, nil)
		f.viewer.Close()
		err := f.wait(t)
		if err == nil || domain.AsFault(err).Code != domain.Unavailable {
			t.Fatal("connection teardown error was lost")
		}
		if strings.Contains(err.Error(), "private-transport-marker") {
			t.Fatal("raw connection error escaped the VNC boundary")
		}
	})
}

type vncFixtureCapture struct {
	mu   sync.Mutex
	data []byte
}

func (c *vncFixtureCapture) snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.data)
}

type vncFixtureCapturedConn struct {
	io.ReadWriteCloser
	capture *vncFixtureCapture
}

func (c vncFixtureCapturedConn) Write(data []byte) (int, error) {
	n, err := c.ReadWriteCloser.Write(data)
	c.capture.mu.Lock()
	c.capture.data = append(c.capture.data, data[:n]...)
	c.capture.mu.Unlock()
	return n, err
}

func newAuthenticatedVNCFixture(t *testing.T, password []byte, allowed func() bool) (*vncFixture, *vncFixtureCapture, *vncFixtureCapture) {
	t.Helper()
	viewerWire, backendWire := &vncFixtureCapture{}, &vncFixtureCapture{}
	f := newVNCFixture(t, func(ctx context.Context, viewer, backend io.ReadWriteCloser) error {
		return BridgeAuthenticatedVNC(ctx,
			vncFixtureCapturedConn{viewer, viewerWire},
			vncFixtureCapturedConn{backend, backendWire}, password, allowed)
	})
	return f, viewerWire, backendWire
}

func vncFixtureAuthChallenge() []byte {
	return []byte{0x13, 0x24, 0x35, 0x46, 0x57, 0x68, 0x79, 0x8a, 0x9b, 0xac, 0xbd, 0xce, 0xdf, 0xe0, 0xf1, 0x02}
}

func vncFixtureAuthResponse(t *testing.T, reversedKey []byte) []byte {
	t.Helper()
	block, err := des.NewCipher(reversedKey)
	if err != nil {
		t.Fatal("could not initialize synthetic VNCAuth fixture")
	}
	challenge := vncFixtureAuthChallenge()
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	return response
}

func (f *vncFixture) targetSecurity(t *testing.T, version int) {
	t.Helper()
	if version == 3 {
		vncFixtureWaitWrite(t, vncFixtureWrite(f.backend, []byte{0, 0, 0, 2}))
	} else {
		f.transfer(t, f.backend, f.backend, []byte{4, 19, 1, 2, 18}, []byte{2})
	}
}

func (f *vncFixture) targetResponse(t *testing.T, response []byte) {
	t.Helper()
	f.transfer(t, f.backend, f.backend, vncFixtureAuthChallenge(), response)
}

func (f *vncFixture) targetAccepted(t *testing.T, version int, init []byte) {
	t.Helper()
	none := []byte{1, 1}
	if version == 3 {
		none = []byte{0, 0, 0, 1}
	}
	f.transfer(t, f.backend, f.viewer, []byte{0, 0, 0, 0}, none)
	if version == 8 {
		f.transfer(t, f.viewer, f.viewer, []byte{1}, []byte{0, 0, 0, 0})
	} else if version == 7 {
		vncFixtureWaitWrite(t, vncFixtureWrite(f.viewer, []byte{1}))
	}
	f.transfer(t, f.viewer, f.backend, []byte{0}, []byte{1})
	if init == nil {
		init = vncFixtureInit(8, 8, vncFixtureFormat(32))
	}
	f.transfer(t, f.backend, f.viewer, init, init)
}

func vncFixtureTargetWrites(version int, response []byte, initialized bool) []byte {
	message := vncFixtureBanner(version)
	if version != 3 {
		message = append(message, 2)
	}
	message = append(message, response...)
	if initialized {
		message = append(message, 1)
	}
	return message
}

func TestBridgeAuthenticatedVNCHandshakeKeepsCredentialsTargetSide(t *testing.T) {
	for _, item := range []struct {
		name     string
		password []byte
		key      []byte
	}{
		{"one-byte", []byte("q"), []byte{0x8e, 0, 0, 0, 0, 0, 0, 0}},
		{"zero-padded", []byte("abc"), []byte{0x86, 0x46, 0xc6, 0, 0, 0, 0, 0}},
		{"eight-bytes", []byte("testpass"), []byte{0x2e, 0xa6, 0xce, 0x2e, 0x0e, 0x86, 0xce, 0xce}},
	} {
		for _, versions := range [][2]int{{3, 3}, {7, 7}, {8, 8}, {8, 3}, {8, 7}} {
			t.Run(fmt.Sprintf("%s/server-%d/viewer-%d", item.name, versions[0], versions[1]), func(t *testing.T) {
				password := bytes.Clone(item.password)
				f, viewerWire, backendWire := newAuthenticatedVNCFixture(t, password, nil)
				response := vncFixtureAuthResponse(t, item.key)
				f.versions(t, versions[0], versions[1])
				f.targetSecurity(t, versions[1])
				f.targetResponse(t, response)
				if !bytes.Equal(viewerWire.snapshot(), vncFixtureBanner(versions[0])) {
					t.Fatal("backend security bytes reached the viewer before target authentication")
				}
				init := vncFixtureInit(8, 8, vncFixtureFormat(32))
				f.targetAccepted(t, versions[1], init)
				f.closeCleanly(t)
				wantViewer := vncFixtureBanner(versions[0])
				if versions[1] == 3 {
					wantViewer = append(wantViewer, 0, 0, 0, 1)
				} else {
					wantViewer = append(wantViewer, 1, 1)
				}
				if versions[1] == 8 {
					wantViewer = append(wantViewer, 0, 0, 0, 0)
				}
				wantViewer = append(wantViewer, init...)
				wire := viewerWire.snapshot()
				if !bytes.Equal(wire, wantViewer) {
					t.Fatal("viewer handshake was not exactly None-security RFB initialization")
				}
				for _, secret := range [][]byte{item.password, vncFixtureAuthChallenge(), response} {
					if bytes.Contains(wire, secret) {
						t.Fatal("target authentication data reached the viewer")
					}
				}
				if !bytes.Equal(backendWire.snapshot(), vncFixtureTargetWrites(versions[1], response, true)) {
					t.Fatal("target authentication was retried, omitted, or exposed extra backend bytes")
				}
				if !bytes.Equal(password, item.password) {
					t.Fatal("target authentication modified caller-owned password memory")
				}
			})
		}
	}
}

func TestBridgeAuthenticatedVNCWrongCredentialsAreGenericAndNotRetried(t *testing.T) {
	password := []byte("badpass")
	response := vncFixtureAuthResponse(t, []byte{0x46, 0x86, 0x26, 0x0e, 0x86, 0xce, 0xce, 0})
	correct := vncFixtureAuthResponse(t, []byte{0x2e, 0xa6, 0xce, 0x2e, 0x0e, 0x86, 0xce, 0xce})
	if bytes.Equal(response, correct) {
		t.Fatal("invalid wrong-password fixture")
	}
	for _, version := range []int{3, 7, 8} {
		for _, status := range []byte{1, 2, 3} {
			t.Run(fmt.Sprintf("3.%d/status-%d", version, status), func(t *testing.T) {
				f, viewerWire, backendWire := newAuthenticatedVNCFixture(t, password, nil)
				f.versions(t, version, version)
				f.targetSecurity(t, version)
				f.targetResponse(t, response)
				reason := vncFixtureJoin([]byte("private-backend-reason"), password, vncFixtureAuthChallenge(), response)
				length := make([]byte, 4)
				binary.BigEndian.PutUint32(length, uint32(len(reason)))
				failure := vncFixtureJoin([]byte{0, 0, 0, status}, length, reason)
				err := f.failPacket(t, f.backend, failure, false, domain.Denied)
				if domain.AsFault(err).Message != "target-owned VNC authentication failed" {
					t.Fatal("backend credential rejection did not return the generic fault")
				}
				if !bytes.Equal(viewerWire.snapshot(), vncFixtureBanner(version)) {
					t.Fatal("backend challenge, response or rejection reached the viewer")
				}
				if !bytes.Equal(backendWire.snapshot(), vncFixtureTargetWrites(version, response, false)) {
					t.Fatal("failed target authentication submitted credentials more than once")
				}
				for _, secret := range [][]byte{password, reason, response, vncFixtureAuthChallenge()} {
					if bytes.Contains([]byte(err.Error()), secret) {
						t.Fatal("target authentication bytes escaped in an error")
					}
				}
			})
		}
	}
}

func TestBridgeAuthenticatedVNCRejectsUnsupportedBackendSecurity(t *testing.T) {
	for _, item := range []struct {
		name    string
		version int
		offer   []byte
	}{
		{"none-only-3.3", 3, []byte{0, 0, 0, 1}},
		{"encrypted-3.3", 3, []byte{0, 0, 0, 19}},
		{"failure-3.3", 3, []byte{0, 0, 0, 0, 0, 0, 0, 255}},
		{"none-only-3.7", 7, []byte{1, 1}},
		{"encrypted-3.7", 7, []byte{2, 18, 19}},
		{"none-only-3.8", 8, []byte{1, 1}},
		{"encrypted-3.8", 8, []byte{3, 18, 19, 16}},
		{"failure-3.8", 8, []byte{0, 255, 255, 255, 255}},
	} {
		t.Run(item.name, func(t *testing.T) {
			f, viewerWire, backendWire := newAuthenticatedVNCFixture(t, []byte("testpass"), nil)
			f.versions(t, item.version, item.version)
			err := f.failPacket(t, f.backend, item.offer, false, domain.Denied)
			if domain.AsFault(err).Message != "target-owned VNC authentication failed" {
				t.Fatal("unsupported target security did not return the generic fault")
			}
			if !bytes.Equal(viewerWire.snapshot(), vncFixtureBanner(item.version)) ||
				!bytes.Equal(backendWire.snapshot(), vncFixtureBanner(item.version)) {
				t.Fatal("unsupported security fell back or exposed authentication bytes")
			}
		})
	}
}

func TestBridgeAuthenticatedVNCRejectsInvalidPasswordWithoutIO(t *testing.T) {
	for _, length := range []int{0, 9} {
		t.Run(fmt.Sprintf("length-%d", length), func(t *testing.T) {
			f, viewerWire, backendWire := newAuthenticatedVNCFixture(t, bytes.Repeat([]byte{'a'}, length), nil)
			if err := f.wait(t); err == nil || domain.AsFault(err).Code != domain.Invalid {
				t.Fatal("invalid target password length was accepted")
			}
			if len(viewerWire.snapshot()) != 0 || len(backendWire.snapshot()) != 0 {
				t.Fatal("invalid target password started a protocol exchange")
			}
		})
	}
}

func TestBridgeAuthenticatedVNCPreservesViewAndInputGates(t *testing.T) {
	var allowed atomic.Bool
	f, _, _ := newAuthenticatedVNCFixture(t, []byte("testpass"), allowed.Load)
	response := vncFixtureAuthResponse(t, []byte{0x2e, 0xa6, 0xce, 0x2e, 0x0e, 0x86, 0xce, 0xce})
	f.versions(t, 8, 8)
	f.targetSecurity(t, 8)
	f.targetResponse(t, response)
	f.targetAccepted(t, 8, nil)
	format := vncFixturePixelFormat(vncFixtureFormat(16))
	f.transfer(t, f.viewer, f.backend, format, format)
	key, pointer, extended, resize, power := vncFixtureInputs()
	marker := vncFixtureEncodings(0)
	requested := vncFixtureEncodings(7, -309, 0)
	for _, permit := range []bool{false, true, false} {
		allowed.Store(permit)
		batch := vncFixtureJoin(key, pointer, extended, resize, power,
			vncFixtureCutText(6, []byte("private-client-clipboard")), requested)
		want := marker
		if permit {
			want = vncFixtureJoin(key, pointer, extended, marker)
		}
		f.transfer(t, f.viewer, f.backend, batch, want)
		f.request(t)
		frame := vncFixtureFrame(vncFixtureRect(0, 0, 0, 2, 2, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
		f.transfer(t, f.backend, f.viewer,
			vncFixtureJoin(vncFixtureCutText(3, []byte("private-server-clipboard")), power, frame, []byte{2}),
			vncFixtureJoin(frame, []byte{2}))
	}
	f.closeCleanly(t)
}

func TestBridgeAuthenticatedVNCTruncationAndViewerSecuritySelection(t *testing.T) {
	response := vncFixtureAuthResponse(t, []byte{0x2e, 0xa6, 0xce, 0x2e, 0x0e, 0x86, 0xce, 0xce})
	for _, phase := range []string{"security-type", "security-count", "security-list", "challenge", "result", "viewer-security"} {
		t.Run(phase, func(t *testing.T) {
			f, viewerWire, _ := newAuthenticatedVNCFixture(t, []byte("testpass"), nil)
			version := 8
			if phase == "security-type" {
				version = 3
			}
			f.versions(t, version, version)
			src, data, truncated, code := f.backend, []byte{0, 0}, true, domain.Invalid
			switch phase {
			case "security-count":
				data = nil
			case "security-list":
				data = []byte{2, 2}
			case "challenge":
				f.targetSecurity(t, version)
				data = vncFixtureAuthChallenge()[:7]
			case "result", "viewer-security":
				f.targetSecurity(t, version)
				f.targetResponse(t, response)
				if phase == "viewer-security" {
					f.transfer(t, f.backend, f.viewer, []byte{0, 0, 0, 0}, []byte{1, 1})
					src, data, truncated, code = f.viewer, []byte{2}, false, domain.Denied
				}
			}
			f.failPacket(t, src, data, truncated, code)
			want := vncFixtureBanner(version)
			if phase == "viewer-security" {
				want = append(want, 1, 1)
			}
			if !bytes.Equal(viewerWire.snapshot(), want) {
				t.Fatal("failed target authentication leaked backend protocol bytes")
			}
		})
	}
}

func TestBridgeAuthenticatedVNCCancellationJoinsAuthentication(t *testing.T) {
	for _, phase := range []string{"challenge-read", "response-write", "result-read", "viewer-security-write", "viewer-selection-read"} {
		t.Run(phase, func(t *testing.T) {
			f, _, _ := newAuthenticatedVNCFixture(t, []byte("testpass"), nil)
			f.versions(t, 8, 8)
			f.targetSecurity(t, 8)
			if phase == "response-write" {
				vncFixtureWaitWrite(t, vncFixtureWrite(f.backend, vncFixtureAuthChallenge()))
			} else if phase != "challenge-read" {
				response := vncFixtureAuthResponse(t, []byte{0x2e, 0xa6, 0xce, 0x2e, 0x0e, 0x86, 0xce, 0xce})
				f.targetResponse(t, response)
				if phase == "viewer-security-write" {
					vncFixtureWaitWrite(t, vncFixtureWrite(f.backend, []byte{0, 0, 0, 0}))
				} else if phase == "viewer-selection-read" {
					f.transfer(t, f.backend, f.viewer, []byte{0, 0, 0, 0}, []byte{1, 1})
				}
			}
			f.cancel()
			if err := f.wait(t); !errors.Is(err, context.Canceled) {
				t.Fatal("target authentication cancellation lost its context error")
			}
		})
	}
}
