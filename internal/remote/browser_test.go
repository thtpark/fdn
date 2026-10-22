package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

// All asset fixtures are original synthetic modules, not upstream noVNC code.
// Keep scratch state inside the checkout, including when TMPDIR is elsewhere.
func browserTestAssets(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".browser-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	files := map[string]string{
		"LICENSE.txt":          "Original synthetic fixture only; no upstream JavaScript is included.\n",
		"core/rfb.js":          "import { marker } from './util/fixture.js';\nimport './util/logging.js';\nexport default class RFB {}\n",
		"core/util/fixture.js": "export { marker } from '../../vendor/fixture.js';\n",
		"core/util/logging.js": "export function initLogging(level) {}\n",
		"vendor/fixture.js":    "export const marker = 'original synthetic module';\n",
	}
	for name, body := range files {
		file := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func TestBrowserRejectsNonLoopbackListen(t *testing.T) {
	client := &api.Client{HTTP: &http.Client{}}
	for _, address := range []string{
		"0.0.0.0:0", ":0", "[::]:0", "192.0.2.1:1234", "localhost:0", "example.invalid:0",
		"127.1:0", "127.0.0.1", "127.0.0.1:http", "127.0.0.1:-1", "127.0.0.1:65536", "[::1%lo0]:0",
	} {
		t.Run(address, func(t *testing.T) {
			err := AttachBrowserVNC(context.Background(), client, "session-browser", BrowserOptions{Address: address}, io.Discard)
			if domain.AsFault(err).Code != domain.Invalid {
				t.Fatalf("unsafe address was not rejected: %v", err)
			}
		})
	}
}

func TestBrowserRejectsUnsafeAssets(t *testing.T) {
	for _, test := range []struct {
		name string
		code domain.Code
		edit func(*testing.T, string) string
	}{
		{"absent", domain.Prerequisite, func(t *testing.T, root string) string { return root + "/absent" }},
		{"empty-option", domain.Prerequisite, func(t *testing.T, root string) string { return "" }},
		{"relative", domain.Invalid, func(t *testing.T, root string) string { return "relative/novnc" }},
		{"unclean", domain.Invalid, func(t *testing.T, root string) string { return root + "/core/.." }},
		{"root-symlink", domain.Denied, func(t *testing.T, root string) string {
			link := filepath.Join(root, "link")
			browserMust(t, os.Symlink(root, link))
			return link
		}},
		{"ancestor-symlink", domain.Denied, func(t *testing.T, root string) string {
			link := filepath.Join(root, "link")
			browserMust(t, os.Symlink(root, link))
			return link + "/core"
		}},
		{"writable-root", domain.Denied, func(t *testing.T, root string) string {
			browserMust(t, os.Chmod(root, 0770))
			return root
		}},
		{"writable-ancestor", domain.Denied, func(t *testing.T, root string) string {
			browserMust(t, os.Chmod(filepath.Join(root, "core"), 0777))
			return root
		}},
		{"writable-module", domain.Denied, func(t *testing.T, root string) string {
			browserMust(t, os.Chmod(filepath.Join(root, "core/rfb.js"), 0666))
			return root
		}},
		{"module-symlink", domain.Denied, func(t *testing.T, root string) string {
			file := filepath.Join(root, "core/rfb.js")
			browserMust(t, os.Remove(file))
			browserMust(t, os.Symlink(filepath.Join(root, "vendor/fixture.js"), file))
			return root
		}},
		{"license-symlink", domain.Denied, func(t *testing.T, root string) string {
			file := filepath.Join(root, "LICENSE.txt")
			browserMust(t, os.Remove(file))
			browserMust(t, os.Symlink(filepath.Join(root, "vendor/fixture.js"), file))
			return root
		}},
		{"dependency-symlink", domain.Denied, func(t *testing.T, root string) string {
			file := filepath.Join(root, "vendor/fixture.js")
			browserMust(t, os.Remove(file))
			browserMust(t, os.Symlink(filepath.Join(root, "core/util/logging.js"), file))
			return root
		}},
		{"module-directory", domain.Denied, func(t *testing.T, root string) string {
			file := filepath.Join(root, "core/rfb.js")
			browserMust(t, os.Remove(file))
			browserMust(t, os.Mkdir(file, 0700))
			return root
		}},
		{"module-fifo", domain.Denied, func(t *testing.T, root string) string {
			file := filepath.Join(root, "core/rfb.js")
			browserMust(t, os.Remove(file))
			browserMust(t, unix.Mkfifo(file, 0600))
			return root
		}},
		{"missing-module", domain.Prerequisite, func(t *testing.T, root string) string {
			browserMust(t, os.Remove(filepath.Join(root, "core/rfb.js")))
			return root
		}},
		{"missing-dependency", domain.Prerequisite, func(t *testing.T, root string) string {
			browserMust(t, os.Remove(filepath.Join(root, "vendor/fixture.js")))
			return root
		}},
		{"missing-license", domain.Prerequisite, func(t *testing.T, root string) string {
			browserMust(t, os.Remove(filepath.Join(root, "LICENSE.txt")))
			return root
		}},
		{"external-import", domain.Prerequisite, func(t *testing.T, root string) string {
			browserMust(t, os.WriteFile(filepath.Join(root, "core/rfb.js"), []byte("import RFB from 'https://example.invalid/rfb.js';\n"), 0600))
			return root
		}},
		{"escaping-import", domain.Prerequisite, func(t *testing.T, root string) string {
			browserMust(t, os.WriteFile(filepath.Join(root, "core/rfb.js"), []byte("import RFB from '../../outside.js';\n"), 0600))
			return root
		}},
		{"oversize-module", domain.Prerequisite, func(t *testing.T, root string) string {
			browserMust(t, os.Truncate(filepath.Join(root, "core/rfb.js"), browserAssetLimit+1))
			return root
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := test.edit(t, browserTestAssets(t))
			_, err := loadBrowserAssets(root)
			if err == nil || domain.AsFault(err).Code != test.code {
				t.Fatalf("wrong asset validation result: %v", err)
			}
		})
	}
}

func TestBrowserAssetsSnapshotRelativeModuleClosure(t *testing.T) {
	root := browserTestAssets(t)
	browserMust(t, os.WriteFile(filepath.Join(root, "core/private.js"), []byte("not a viewer dependency"), 0600))
	browserMust(t, os.WriteFile(filepath.Join(root, "core/rfb.js"), []byte("import { marker,\n renamed } from './util/fixture.js';\nimport('./dynamic.js');\nexport default class RFB {}\n"), 0600))
	browserMust(t, os.WriteFile(filepath.Join(root, "core/dynamic.js"), []byte("export default 'original dynamic fixture';\n"), 0600))
	assets, err := loadBrowserAssets(root)
	browserMust(t, err)
	for _, name := range []string{"LICENSE.txt", "core/rfb.js", "core/util/logging.js", "core/util/fixture.js", "vendor/fixture.js", "core/dynamic.js"} {
		if _, ok := assets[name]; !ok {
			t.Fatalf("relative module closure omitted %s", name)
		}
	}
	if _, ok := assets["core/private.js"]; ok {
		t.Fatal("unrelated installed files were exposed")
	}
	before := append([]byte(nil), assets["core/rfb.js"]...)
	browserMust(t, os.Remove(filepath.Join(root, "core/rfb.js")))
	browserMust(t, os.Symlink(filepath.Join(root, "core/private.js"), filepath.Join(root, "core/rfb.js")))
	if !bytes.Equal(assets["core/rfb.js"], before) {
		t.Fatal("validated asset snapshot changed after file replacement")
	}
}

func browserMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type browserFixture struct {
	client     *api.Client
	session    domain.Session
	denyRelay  int
	waitRelay  bool
	readFault  *domain.Fault
	reads      atomic.Int32
	dials      atomic.Int32
	connected  chan *websocket.Conn
	attempted  chan struct{}
	relayEnded chan struct{}
}

func browserNewFixture(t *testing.T, edit func(*browserFixture)) *browserFixture {
	t.Helper()
	f := &browserFixture{
		session:   domain.Session{ID: "session-browser", RequesterID: "requester-fixture", TargetID: "target-fixture", Mode: "vnc", State: "consented", OpenRequested: true, ExpiresAt: time.Now().Add(time.Minute).Unix()},
		connected: make(chan *websocket.Conn, 4), attempted: make(chan struct{}, 4), relayEnded: make(chan struct{}, 4),
	}
	if edit != nil {
		edit(f)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var sockets []*websocket.Conn
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/session-browser":
			f.reads.Add(1)
			w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
			w.Header().Set("Content-Type", "application/json")
			if f.readFault != nil {
				w.WriteHeader(domain.HTTPStatus(f.readFault))
				_ = json.NewEncoder(w).Encode(api.Envelope{Error: f.readFault})
				return
			}
			body, _ := json.Marshal(f.session)
			_ = json.NewEncoder(w).Encode(api.Envelope{OK: true, Data: body})
		case "/v1/sessions/session-browser/relay/requester":
			f.dials.Add(1)
			f.attempted <- struct{}{}
			defer func() { f.relayEnded <- struct{}{} }()
			if f.waitRelay {
				<-r.Context().Done()
				return
			}
			if f.denyRelay != 0 {
				http.Error(w, "synthetic relay authority denied", f.denyRelay)
				return
			}
			socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
			if err != nil {
				t.Error("synthetic relay handshake failed")
				return
			}
			mu.Lock()
			sockets = append(sockets, socket)
			mu.Unlock()
			defer socket.CloseNow()
			socket.SetReadLimit(browserFrameLimit)
			f.connected <- socket
			if socket.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n")) != nil {
				return
			}
			for {
				kind, data, err := socket.Read(ctx)
				if err != nil {
					return
				}
				if kind != websocket.MessageBinary {
					t.Error("authenticated relay received nonbinary data")
					return
				}
				if socket.Write(ctx, websocket.MessageBinary, data) != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Start()
	transport := &http.Transport{Proxy: nil}
	f.client = &api.Client{BaseURL: server.URL, Local: true, HTTP: &http.Client{Transport: transport}, Credential: config.Credential{AgentID: "requester-fixture"}}
	t.Cleanup(func() {
		cancel()
		mu.Lock()
		for _, socket := range sockets {
			socket.CloseNow()
		}
		mu.Unlock()
		transport.CloseIdleConnections()
		server.CloseClientConnections()
		server.Close()
	})
	return f
}

func TestBrowserSessionPreflightAndHumanWriter(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*browserFixture)
		code domain.Code
	}{
		{"wrong-id", func(f *browserFixture) { f.session.ID = "other-session" }, domain.Invalid},
		{"terminal-session", func(f *browserFixture) { f.session.Mode = "tmux" }, domain.Invalid},
		{"not-open", func(f *browserFixture) { f.session.OpenRequested = false }, domain.Denied},
		{"not-consented", func(f *browserFixture) { f.session.State = "requested" }, domain.Denied},
		{"revoked", func(f *browserFixture) { f.session.State = "revoked" }, domain.Denied},
		{"expired", func(f *browserFixture) { f.session.ExpiresAt = time.Now().Add(-time.Second).Unix() }, domain.Denied},
		{"identity-denied", func(f *browserFixture) { f.readFault = domain.Fail(domain.Denied, "synthetic identity denial") }, domain.Denied},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := browserNewFixture(t, test.edit)
			var diagnostic bytes.Buffer
			err := AttachBrowserVNC(context.Background(), f.client, "session-browser", BrowserOptions{AssetsDir: browserTestAssets(t)}, &diagnostic)
			if domain.AsFault(err).Code != test.code || diagnostic.Len() != 0 || f.dials.Load() != 0 {
				t.Fatalf("preflight failed to preserve authority: %v", err)
			}
		})
	}
	f := browserNewFixture(t, nil)
	if err := AttachBrowserVNC(context.Background(), f.client, "session-browser", BrowserOptions{}, nil); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("missing explicitly authorized human writer accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AttachBrowserVNC(ctx, f.client, "session-browser", BrowserOptions{}, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context did not fail before I/O")
	}
	if f.reads.Load() != 0 {
		t.Fatal("invalid human attachment performed API I/O")
	}
}

