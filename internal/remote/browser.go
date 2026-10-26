package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

// BrowserOptions selects a literal loopback listener and an installed, trusted
// noVNC ESM asset tree. AssetsDir is required; assets are never downloaded.
type BrowserOptions struct {
	Address   string
	AssetsDir string
}

const (
	browserIdleTimeout      = 2 * time.Minute
	browserHandshakeTimeout = 10 * time.Second
	browserFrameLimit       = 64 << 10
	browserInputLimit       = 64 << 20
	browserOutputLimit      = 1 << 30
	browserAssetLimit       = 4 << 20
	browserAssetsLimit      = 32 << 20
	browserAssetCount       = 512
)

// AttachBrowserVNC serves one human browser connection until disconnect, idle
// timeout, session expiry or cancellation. Address defaults to 127.0.0.1:0.
// Idle timeout is two minutes; client messages are limited to 64 KiB and total
// session input/output to 64 MiB/1 GiB respectively.
// The caller must supply an explicitly authorized human diagnostic terminal:
// ONLY that writer receives the secret, one-use bootstrap URL. Never send it to
// Foundation messages, model context or structured logs. No browser is launched.
//
// Use the printed literal host AND port, including through SSH forwarding;
// localhost aliases and different-port forwards are deliberately rejected. HTTP
// is loopback-only, so its expiring, path-scoped HttpOnly/SameSite=Strict cookie
// cannot portably use Secure; remote use requires a loopback-bound SSH tunnel.
//
// The installed assets must include LICENSE.txt, core/rfb.js,
// core/util/logging.js and their relative core/ or vendor/ JavaScript imports.
// Files and every ancestor must be real, root/current-user owned and not
// group/other writable. Symlink-based distribution layouts are not supported.
// noVNC remains separately installed MPL-2.0 software, not vendored or modified
// by this original wrapper. Clipboard integration is disabled in both directions.
// Requested VNC endpoint credentials are entered only in the human browser and
// passed directly to noVNC's RFB authentication, never to an HTTP form endpoint,
// URL, storage or Foundation metadata. The form is cleared before handoff and on
// cancellation, authentication failure, connection and teardown.
func AttachBrowserVNC(ctx context.Context, client *api.Client, sessionID string, options BrowserOptions, diagnostic io.Writer) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if client == nil || (!client.IsMessageClient() && client.HTTP == nil) || sessionID == "" || diagnostic == nil {
		return domain.Fail(domain.Invalid, "browser VNC requires a client, session and authorized human diagnostic writer")
	}
	address := options.Address
	if address == "" {
		address = "127.0.0.1:0"
	}
	bind, parseErr := netip.ParseAddrPort(address)
	if parseErr != nil || !bind.Addr().IsLoopback() || bind.Addr().Zone() != "" {
		return domain.Fail(domain.Invalid, "browser VNC address must be a literal loopback IP and numeric port")
	}
	assets, err := loadBrowserAssets(options.AssetsDir)
	if err != nil {
		return err
	}
	session, err := api.Call[domain.Session](ctx, client, "GET", api.Path("sessions", sessionID), nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if session.ID != sessionID || session.Mode != "vnc" {
		return domain.Fail(domain.Invalid, "browser VNC requires the requested VNC session")
	}
	if !session.OpenRequested || (session.State != "consented" && session.State != "active") {
		return domain.Fail(domain.Denied, "VNC session must be consented and explicitly opened")
	}
	expires := time.Unix(session.ExpiresAt, 0)
	if !expires.After(time.Now()) {
		return domain.Fail(domain.Denied, "VNC session has expired")
	}
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		expires = deadline
	}
	secrets := make([]string, 3)
	for i := range secrets {
		var value [32]byte
		if _, err := rand.Read(value[:]); err != nil {
			return domain.Fail(domain.Unavailable, "could not create browser attachment authorization")
		}
		secrets[i] = base64.RawURLEncoding.EncodeToString(value[:])
	}
	listener, err := net.Listen("tcp", bind.String())
	if err != nil {
		return err
	}
	defer listener.Close()
	a := &browserAttachment{
		ctx: ctx, client: client, sessionID: sessionID, host: strings.TrimSuffix(listener.Addr().String(), ":80"),
		prefix: "/viewer/" + secrets[2] + "/", cookieName: "fdn_vnc_" + secrets[2],
		bootstrapHash: sha256.Sum256([]byte(secrets[0])), cookieHash: sha256.Sum256([]byte(secrets[1])),
		cookieValue: secrets[1], expires: expires, assets: assets,
		done: make(chan error, 1), activity: make(chan struct{}, 1),
	}
	server := &http.Server{
		Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: browserHandshakeTimeout,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10,
		ErrorLog:    log.New(io.Discard, "", 0),
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	var serveErr error
	served := make(chan struct{})
	go func() {
		serveErr = server.Serve(&browserListener{Listener: listener, slots: make(chan struct{}, 16)})
		close(served)
	}()
	defer func() {
		a.mu.Lock()
		a.stopping = true
		viewerDone := a.viewerDone
		a.mu.Unlock()
		cancel()
		err = errors.Join(err, server.Close())
		if viewerDone != nil {
			<-viewerDone
		}
		<-served
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
			err = errors.Join(err, domain.Fail(domain.Unavailable, "browser VNC loopback server failed"))
		}
	}()
	if _, writeErr := fmt.Fprintf(diagnostic, "Open this one-use URL ONLY in the authorized human browser:\nhttp://%s/bootstrap?key=%s\nUse the same literal loopback host and port for SSH forwarding; clipboard sharing is disabled.\n", a.host, secrets[0]); writeErr != nil {
		return domain.Fail(domain.Unavailable, "could not write browser VNC human diagnostics")
	}
	secrets = nil
	idle := time.NewTimer(browserIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-a.done:
			return err
		case <-served:
			return domain.Fail(domain.Unavailable, "browser VNC loopback listener stopped")
		case <-idle.C:
			return domain.Fail(domain.Unavailable, "browser VNC attachment reached its idle timeout")
		case <-a.activity:
			idle.Reset(browserIdleTimeout)
		}
	}
}