type browserDiagnostic struct {
	mu    sync.Mutex
	text  strings.Builder
	ready chan string
	once  sync.Once
	fail  bool
}

func (d *browserDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.text.Write(p)
	for _, field := range strings.Fields(d.text.String()) {
		if strings.HasPrefix(field, "http://") {
			d.once.Do(func() { d.ready <- field })
		}
	}
	if d.fail {
		return 0, errors.New("synthetic terminal write failure")
	}
	return len(p), nil
}

type browserRun struct {
	cancel    context.CancelFunc
	stopped   chan struct{}
	err       error
	bootstrap *url.URL
	base      string
	prefix    string
	cookie    *http.Cookie
	http      *http.Client
}

func browserStart(t *testing.T, f *browserFixture) *browserRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &browserRun{cancel: cancel, stopped: make(chan struct{})}
	diagnostic := &browserDiagnostic{ready: make(chan string, 1)}
	assets := browserTestAssets(t)
	transport := &http.Transport{Proxy: nil}
	r.http = &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() {
		cancel()
		r.wait(t)
		transport.CloseIdleConnections()
	})
	go func() {
		r.err = AttachBrowserVNC(ctx, f.client, "session-browser", BrowserOptions{AssetsDir: assets}, diagnostic)
		close(r.stopped)
	}()
	select {
	case raw := <-diagnostic.ready:
		var err error
		r.bootstrap, err = url.Parse(raw)
		if err != nil || r.bootstrap.Scheme != "http" || r.bootstrap.Query().Get("key") == "" {
			t.Fatal("human diagnostic omitted a valid bootstrap URL")
		}
		r.base = "http://" + r.bootstrap.Host
	case <-r.stopped:
		t.Fatalf("attachment stopped before bootstrap: %v", r.err)
	case <-time.After(5 * time.Second):
		t.Fatal("attachment did not publish human diagnostics")
	}
	return r
}

func (r *browserRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.stopped:
		return r.err
	case <-time.After(7 * time.Second):
		t.Fatal("browser attachment leaked a listener or handler")
		return nil
	}
}

func (r *browserRun) request(t *testing.T, method, target string, edit func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal("could not construct synthetic loopback request")
	}
	if edit != nil {
		edit(req)
	}
	response, err := r.http.Do(req)
	if err != nil {
		t.Fatal("synthetic loopback request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, browserAssetLimit+1))
	browserMust(t, err)
	browserAssertHeaders(t, response.Header, r.bootstrap.Host)
	return response, body
}

func browserAssertHeaders(t *testing.T, h http.Header, host string) {
	t.Helper()
	for name, expected := range map[string]string{
		"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY",
		"Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-origin",
		"Content-Security-Policy": "frame-ancestors 'none'", "Permissions-Policy": "clipboard-read=(), clipboard-write=()",
	} {
		if !strings.Contains(h.Get(name), expected) {
			t.Fatalf("missing private browser header %s", name)
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "connect-src ws://"+host+";") {
		t.Fatal("WebSocket CSP did not pin the literal host and port")
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "form-action 'none'") {
		t.Fatal("CSP allowed a native credential form submission")
	}
}

func (r *browserRun) authorize(t *testing.T) {
	t.Helper()
	response, _ := r.request(t, http.MethodGet, r.bootstrap.String(), nil)
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "/viewer/") {
		t.Fatal("one-use bootstrap did not redirect to the private viewer")
	}
	r.prefix = response.Header.Get("Location")
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatal("bootstrap must set exactly one session-local cookie")
	}
	r.cookie = cookies[0]
	if !r.cookie.HttpOnly || r.cookie.SameSite != http.SameSiteStrictMode || r.cookie.Path != r.prefix || r.cookie.Domain != "" || r.cookie.MaxAge <= 0 || r.cookie.Expires.IsZero() {
		t.Fatal("browser cookie omitted its strict, expiring path-scoped policy")
	}
	// Secure on HTTP literal loopback is not portable across supported browsers.
	if r.cookie.Secure || r.bootstrap.Scheme != "http" {
		t.Fatal("unexpected nonportable loopback cookie transport")
	}
}

func (r *browserRun) cookieRequest(req *http.Request) { req.AddCookie(r.cookie) }

func TestBrowserBootstrapAuthorizationHeadersAndPaths(t *testing.T) {
	f := browserNewFixture(t, nil)
	r := browserStart(t, f)
	for _, test := range []struct {
		name, method, path string
		edit               func(*http.Request)
		status             int
	}{
		{"root", "GET", "/", nil, http.StatusNotFound},
		{"no-key", "GET", "/bootstrap", nil, http.StatusForbidden},
		{"wrong-key", "GET", "/bootstrap?key=" + strings.Repeat("A", 43), nil, http.StatusForbidden},
		{"duplicate-key", "GET", r.bootstrap.RequestURI() + "&key=another", nil, http.StatusForbidden},
		{"wrong-host", "GET", r.bootstrap.RequestURI(), func(req *http.Request) { req.Host = "localhost:" + r.bootstrap.Port() }, http.StatusForbidden},
		{"forwarded-other-port", "GET", r.bootstrap.RequestURI(), func(req *http.Request) { req.Host = "127.0.0.1:1" }, http.StatusForbidden},
		{"cross-origin", "GET", r.bootstrap.RequestURI(), func(req *http.Request) { req.Header.Set("Origin", "http://example.invalid") }, http.StatusForbidden},
		{"null-origin", "GET", r.bootstrap.RequestURI(), func(req *http.Request) { req.Header.Set("Origin", "null") }, http.StatusForbidden},
		{"fetch-cross-site", "GET", r.bootstrap.RequestURI(), func(req *http.Request) { req.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"fetch-same-site-other-origin", "GET", r.bootstrap.RequestURI(), func(req *http.Request) { req.Header.Set("Sec-Fetch-Site", "same-site") }, http.StatusForbidden},
		{"post", "POST", r.bootstrap.RequestURI(), nil, http.StatusMethodNotAllowed},
		{"encoded-path", "GET", "/%62ootstrap" + "?" + r.bootstrap.RawQuery, nil, http.StatusBadRequest},
		{"encoded-space", "GET", "/bootstrap%20" + "?" + r.bootstrap.RawQuery, nil, http.StatusBadRequest},
		{"traversal", "GET", "/not/../bootstrap" + "?" + r.bootstrap.RawQuery, nil, http.StatusBadRequest},
		{"trailing-traversal", "GET", "/not/../bootstrap/" + "?" + r.bootstrap.RawQuery, nil, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := r.request(t, test.method, r.base+test.path, test.edit)
			if response.StatusCode != test.status {
				t.Fatalf("unexpected rejection status: %d", response.StatusCode)
			}
			if len(response.Cookies()) != 0 || bytes.Contains(body, []byte(r.bootstrap.Query().Get("key"))) {
				t.Fatal("unauthorized response disclosed or established authorization")
			}
		})
	}
	r.authorize(t)
	if r.cookie.Expires.After(time.Unix(f.session.ExpiresAt, 0)) {
		t.Fatal("cookie survived the authoritative session expiry")
	}
	response, _ := r.request(t, "GET", r.bootstrap.String(), nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("bootstrap token was reusable")
	}
	response, _ = r.request(t, "GET", r.base+r.prefix, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("predictable local request bypassed cookie authorization")
	}
	response, _ = r.request(t, "GET", r.base+r.prefix, func(req *http.Request) {
		bad := *r.cookie
		bad.Value = strings.Repeat("B", 43)
		req.AddCookie(&bad)
	})
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("wrong cookie authorized the viewer")
	}
	response, _ = r.request(t, "GET", r.base+r.prefix, func(req *http.Request) {
		bad := *r.cookie
		bad.Value = r.bootstrap.Query().Get("key")
		req.AddCookie(&bad)
	})
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("bootstrap secret was reusable as the independent session cookie")
	}
	response, _ = r.request(t, "GET", r.base+r.prefix, func(req *http.Request) {
		req.AddCookie(r.cookie)
		req.AddCookie(r.cookie)
	})
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("ambiguous duplicate cookie authorized the viewer")
	}
	for _, name := range []string{"", "viewer.js", "viewer.css", "assets/core/rfb.js", "assets/vendor/fixture.js", "assets/LICENSE.txt"} {
		response, body := r.request(t, "GET", r.base+r.prefix+name, r.cookieRequest)
		if response.StatusCode != http.StatusOK || len(body) == 0 || len(response.Cookies()) != 0 {
			t.Fatal("authenticated original wrapper or installed module was unavailable")
		}
		if bytes.Contains(body, []byte(r.bootstrap.Query().Get("key"))) || bytes.Contains(body, []byte(r.cookie.Value)) {
			t.Fatal("bootstrap or cookie secret leaked into static content")
		}
		if name == "" && (!bytes.Contains(body, []byte("MPL-2.0")) || !bytes.Contains(body, []byte("Clipboard sharing is disabled"))) {
			t.Fatal("viewer omitted its clipboard or noVNC license boundary")
		}
	}
	for _, suffix := range []string{"assets/core/private.js", "assets/../LICENSE.txt", "assets/core/%72fb.js", "relay?anything=1", "viewer.js?anything=1"} {
		response, _ := r.request(t, "GET", r.base+r.prefix+suffix, r.cookieRequest)
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusSwitchingProtocols {
			t.Fatal("unsupported path or query was exposed")
		}
	}
	response, body := r.request(t, "HEAD", r.base+r.prefix+"viewer.js", r.cookieRequest)
	if response.StatusCode != http.StatusOK || len(body) != 0 || response.ContentLength != int64(len(browserScript)) {
		t.Fatal("HEAD response did not preserve the bounded static representation")
	}
	if f.dials.Load() != 0 {
		t.Fatal("HTTP page or asset access dialed the authenticated RFB relay")
	}
	r.cancel()
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting attachment ignored cancellation: %v", err)
	}
	browserAssertListenerClosed(t, r.bootstrap.Host)
}