type browserAttachment struct {
	ctx                       context.Context
	client                    *api.Client
	sessionID, host, prefix   string
	cookieName, cookieValue   string
	bootstrapHash, cookieHash [32]byte
	expires                   time.Time
	assets                    map[string][]byte
	done                      chan error
	activity                  chan struct{}
	mu                        sync.Mutex
	bootstrapped              bool
	reserved, used, stopping  bool
	viewerDone                chan struct{}
}

func (a *browserAttachment) touch() {
	select {
	case a.activity <- struct{}{}:
	default:
	}
}

func (a *browserAttachment) finish(err error) {
	select {
	case a.done <- err:
	default:
	}
}

func (a *browserAttachment) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store, max-age=0")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "clipboard-read=(), clipboard-write=(), camera=(), microphone=(), geolocation=(), display-capture=()")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src ws://"+a.host+"; img-src 'self' data: blob:; font-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; worker-src 'none'")
	if r.Host != a.host || r.URL.IsAbs() || r.URL.Host != "" {
		http.Error(w, "unexpected local host", http.StatusForbidden)
		return
	}
	origins := r.Header.Values("Origin")
	if len(origins) > 1 || (len(origins) == 1 && origins[0] != "http://"+a.host) {
		http.Error(w, "unexpected local origin", http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		h.Set("Connection", "close")
		r.Body = http.MaxBytesReader(w, r.Body, 0)
		http.Error(w, "request bodies are not accepted", http.StatusRequestEntityTooLarge)
		return
	}
	clean := path.Clean(r.URL.Path)
	if r.URL.RawPath != "" || r.URL.EscapedPath() != r.URL.Path || (r.URL.Path != clean && r.URL.Path != clean+"/") || strings.Contains(r.URL.Path, "\\") || strings.Contains(r.URL.Path, "//") || strings.Contains(r.URL.Path, "\x00") {
		http.Error(w, "invalid local path", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/bootstrap" {
		a.bootstrap(w, r)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.Error(w, "unexpected query", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(r.URL.Path, a.prefix) {
		http.NotFound(w, r)
		return
	}
	cookies := r.CookiesNamed(a.cookieName)
	if len(cookies) != 1 || !browserSecretMatches(cookies[0].Value, a.cookieHash) {
		http.Error(w, "browser attachment authorization required", http.StatusForbidden)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, a.prefix)
	if rel == "relay" {
		if r.Method != http.MethodGet || len(origins) != 1 || r.Header.Get("Sec-WebSocket-Protocol") != "binary" {
			http.Error(w, "same-origin binary WebSocket required", http.StatusForbidden)
			return
		}
		a.relay(w, r)
		return
	}
	var body []byte
	switch rel {
	case "":
		body = []byte(browserPage)
		h.Set("Content-Type", "text/html; charset=utf-8")
	case "viewer.js":
		body = []byte(browserScript)
		h.Set("Content-Type", "text/javascript; charset=utf-8")
	case "viewer.css":
		body = []byte(browserStyles)
		h.Set("Content-Type", "text/css; charset=utf-8")
	default:
		if strings.HasPrefix(rel, "assets/") {
			name := strings.TrimPrefix(rel, "assets/")
			body = a.assets[name]
			if name == "LICENSE.txt" {
				h.Set("Content-Type", "text/plain; charset=utf-8")
			} else {
				h.Set("Content-Type", "text/javascript; charset=utf-8")
			}
		}
	}
	if body == nil {
		http.NotFound(w, r)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (a *browserAttachment) bootstrap(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if r.Method != http.MethodGet || len(query) != 1 || len(query["key"]) != 1 || r.URL.RawQuery != "key="+query.Get("key") {
		http.Error(w, "invalid browser bootstrap", http.StatusForbidden)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping || a.bootstrapped || a.ctx.Err() != nil || !browserSecretMatches(query.Get("key"), a.bootstrapHash) {
		http.Error(w, "browser bootstrap denied or already used", http.StatusForbidden)
		return
	}
	a.bootstrapped = true
	a.bootstrapHash = [32]byte{}
	http.SetCookie(w, &http.Cookie{
		Name: a.cookieName, Value: a.cookieValue, Path: a.prefix, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Expires: a.expires.UTC(),
		MaxAge: max(1, int(time.Until(a.expires).Seconds())),
	})
	a.cookieValue = ""
	w.Header().Set("Location", a.prefix)
	w.WriteHeader(http.StatusSeeOther)
	a.touch()
}

func browserSecretMatches(value string, expected [32]byte) bool {
	actual := sha256.Sum256([]byte(value))
	return len(value) == 43 && subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func (a *browserAttachment) relay(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.stopping || a.reserved || a.used || a.ctx.Err() != nil {
		a.mu.Unlock()
		http.Error(w, "browser viewer already connected or stopping", http.StatusConflict)
		return
	}
	a.reserved = true
	viewerDone := make(chan struct{})
	a.viewerDone = viewerDone
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.reserved = false
		close(viewerDone)
		a.mu.Unlock()
	}()
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer socket.CloseNow()
	a.mu.Lock()
	a.used = true
	a.mu.Unlock()
	a.touch()
	relayCtx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	handshake := time.AfterFunc(browserHandshakeTimeout, cancel)
	relay, err := Dial(relayCtx, a.client, a.sessionID, "requester")
	handshake.Stop()
	if err != nil {
		if a.ctx.Err() != nil {
			err = a.ctx.Err()
		}
		a.finish(err)
		return
	}
	defer relay.Close()
	local := websocket.NetConn(a.ctx, socket, websocket.MessageBinary)
	// NetConn disables the library's message limit; restore it AFTER conversion.
	socket.SetReadLimit(browserFrameLimit)
	ending := &atomic.Bool{}
	left := &browserBridgeStream{ReadWriteCloser: relay, ctx: a.ctx, ending: ending, remote: true, limit: browserOutputLimit, touch: a.touch, cancel: cancel}
	right := &browserBridgeStream{ReadWriteCloser: &browserSocket{Conn: local, socket: socket}, ctx: a.ctx, ending: ending, limit: browserInputLimit, touch: a.touch}
	a.finish(Bridge(a.ctx, left, right))
}

type browserSocket struct {
	net.Conn
	socket *websocket.Conn
}

func (s *browserSocket) Close() error { return s.socket.CloseNow() }

type browserBridgeStream struct {
	io.ReadWriteCloser
	ctx       context.Context
	ending    *atomic.Bool
	remote    bool
	limit     int64
	read      int64
	touch     func()
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

func (s *browserBridgeStream) Read(p []byte) (int, error) {
	n, err := s.ReadWriteCloser.Read(p)
	if int64(n) > s.limit-s.read {
		return 0, domain.Fail(domain.Unavailable, "browser VNC session data limit exceeded")
	}
	s.read += int64(n)
	if n > 0 {
		s.touch()
	}
	return n, s.transportError(err)
}

func (s *browserBridgeStream) Write(p []byte) (int, error) {
	n, err := s.ReadWriteCloser.Write(p)
	if n > 0 {
		s.touch()
	}
	return n, s.transportError(err)
}

func (s *browserBridgeStream) transportError(err error) error {
	if err == nil {
		return nil
	}
	if s.ending.Load() {
		return net.ErrClosed
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if !s.remote {
		s.ending.Store(true)
		if err == io.EOF {
			return err
		}
		return domain.Fail(domain.Unavailable, "browser VNC WebSocket interrupted or exceeded its binary message limit")
	}
	// Bridge treats EOF (including wrapped EOF) as normal. A relay disappearing
	// before the viewer disconnects must instead remain an observable failure.
	if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
		return domain.Fail(domain.Denied, "VNC relay identity or current session authority was revoked")
	}
	return domain.Fail(domain.Unavailable, "authenticated VNC relay interrupted; recheck identity, current authority and expiry")
}

func (s *browserBridgeStream) Close() error {
	s.ending.Store(true)
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.closeErr = s.ReadWriteCloser.Close()
		if errors.Is(s.closeErr, net.ErrClosed) || errors.Is(s.closeErr, io.EOF) || errors.Is(s.closeErr, context.Canceled) {
			s.closeErr = nil
		}
	})
	return s.closeErr
}

type browserListener struct {
	net.Listener
	slots chan struct{}
}

func (l *browserListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &browserHTTPConn{Conn: conn, slots: l.slots}, nil
		default:
			conn.Close()
		}
	}
}

type browserHTTPConn struct {
	net.Conn
	slots chan struct{}
	once  sync.Once
	err   error
}

func (c *browserHTTPConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close(); <-c.slots })
	return c.err
}

var browserImports = regexp.MustCompile(`(?m)^\s*(?:import\s+(?:[^;'\"]*?\bfrom\s*)?|export\s+(?:\{[^}]*\}|\*)\s+from\s*)['\"]([^'\"\r\n]+)['\"]`)
var browserDynamicImports = regexp.MustCompile(`\bimport\s*\(\s*['\"]([^'\"\r\n]+)['\"]\s*\)`)
var browserAssetName = regexp.MustCompile(`^(core|vendor)/[A-Za-z0-9_./-]+\.js$`)

func loadBrowserAssets(directory string) (map[string][]byte, error) {
	if directory == "" {
		return nil, domain.Fail(domain.Prerequisite, "browser VNC requires a trusted installed noVNC AssetsDir; no assets are installed automatically")
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || strings.ContainsAny(directory, "\\\x00") {
		return nil, domain.Fail(domain.Invalid, "noVNC AssetsDir must be an absolute, clean directory path")
	}
	root, err := os.OpenFile("/", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, browserAssetError(err)
	}
	if err := checkBrowserAsset(root, true); err != nil {
		root.Close()
		return nil, err
	}
	if directory != "/" {
		next, openErr := openBrowserAsset(root, strings.TrimPrefix(directory, "/"), true)
		root.Close()
		if openErr != nil {
			return nil, openErr
		}
		root = next
	}
	defer root.Close()
	// Snapshot only the module closure and license, not an arbitrary file server.
	// Descriptor-relative O_NOFOLLOW opens protect every component; the immutable
	// bounded snapshot also prevents replacement races after validation.
	assets := make(map[string][]byte)
	pending := []string{"LICENSE.txt", "core/rfb.js", "core/util/logging.js"}
	total := 0
	for len(pending) != 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, ok := assets[name]; ok {
			continue
		}
		if len(assets) >= browserAssetCount {
			return nil, domain.Fail(domain.Prerequisite, "installed noVNC module tree exceeds its file limit")
		}
		file, err := openBrowserAsset(root, name, false)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, browserAssetLimit+1))
		closeErr := file.Close()
		total += len(data)
		if readErr != nil || closeErr != nil || len(data) > browserAssetLimit || total > browserAssetsLimit {
			return nil, domain.Fail(domain.Prerequisite, "installed noVNC assets must be readable and within their size limits")
		}
		assets[name] = data
		if name == "LICENSE.txt" {
			continue
		}
		imports := browserImports.FindAllSubmatch(data, -1)
		imports = append(imports, browserDynamicImports.FindAllSubmatch(data, -1)...)
		if len(imports) > browserAssetCount {
			return nil, domain.Fail(domain.Prerequisite, "installed noVNC module exceeds its import limit")
		}
		for _, match := range imports {
			specifier := string(match[1])
			dependency := path.Join(path.Dir(name), specifier)
			if (!strings.HasPrefix(specifier, "./") && !strings.HasPrefix(specifier, "../")) || !browserAssetName.MatchString(dependency) || path.Clean(dependency) != dependency {
				return nil, domain.Fail(domain.Prerequisite, "installed noVNC imports must be relative JavaScript within core/ or vendor/")
			}
			pending = append(pending, dependency)
		}
	}
	return assets, nil
}

func browserAssetError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return domain.Fail(domain.Denied, "noVNC asset ancestry must not contain symlinks or non-directories")
	}
	return domain.Fail(domain.Prerequisite, "browser VNC requires readable installed noVNC LICENSE.txt, core/rfb.js, core/util/logging.js and their JavaScript dependencies")
}

func openBrowserAsset(root *os.File, name string, directory bool) (*os.File, error) {
	if path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return nil, domain.Fail(domain.Denied, "invalid noVNC asset path")
	}
	current := root
	parts := strings.Split(name, "/")
	for i, part := range parts {
		isDir := i < len(parts)-1 || directory
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if isDir {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(current.Fd()), part, flags, 0)
		if current != root {
			current.Close()
		}
		if err != nil {
			return nil, browserAssetError(err)
		}
		current = os.NewFile(uintptr(fd), part)
		if err := checkBrowserAsset(current, isDir); err != nil {
			current.Close()
			return nil, err
		}
	}
	return current, nil
}

func checkBrowserAsset(file *os.File, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return browserAssetError(err)
	}
	kind := uint32(unix.S_IFREG)
	if directory {
		kind = unix.S_IFDIR
	}
	if uint32(stat.Mode)&unix.S_IFMT != kind || stat.Mode&0022 != 0 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return domain.Fail(domain.Denied, "noVNC assets and ancestry must be real, root/current-user owned and not group/other writable")
	}
	return nil
}

const browserPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Foundation private VNC</title><link rel="stylesheet" href="viewer.css"><script type="module" src="viewer.js"></script></head>
<body><header><span id="status" role="status">Loading trusted, locally installed noVNC...</span><button id="disconnect" type="button">Disconnect</button></header>
<form id="credentials-form" method="post" autocomplete="off" aria-labelledby="credentials-title" hidden>
<h2 id="credentials-title">VNC endpoint authentication</h2>
<p>Type only the requested VNC credentials here. They are sent through this authorized VNC connection; clipboard sharing remains disabled.</p>
<label id="credential-username-label" hidden>Username <input id="credential-username" type="text" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="4096" disabled></label>
<label id="credential-target-label" hidden>Target <input id="credential-target" type="text" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="4096" disabled></label>
<label id="credential-password-label" hidden>Password <input id="credential-password" type="password" autocomplete="off" maxlength="4096" disabled></label>
<div><button id="credential-submit" type="submit" disabled>Authenticate</button> <button id="credential-cancel" type="button">Cancel</button></div></form>
<main id="screen"></main><footer>Clipboard sharing is disabled. noVNC is separately installed MPL-2.0 software; its source and notice obligations remain with that installation. <a href="assets/LICENSE.txt" target="_blank" rel="noopener noreferrer">Installed license</a></footer></body></html>`

const browserScript = `import RFB from './assets/core/rfb.js';
import { initLogging } from './assets/core/util/logging.js';
initLogging('none');