func TestBrowserBootstrapIsCryptographicAndSingleUseUnderRace(t *testing.T) {
	f := browserNewFixture(t, nil)
	r := browserStart(t, f)
	if key := r.bootstrap.Query().Get("key"); len(key) != 43 || key == strings.Repeat(key[:1], len(key)) {
		t.Fatal("bootstrap did not contain fresh 256-bit authorization")
	}
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			response, err := r.http.Get(r.bootstrap.String())
			if err != nil {
				statuses <- 0
				return
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			statuses <- response.StatusCode
		})
	}
	wg.Wait()
	one, two := <-statuses, <-statuses
	if !((one == http.StatusSeeOther && two == http.StatusForbidden) || (two == http.StatusSeeOther && one == http.StatusForbidden)) {
		t.Fatal("concurrent bootstrap redemption did not have exactly one winner")
	}
}

func (r *browserRun) dial(t *testing.T, cookie bool, origin, suffix string, kind string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := make(http.Header)
	if cookie {
		header.Set("Cookie", (&http.Cookie{Name: r.cookie.Name, Value: r.cookie.Value}).String())
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws://"+r.bootstrap.Host+r.prefix+suffix, &websocket.DialOptions{HTTPClient: r.http, HTTPHeader: header, Subprotocols: []string{kind}, CompressionMode: websocket.CompressionDisabled})
}

func (r *browserRun) connect(t *testing.T) *websocket.Conn {
	t.Helper()
	socket, response, err := r.dial(t, true, r.base, "relay", "binary")
	if err != nil {
		t.Fatal("authorized synthetic WebSocket failed to connect")
	}
	t.Cleanup(func() { socket.CloseNow() })
	browserAssertHeaders(t, response.Header, r.bootstrap.Host)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	kind, greeting, err := socket.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || string(greeting) != "RFB 003.008\n" {
		t.Fatal("authenticated binary RFB greeting was not relayed")
	}
	return socket
}

func TestBrowserWebSocketAuthorizationAndSingleViewer(t *testing.T) {
	f := browserNewFixture(t, nil)
	r := browserStart(t, f)
	r.authorize(t)
	for _, test := range []struct {
		name, origin, suffix, kind string
		cookie                     bool
		status                     int
	}{
		{"no-cookie", r.base, "relay", "binary", false, http.StatusForbidden},
		{"missing-origin", "", "relay", "binary", true, http.StatusForbidden},
		{"cross-origin", "http://example.invalid", "relay", "binary", true, http.StatusForbidden},
		{"other-port", "http://127.0.0.1:1", "relay", "binary", true, http.StatusForbidden},
		{"null-origin", "null", "relay", "binary", true, http.StatusForbidden},
		{"wrong-path", r.base, "other", "binary", true, http.StatusNotFound},
		{"query", r.base, "relay?key=not-supported", "binary", true, http.StatusBadRequest},
		{"text-protocol", r.base, "relay", "base64", true, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket, response, err := r.dial(t, test.cookie, test.origin, test.suffix, test.kind)
			if socket != nil {
				socket.CloseNow()
			}
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			if err == nil || response == nil || response.StatusCode != test.status {
				t.Fatal("unauthorized local WebSocket was not rejected")
			}
		})
	}
	response, _ := r.request(t, "GET", r.base+r.prefix+"relay", func(req *http.Request) {
		r.cookieRequest(req)
		req.Header.Set("Origin", r.base)
		req.Header.Set("Sec-WebSocket-Protocol", "binary")
	})
	if response.StatusCode < 400 {
		t.Fatal("incomplete WebSocket handshake was accepted")
	}
	response, _ = r.request(t, "GET", r.base+r.prefix+"relay", func(req *http.Request) {
		r.cookieRequest(req)
		req.Host = "example.invalid:1234"
		req.Header.Set("Origin", r.base)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==")
		req.Header.Set("Sec-WebSocket-Protocol", "binary")
	})
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("WebSocket Host rebinding was accepted")
	}
	if f.dials.Load() != 0 {
		t.Fatal("unauthorized or malformed WebSocket dialed the authoritative relay")
	}
	socket := r.connect(t)
	second, response, err := r.dial(t, true, r.base, "relay", "binary")
	if second != nil {
		second.CloseNow()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusConflict || f.dials.Load() != 1 {
		t.Fatal("a concurrent viewer acquired a second authenticated relay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte{0, 255, 128, 3, 10, 6}, 5000)
	browserMust(t, socket.Write(ctx, websocket.MessageBinary, payload))
	var echo []byte
	for len(echo) < len(payload) {
		kind, data, err := socket.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatal("binary RFB payload was lost or converted to text")
		}
		echo = append(echo, data...)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatal("binary RFB payload changed across the bridge")
	}
	browserMust(t, socket.Close(websocket.StatusNormalClosure, "synthetic human disconnect"))
	if err := r.wait(t); err != nil {
		t.Fatalf("clean human disconnect was not clean: %v", err)
	}
	browserAssertListenerClosed(t, r.bootstrap.Host)
	select {
	case <-f.relayEnded:
	case <-time.After(3 * time.Second):
		t.Fatal("viewer disconnect retained an authenticated relay")
	}
}

func TestBrowserRelayIdentityDenialIsNotSuccess(t *testing.T) {
	f := browserNewFixture(t, func(f *browserFixture) { f.denyRelay = http.StatusForbidden })
	r := browserStart(t, f)
	r.authorize(t)
	socket, _, err := r.dial(t, true, r.base, "relay", "binary")
	if err != nil {
		t.Fatal("local handshake unexpectedly failed before authoritative denial")
	}
	defer socket.CloseNow()
	if err := r.wait(t); domain.AsFault(err).Code != domain.Denied {
		t.Fatalf("authenticated relay denial was lost: %v", err)
	}
	browserAssertListenerClosed(t, r.bootstrap.Host)
}

func TestBrowserRevocationAndTransportLossStopAttachment(t *testing.T) {
	for _, policy := range []bool{false, true} {
		name := "transport-loss"
		if policy {
			name = "revocation"
		}
		t.Run(name, func(t *testing.T) {
			f := browserNewFixture(t, nil)
			r := browserStart(t, f)
			r.authorize(t)
			socket := r.connect(t)
			relay := <-f.connected
			if policy {
				_ = relay.Close(websocket.StatusPolicyViolation, "synthetic authority revoked")
			} else {
				relay.CloseNow()
			}
			err := r.wait(t)
			want := domain.Unavailable
			if policy {
				want = domain.Denied
			}
			if err == nil || domain.AsFault(err).Code != want {
				t.Fatalf("relay retirement incorrectly reported success: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, _, err := socket.Read(ctx); err == nil {
				t.Fatal("retired relay left the browser WebSocket alive")
			}
			browserAssertListenerClosed(t, r.bootstrap.Host)
		})
	}
}

func TestBrowserCancellationDuringAuthenticatedRelayHandshake(t *testing.T) {
	f := browserNewFixture(t, func(f *browserFixture) { f.waitRelay = true })
	r := browserStart(t, f)
	r.authorize(t)
	socket, _, err := r.dial(t, true, r.base, "relay", "binary")
	if err != nil {
		t.Fatal("local WebSocket handshake failed before the synthetic relay stall")
	}
	defer socket.CloseNow()
	select {
	case <-f.attempted:
	case <-time.After(3 * time.Second):
		t.Fatal("authorized WebSocket did not attempt authenticated relay admission")
	}
	r.cancel()
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during admission was lost: %v", err)
	}
	select {
	case <-f.relayEnded:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled authenticated relay handshake retained its connection")
	}
	browserAssertListenerClosed(t, r.bootstrap.Host)
}

func TestBrowserContextCancellationAndExpiryCloseSockets(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		name := "cancel"
		if expiry {
			name = "expire"
		}
		t.Run(name, func(t *testing.T) {
			f := browserNewFixture(t, func(f *browserFixture) {
				if expiry {
					f.session.ExpiresAt = time.Now().Add(2 * time.Second).Unix()
				}
			})
			r := browserStart(t, f)
			r.authorize(t)
			socket := r.connect(t)
			if !expiry {
				r.cancel()
			}
			err := r.wait(t)
			want := context.Canceled
			if expiry {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("attachment did not propagate its lifetime: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, _, err := socket.Read(ctx); err == nil {
				t.Fatal("expired/cancelled attachment left the browser socket alive")
			}
			select {
			case <-f.relayEnded:
			case <-time.After(3 * time.Second):
				t.Fatal("expired/cancelled attachment retained an authenticated relay")
			}
			browserAssertListenerClosed(t, r.bootstrap.Host)
		})
	}
}

func TestBrowserRejectsTextAndOversizedMessages(t *testing.T) {
	for _, large := range []bool{false, true} {
		name := "text"
		if large {
			name = "oversized"
		}
		t.Run(name, func(t *testing.T) {
			f := browserNewFixture(t, nil)
			r := browserStart(t, f)
			r.authorize(t)
			socket := r.connect(t)
			kind, data := websocket.MessageText, []byte("not RFB binary")
			if large {
				kind, data = websocket.MessageBinary, make([]byte, browserFrameLimit+1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = socket.Write(ctx, kind, data)
			if err := r.wait(t); err == nil || domain.AsFault(err).Code != domain.Unavailable {
				t.Fatalf("invalid browser data did not terminate with an error: %v", err)
			}
			browserAssertListenerClosed(t, r.bootstrap.Host)
		})
	}
}

func TestBrowserBoundsHTTPRequestsAndFailedHumanDiagnostics(t *testing.T) {
	f := browserNewFixture(t, nil)
	r := browserStart(t, f)
	response, _ := r.request(t, "GET", r.bootstrap.String(), func(req *http.Request) {
		req.Body = io.NopCloser(strings.NewReader("not accepted"))
		req.ContentLength = int64(len("not accepted"))
	})
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatal("HTTP body was accepted by the bootstrap")
	}
	r.authorize(t)
	conn, err := net.DialTimeout("tcp", r.bootstrap.Host, time.Second)
	browserMust(t, err)
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+r.bootstrap.Host+"\r\nX-Oversize: "+strings.Repeat("x", 20<<10)+"\r\n\r\n")
	browserMust(t, err)
	browserMust(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	conn.Close()
	if !strings.Contains(string(buf[:n]), "431") {
		t.Fatal("HTTP header limit was not enforced")
	}
	failing := &browserDiagnostic{ready: make(chan string, 1), fail: true}
	err = AttachBrowserVNC(context.Background(), f.client, "session-browser", BrowserOptions{AssetsDir: browserTestAssets(t)}, failing)
	if err == nil || domain.AsFault(err).Code != domain.Unavailable || strings.Contains(err.Error(), "http://") {
		t.Fatal("human diagnostic failure was swallowed or disclosed a secret URL")
	}
	select {
	case raw := <-failing.ready:
		u, parseErr := url.Parse(raw)
		browserMust(t, parseErr)
		browserAssertListenerClosed(t, u.Host)
	default:
		t.Fatal("failure fixture did not observe the owned listener")
	}
}

func browserAssertListenerClosed(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("completed attachment retained its local listener")
	}
}

func TestBrowserBoundsAcceptedHTTPConnections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	browserMust(t, err)
	defer listener.Close()
	bounded := &browserListener{Listener: listener, slots: make(chan struct{}, 1)}
	first, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	browserMust(t, err)
	defer first.Close()
	accepted, err := bounded.Accept()
	browserMust(t, err)
	defer accepted.Close()
	ready := make(chan net.Conn, 1)
	failed := make(chan error, 1)
	go func() {
		conn, err := bounded.Accept()
		if err != nil {
			failed <- err
			return
		}
		ready <- conn
	}()
	second, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	browserMust(t, err)
	browserMust(t, second.SetReadDeadline(time.Now().Add(time.Second)))
	_, readErr := second.Read(make([]byte, 1))
	second.Close()
	if readErr != io.EOF {
		t.Fatal("saturated HTTP listener did not promptly drop the extra socket")
	}
	browserMust(t, accepted.Close())
	third, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	browserMust(t, err)
	defer third.Close()
	select {
	case conn := <-ready:
		browserMust(t, conn.Close())
		browserMust(t, conn.Close())
	case <-failed:
		t.Fatal("bounded HTTP listener failed after releasing its slot")
	case <-time.After(time.Second):
		t.Fatal("closed HTTP connection did not release its slot")
	}
	if len(bounded.slots) != 0 {
		t.Fatal("HTTP close retained a connection slot")
	}
}

func TestBrowserWrapperDisablesClipboardWithoutBundlingNoVNC(t *testing.T) {
	for _, fragment := range []string{
		"import RFB from './assets/core/rfb.js'", "initLogging('none')", "class ClipboardIsolatedRFB extends RFB",
		"clipboardPasteFrom() {}", "if (event.type === 'clipboard') return false", "['copy', 'cut', 'paste']",
		"event.preventDefault()", "event.stopImmediatePropagation()", "endpoint.protocol = 'ws:'", "rfb.resizeSession = false",
	} {
		if !strings.Contains(browserScript, fragment) {
			t.Fatalf("original wrapper omitted clipboard/transport policy: %s", fragment)
		}
	}
	for _, forbidden := range []string{"navigator.clipboard", "console.", "http://", "https://", "localStorage", "sessionStorage", "URLSearchParams", "FormData", "fetch(", "XMLHttpRequest", "sendBeacon"} {
		if strings.Contains(browserScript, forbidden) {
			t.Fatalf("wrapper added automatic data exchange or logging: %s", forbidden)
		}
	}
	if !strings.Contains(browserPage, "MPL-2.0") || !strings.Contains(browserPage, "assets/LICENSE.txt") {
		t.Fatal("separately installed noVNC notice boundary was omitted")
	}
}