// This original UI has no clipboard controls or browser clipboard integration.
class ClipboardIsolatedRFB extends RFB {
    clipboardPasteFrom() {}
    dispatchEvent(event) {
        if (event.type === 'clipboard') return false;
        return super.dispatchEvent(event);
    }
}
for (const type of ['copy', 'cut', 'paste']) {
    document.addEventListener(type, event => {
        event.preventDefault();
        event.stopImmediatePropagation();
    }, { capture: true });
}
const status = document.getElementById('status');
const credentialForm = document.getElementById('credentials-form');
const credentialSubmit = document.getElementById('credential-submit');
const credentialFields = new Map(['username', 'target', 'password'].map(type => [type, document.getElementById('credential-' + type)]));
let requestedCredentials = [];
function clearCredentials() {
    requestedCredentials = [];
    credentialForm.hidden = true;
    credentialSubmit.disabled = true;
    for (const [type, field] of credentialFields) {
        field.value = '';
        field.disabled = true;
        field.required = false;
        document.getElementById('credential-' + type + '-label').hidden = true;
    }
}
clearCredentials();
for (const type of ['keydown', 'keypress', 'keyup']) {
    credentialForm.addEventListener(type, event => event.stopPropagation());
}
const endpoint = new URL('relay', import.meta.url);
endpoint.protocol = 'ws:';
try {
    const rfb = new ClipboardIsolatedRFB(document.getElementById('screen'), endpoint.href, { shared: false });
    let failureMessage = '';
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
    rfb.addEventListener('connect', () => {
        clearCredentials();
        failureMessage = '';
        rfb.viewOnly = false;
        rfb.focusOnClick = true;
        rfb.focus();
        status.textContent = 'Connected. Clipboard sharing is disabled.';
    });
    rfb.addEventListener('disconnect', event => {
        clearCredentials();
        status.textContent = failureMessage || (event.detail.clean ? 'Disconnected. Close this tab.' : 'VNC transport stopped; check the authorized terminal.');
    });
    rfb.addEventListener('securityfailure', () => {
        clearCredentials();
        failureMessage = 'VNC endpoint authentication failed; disconnect and retry in the authorized human browser.';
        status.textContent = failureMessage;
    });
    rfb.addEventListener('credentialsrequired', event => {
        clearCredentials();
        const types = event.detail && event.detail.types;
        if (!Array.isArray(types) || types.length === 0 || types.length > credentialFields.size || new Set(types).size !== types.length || types.some(type => !credentialFields.has(type))) {
            failureMessage = 'Unsupported VNC credential request; check the endpoint in the authorized terminal.';
            status.textContent = failureMessage;
            rfb.disconnect();
            return;
        }
        failureMessage = '';
        requestedCredentials = [...types];
        rfb.viewOnly = true;
        rfb.focusOnClick = false;
        rfb.blur();
        for (const type of requestedCredentials) {
            const field = credentialFields.get(type);
            field.disabled = false;
            field.required = true;
            document.getElementById('credential-' + type + '-label').hidden = false;
        }
        credentialForm.hidden = false;
        credentialSubmit.disabled = false;
        credentialFields.get(requestedCredentials[0]).focus();
        status.textContent = 'Enter the requested VNC credentials here. Clipboard sharing remains disabled.';
    });
    credentialForm.addEventListener('submit', event => {
        event.preventDefault();
        event.stopPropagation();
        if (!event.isTrusted || requestedCredentials.length === 0) return;
        const credentials = {};
        for (const type of requestedCredentials) {
            const field = credentialFields.get(type);
            if (field.value === '') {
                field.focus();
                return;
            }
            credentials[type] = field.value;
        }
        // noVNC may retain this object until its asynchronous authentication step.
        // Clear every DOM field before handoff, but do not mutate noVNC's object.
        clearCredentials();
        status.textContent = 'Authenticating the VNC endpoint...';
        try {
            rfb.sendCredentials(credentials);
        } catch {
            failureMessage = 'VNC credential submission failed; disconnect and retry in the authorized human browser.';
            status.textContent = failureMessage;
            rfb.disconnect();
        }
    });
    const disconnect = () => { clearCredentials(); rfb.disconnect(); };
    document.getElementById('credential-cancel').addEventListener('click', disconnect);
    document.getElementById('disconnect').addEventListener('click', disconnect);
    addEventListener('pagehide', disconnect, { once: true });
} catch {
    clearCredentials();
    status.textContent = 'Installed noVNC could not start; check its prerequisites in the authorized terminal.';
}
`

const browserStyles = `html,body{height:100%;margin:0;background:#151515;color:#eee;font:14px system-ui,sans-serif}body{display:flex;flex-direction:column}[hidden]{display:none!important}header,footer{padding:0.6rem;display:flex;gap:1rem;align-items:center}header{justify-content:space-between}footer{font-size:12px}a{color:#9ecbff}button,input{font:inherit}#credentials-form{margin:1rem auto;padding:1rem;max-width:36rem;overflow:auto}#credentials-form label{display:grid;gap:0.25rem;margin-bottom:0.75rem}#screen{flex:1;min-height:0;overflow:hidden}`