func TestBrowserCredentialFormCannotSubmitSecretsToHTTP(t *testing.T) {
	forms := regexp.MustCompile(`<form\b[^>]*>`).FindAllString(browserPage, -1)
	if len(forms) != 1 || !strings.Contains(forms[0], `method="post"`) || !strings.Contains(forms[0], `autocomplete="off"`) || !strings.Contains(forms[0], ` hidden`) {
		t.Fatal("credential form omitted its non-navigation, initially hidden policy")
	}
	inputs := regexp.MustCompile(`<input\b[^>]*>`).FindAllString(browserPage, -1)
	if len(inputs) != 3 {
		t.Fatal("credential form added unrequested input types")
	}
	for _, input := range inputs {
		if regexp.MustCompile(`\b(name|value)\s*=`).MatchString(input) || !strings.Contains(input, `autocomplete="off"`) || !strings.Contains(input, ` disabled`) {
			t.Fatal("credential input could serialize or retain a preset secret")
		}
		if strings.Contains(input, `id="credential-password"`) && !strings.Contains(input, `type="password"`) {
			t.Fatal("VNC password was not masked")
		}
	}
	for _, fragment := range []string{
		"['username', 'target', 'password']", "types.some(type => !credentialFields.has(type))",
		"if (!event.isTrusted || requestedCredentials.length === 0) return", "if (field.value === '')",
		"credentials[type] = field.value", "rfb.sendCredentials(credentials)", "field.value = ''",
		"rfb.viewOnly = true", "rfb.blur()", "credential-cancel", "addEventListener('pagehide', disconnect",
	} {
		if !strings.Contains(browserScript, fragment) {
			t.Fatalf("human credential handoff omitted a safety boundary: %s", fragment)
		}
	}
	if strings.Count(browserScript, "rfb.sendCredentials(") != 1 || !strings.Contains(browserStyles, "[hidden]{display:none!important}") {
		t.Fatal("credentials had another send path or hidden fields could be displayed by CSS")
	}
}

func TestBrowserCredentialFormSyntheticJavaScriptBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; structural credential and clipboard assertions still run")
	}
	source, err := json.Marshal(map[string]string{"script": browserScript, "page": browserPage})
	browserMust(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-vm-modules", "--no-warnings", "--input-type=module")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "NODE_DISABLE_COMPILE_CACHE=1"}
	cmd.Stdin = strings.NewReader("import vm from 'node:vm';\nconst source = " + string(source) + ";\n" + browserCredentialBehaviorTest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("original synthetic noVNC/DOM JavaScript validation failed: %v\n%s", err, output)
	}
	if string(output) != "synthetic noVNC credential and clipboard behavior passed\n" {
		t.Fatal("JavaScript validation did not complete")
	}
}

// This original fixture executes the exact shipped module in Node's ESM parser.
// It implements only the DOM and noVNC public interfaces used by the wrapper;
// no browser, provider, real credential, upstream JS or network is involved.
const browserCredentialBehaviorTest = `
const types = ['username', 'target', 'password'];
const values = Object.freeze({ username: 'synthetic-human-user', target: 'synthetic-human-target', password: 'synthetic-human-password' });
function expect(condition, message) {
    if (!condition) throw new Error(message);
}
class Element {
    constructor(id, attributes) {
        this.id = id;
        this.hidden = /\bhidden\b/.test(attributes);
        this.disabled = /\bdisabled\b/.test(attributes);
        this.required = false;
        this.value = '';
        this.textContent = '';
        this.listeners = new Map();
        this.focuses = 0;
    }
    addEventListener(type, listener) {
        const list = this.listeners.get(type) || [];
        list.push(listener);
        this.listeners.set(type, list);
    }
    emit(type, options = {}) {
        const event = {
            type, isTrusted: true, defaultPrevented: false, propagationStopped: false, immediateStopped: false,
            preventDefault() { this.defaultPrevented = true; },
            stopPropagation() { this.propagationStopped = true; },
            stopImmediatePropagation() { this.immediateStopped = true; },
            ...options,
        };
        for (const listener of this.listeners.get(type) || []) {
            listener(event);
            if (event.immediateStopped) break;
        }
        return event;
    }
    focus() { this.focuses++; }
}
class FixtureRFB {
    constructor(target, endpoint, options) {
        this.target = target;
        this.endpoint = endpoint;
        this.options = options;
        this.listeners = new Map();
        this.sent = [];
        this.sendAttempts = 0;
        this.disconnects = 0;
        this.focuses = 0;
        this.blurs = 0;
        this.viewOnly = false;
        this.focusOnClick = true;
        globalThis.fixtureRFB = this;
    }
    addEventListener(type, listener) {
        const list = this.listeners.get(type) || [];
        list.push(listener);
        this.listeners.set(type, list);
    }
    dispatchEvent(event) {
        for (const listener of this.listeners.get(event.type) || []) listener(event);
        return true;
    }
    sendCredentials(credentials) {
        this.sendAttempts++;
        for (const type of ['username', 'target', 'password']) {
            if (document.getElementById('credential-' + type).value !== '') {
                throw new Error('credential DOM was not cleared before noVNC handoff');
            }
        }
        if (!document.getElementById('credentials-form').hidden) {
            throw new Error('credential form was not hidden before noVNC handoff');
        }
        if (this.throwCredentials) throw new Error('synthetic noVNC send failure');
        this.sent.push(credentials);
    }
    disconnect() {
        this.disconnects++;
        this.dispatchEvent({ type: 'disconnect', detail: { clean: true } });
    }
    focus() { this.focuses++; }
    blur() { this.blurs++; }
    clipboardPasteFrom() { throw new Error('outbound clipboard was not disabled'); }
}
let forbiddenCalls = 0;
function forbidden() {
    forbiddenCalls++;
    throw new Error('viewer attempted an out-of-band request, storage or logging');
}
async function boot() {
    const elements = new Map();
    for (const match of source.page.matchAll(/<[a-z]+\b([^>]*\bid="([^"]+)"[^>]*)>/g)) {
        elements.set(match[2], new Element(match[2], match[1]));
    }
    const documentEvents = new Element('document', '');
    const windowEvents = new Element('window', '');
    const document = {
        getElementById(id) {
            expect(elements.has(id), 'wrapper accessed a missing form element');
            return elements.get(id);
        },
        addEventListener(type, listener) { documentEvents.addEventListener(type, listener); },
    };
    Object.defineProperty(document, 'cookie', { get: forbidden, set: forbidden });
    const context = vm.createContext({
        URL, document,
        addEventListener: (type, listener) => windowEvents.addEventListener(type, listener),
        console: new Proxy({}, { get: () => forbidden }),
        fetch: forbidden, XMLHttpRequest: forbidden, WebSocket: forbidden,
        navigator: { clipboard: new Proxy({}, { get: () => forbidden }), sendBeacon: forbidden },
        localStorage: new Proxy({}, { get: forbidden, set: forbidden }),
        sessionStorage: new Proxy({}, { get: forbidden, set: forbidden }),
    });
    const base = 'http://127.0.0.1:1/viewer/synthetic/';
    const main = new vm.SourceTextModule(source.script, {
        context, identifier: base + 'viewer.js',
        initializeImportMeta(meta) { meta.url = base + 'viewer.js'; },
    });
    await main.link(specifier => {
        if (specifier === './assets/core/rfb.js') {
            return new vm.SourceTextModule('export default ' + FixtureRFB.toString(), { context });
        }
        if (specifier === './assets/core/util/logging.js') {
            return new vm.SourceTextModule("export function initLogging(level) { if (level !== 'none') throw new Error('unexpected logging'); globalThis.loggingQuiet = true; }", { context });
        }
        throw new Error('wrapper imported an unexpected asset');
    });
    await main.evaluate();
    expect(context.fixtureRFB !== undefined, 'wrapper failed to create the synthetic noVNC viewer');
    const rfb = context.fixtureRFB;
    expect(rfb.endpoint === 'ws://127.0.0.1:1/viewer/synthetic/relay', 'wrapper changed the pinned RFB URL');
    expect(rfb.options.credentials === undefined && rfb.sent.length === 0, 'wrapper supplied credentials without a human request');
    expect(context.loggingQuiet === true, 'wrapper left noVNC logging enabled');
    const run = { rfb, elements, documentEvents, windowEvents, form: elements.get('credentials-form') };
    cleared(run);
    return run;
}
function cleared(run) {
    expect(run.form.hidden && run.elements.get('credential-submit').disabled, 'credential form remained active');
    for (const type of types) {
        const field = run.elements.get('credential-' + type);
        expect(field.value === '' && field.disabled && !field.required, 'credential field retained a value or remained active');
        expect(run.elements.get('credential-' + type + '-label').hidden, 'unused credential label remained visible');
    }
}
function request(run, requested) {
    const sent = run.rfb.sent.length;
    run.rfb.dispatchEvent({ type: 'credentialsrequired', detail: { types: requested } });
    expect(!run.form.hidden && !run.elements.get('credential-submit').disabled, 'human credential request did not show the form');
    expect(run.rfb.sent.length === sent && run.rfb.disconnects === 0, 'credential challenge caused automatic submission or bypass/disconnect');
    expect(run.rfb.viewOnly && !run.rfb.focusOnClick && run.rfb.blurs > 0, 'credential typing could reach the remote screen');
    for (const type of types) {
        const field = run.elements.get('credential-' + type);
        expect(field.disabled === !requested.includes(type) && field.required === requested.includes(type), 'credential field did not follow the requested types');
        expect(run.elements.get('credential-' + type + '-label').hidden === !requested.includes(type), 'unrequested credential field was visible');
        expect(field.value === '', 'challenge reused an earlier credential value');
    }
}
function fill(run, requested) {
    for (const type of requested) run.elements.get('credential-' + type).value = values[type];
}
function submit(run, requested) {
    const before = run.rfb.sent.length;
    const event = run.form.emit('submit');
    expect(event.defaultPrevented && event.propagationStopped, 'credential submission could navigate or propagate to RFB keyboard handlers');
    expect(run.rfb.sent.length === before + 1, 'human credential submission did not reach noVNC exactly once');
    const sent = run.rfb.sent[before];
    expect(Object.keys(sent).length === requested.length, 'handoff included unrequested credential fields');
    for (const type of requested) expect(sent[type] === values[type], 'human credential value changed during handoff');
    cleared(run);
    expect(run.rfb.viewOnly, 'keyboard forwarding resumed before authentication completed');
    expect(!run.elements.get('status').textContent.includes('synthetic-human-') && !run.rfb.endpoint.includes('synthetic-human-'), 'credential value reached metadata or a URL');
    return sent;
}
function clipboard(run) {
    let received = false;
    run.rfb.addEventListener('clipboard', () => { received = true; });
    run.rfb.dispatchEvent({ type: 'clipboard', detail: { text: 'synthetic remote clipboard' } });
    expect(!received, 'inbound clipboard reached a listener');
    run.rfb.clipboardPasteFrom('synthetic local clipboard');
    for (const type of ['copy', 'cut', 'paste']) {
        const event = run.documentEvents.emit(type);
        expect(event.defaultPrevented && event.immediateStopped, 'browser clipboard event was not blocked');
    }
}

for (const requested of [['password'], ['username', 'password'], ['target', 'password'], ['username', 'target', 'password'], ['target']]) {
    const run = await boot();
    request(run, requested);
    const empty = run.form.emit('submit');
    expect(empty.defaultPrevented && run.rfb.sendAttempts === 0, 'empty credentials were submitted as an authentication bypass');
    fill(run, requested);
    run.form.emit('submit', { isTrusted: false });
    expect(run.rfb.sendAttempts === 0, 'untrusted submission supplied credentials');
    // Autofill or another DOM actor must not cause unrequested values to be sent.
    for (const type of types.filter(type => !requested.includes(type))) run.elements.get('credential-' + type).value = values[type];
    const sent = submit(run, requested);
    await Promise.resolve();
    for (const type of requested) expect(sent[type] === values[type], 'clearing DOM fields broke asynchronous noVNC authentication');
    run.form.emit('submit');
    expect(run.rfb.sent.length === 1, 'duplicate submit reused cleared credentials');
    run.rfb.dispatchEvent({ type: 'connect' });
    cleared(run);
    expect(!run.rfb.viewOnly && run.rfb.focusOnClick && run.rfb.focuses > 0, 'authenticated viewer did not restore interactive input');
    clipboard(run);
}

{
    const run = await boot();
    request(run, ['username', 'password']);
    fill(run, ['username', 'password']);
    request(run, ['password']);
    fill(run, ['password']);
    submit(run, ['password']);
    request(run, ['password']);
    fill(run, ['password']);
    run.rfb.dispatchEvent({ type: 'securityfailure', detail: { reason: values.password } });
    cleared(run);
    expect(!run.elements.get('status').textContent.includes(values.password), 'authentication failure echoed a private reason');
    expect(run.rfb.sent.length === 1, 'authentication failure retried credentials automatically');
}

for (const action of ['credential-cancel', 'disconnect', 'pagehide', 'disconnect-event', 'connect-event']) {
    const run = await boot();
    request(run, ['username', 'target', 'password']);
    fill(run, types);
    if (action === 'pagehide') run.windowEvents.emit('pagehide');
    else if (action === 'disconnect-event') run.rfb.dispatchEvent({ type: 'disconnect', detail: { clean: false } });
    else if (action === 'connect-event') run.rfb.dispatchEvent({ type: 'connect' });
    else run.elements.get(action).emit('click');
    cleared(run);
    expect(run.rfb.sent.length === 0, 'cancel/teardown sent pending credentials');
}

{
    const run = await boot();
    request(run, ['password']);
    fill(run, ['password']);
    run.rfb.throwCredentials = true;
    run.form.emit('submit');
    cleared(run);
    expect(run.rfb.sendAttempts === 1 && run.rfb.disconnects === 1, 'failed noVNC credential handoff did not stop the viewer');
    expect(!run.elements.get('status').textContent.includes(values.password), 'credential handoff error exposed a secret');
}

for (const requested of [undefined, null, [], {}, ['token'], ['__proto__'], ['password', 'password'], ['password', 'username', 'target', 'password']]) {
    const run = await boot();
    run.rfb.dispatchEvent({ type: 'credentialsrequired', detail: { types: requested } });
    cleared(run);
    expect(run.rfb.disconnects === 1 && run.rfb.sendAttempts === 0, 'malformed or unsupported challenge bypassed authentication');
}

{
    const run = await boot();
    request(run, ['password']);
    for (const type of ['keydown', 'keypress', 'keyup']) {
        expect(run.form.emit(type).propagationStopped, 'credential keystroke could propagate to the remote screen');
    }
    clipboard(run);
    expect(!run.rfb.endpoint.includes(values.password), 'credential changed the transport URL');
}
expect(forbiddenCalls === 0, 'credential handling attempted out-of-band communication, storage or logging');
process.stdout.write('synthetic noVNC credential and clipboard behavior passed\n');
`

type browserTestStream struct{ io.Reader }

func (s browserTestStream) Write(p []byte) (int, error) { return len(p), nil }
func (s browserTestStream) Close() error                { return nil }

func TestBrowserDataBudgetAndUnexpectedRelayEOF(t *testing.T) {
	s := &browserBridgeStream{ReadWriteCloser: browserTestStream{strings.NewReader("four")}, ctx: context.Background(), ending: &atomic.Bool{}, limit: 3, touch: func() {}}
	if n, err := s.Read(make([]byte, 8)); n != 0 || err == nil || domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("session data budget forwarded oversized data")
	}
	s.remote = true
	if err := s.transportError(io.EOF); domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("unexpected authenticated relay EOF became silent success")
	}
}
