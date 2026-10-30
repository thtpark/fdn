// Package mobile embeds a foreground Founder-device host. It shares the CLI,
// identity owner, native transport and local journal, not a desktop controller.
package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/cli"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/identityservice"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/observe"
	"github.com/thtpark/fdn/internal/secure"
)

type Runtime struct {
	root                 string
	lifecycle            sync.Mutex
	mu                   sync.RWMutex
	run                  *foreground
	remote               identityservice.Remote
	container            *identityservice.ContainerStorage
	calls                map[string]context.CancelFunc
	canceled             map[string]time.Time
	cancellationOverflow bool
	readers              chan struct{}
	writers              chan struct{}
	lastError            *domain.Fault
	closed               bool
}

type foreground struct {
	context    context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	err        error
	service    *identityservice.Service
	attachment *identityservice.EmbeddedAttachment
	client     *api.Client
	state      *local.State
	cli        *cli.Embedded
	observer   *observation
	observerMu sync.Mutex
}

type Request struct {
	ID          string          `json:"id"`
	Op          string          `json:"op"`
	Name        string          `json:"name,omitempty"`
	Profile     json.RawMessage `json:"profile,omitempty"`
	Query       observe.Query   `json:"query,omitempty"`
	Queries     []observe.Query `json:"queries,omitempty"`
	Arguments   []string        `json:"arguments,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
	Revision    int64           `json:"revision,omitempty"`
}

func New(root string) (*Runtime, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n") ||
		os.Geteuid() == 0 || os.Getuid() != os.Geteuid() {
		return nil, domain.Fail(domain.Denied, "mobile runtime requires a clean app-private path under the current non-root account")
	}
	if err := secure.Dir(root); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root = canonical
	for _, name := range []string{"identity", "runner", "exchange", "ipc"} {
		if err := secure.Dir(filepath.Join(root, name)); err != nil {
			return nil, err
		}
	}
	r := &Runtime{root: root, remote: identityservice.EmbeddedRemote{}, calls: make(map[string]context.CancelFunc), canceled: make(map[string]time.Time),
		readers: make(chan struct{}, 2), writers: make(chan struct{}, 1)}
	if goruntime.GOOS == "ios" {
		r.container, err = identityservice.NewContainerStorage(root)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Runtime) settings() config.IdentityService {
	worker := config.DefaultWorker(filepath.Join(r.root, "runner"))
	worker.AutomaticProviderUpdates = false
	worker.IdentitySocket = filepath.Join(r.root, "ipc", "identity.sock")
	return config.IdentityService{StateDir: filepath.Join(r.root, "identity"), Socket: worker.IdentitySocket, Worker: worker}.WithCurrentUser()
}

func (r *Runtime) identityOptions() identityservice.Options {
	return identityservice.Options{Remote: r.remote, Container: r.container}
}

func (r *Runtime) status() any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value := map[string]any{
		"runtime": "foreground-founder", "foreground_only": true, "controller": false,
		"provider_execution": false, "native_receivers": false, "daemon_running": false,
		"state": "inactive", "ready": false, "version": domain.Version, "commit": domain.Commit,
		"api_version": domain.APIVersion, "schema_version": domain.SchemaVersion,
	}
	if r.lastError != nil {
		value["error"] = r.lastError
	}
	if r.run != nil {
		status := r.run.service.Status()
		value["state"], value["ready"] = "connecting", status.Ready
		if status.Ready && r.run.cli != nil {
			value["state"] = "active"
		}
		value["identity"], value["agent"], value["connected"], value["problem"] =
			status.Identity, status.Agent, status.Connected, status.Problem
	}
	return value
}

func (r *Runtime) idle() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.run != nil {
		return domain.Fail(domain.Conflict, "stop this app's foreground runtime before changing its identity setup")
	}
	return nil
}

func (r *Runtime) prepare(name string) (domain.PublicIdentity, error) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if err := r.idle(); err != nil {
		return domain.PublicIdentity{}, err
	}
	return identityservice.Prepare(r.settings().StateDir, name, filepath.Join(r.root, "exchange", "public-request.json"), r.identityOptions())
}

func (r *Runtime) connect(ctx context.Context, data json.RawMessage) (api.LocalStatus, error) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if err := r.idle(); err != nil {
		return api.LocalStatus{}, err
	}
	var profile config.ConnectionProfile
	if err := decode(data, &profile); err != nil {
		return api.LocalStatus{}, err
	}
	if profile.Identity.DeviceID == "" {
		return api.LocalStatus{}, domain.Fail(domain.Denied, "mobile requires its own approved Founder device binding, never another computer's identity")
	}
	status, err := identityservice.Connect(ctx, r.settings(), profile, r.identityOptions())
	if err != nil {
		return status, err
	}
	if !status.Agent.Founder || !status.Agent.Active || status.Identity.DeviceID == "" {
		return api.LocalStatus{}, domain.Fail(domain.Denied, "approved profile did not activate an active Founder device")
	}
	return status, nil
}

func (r *Runtime) start(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if err := r.idle(); err != nil {
		return err
	}
	settings := r.settings()
	var profile config.ConnectionProfile
	if err := config.Read(filepath.Join(settings.StateDir, "connection.json"), &profile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Fail(domain.Prerequisite, "prepare fresh phone keys and import its approved public Founder-device profile first")
		}
		return err
	}
	if profile.Identity.DeviceID == "" {
		return domain.Fail(domain.Denied, "foreground mobile hosting requires a Founder device binding")
	}
	service, err := identityservice.New(settings, r.identityOptions())
	if err != nil {
		return err
	}
	attachment, err := identityservice.NewEmbeddedAttachment(settings)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	f := &foreground{context: runCtx, cancel: cancel, done: make(chan struct{}), service: service, attachment: attachment}
	r.mu.Lock()
	r.run, r.lastError = f, nil
	r.mu.Unlock()
	go func() {
		f.err = service.ServeWithBoundary(runCtx, attachment)
		close(f.done)
	}()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = r.closeRun(f)
		}
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !service.Status().Ready {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.done:
			if f.err != nil {
				return f.err
			}
			return domain.Fail(domain.Unavailable, "foreground identity stopped before becoming ready")
		case <-runCtx.Done():
			return runCtx.Err()
		case <-ticker.C:
			status := service.Status()
			if status.Problem == domain.Denied {
				return domain.Fail(domain.Denied, "this phone identity was denied or revoked")
			}
		}
	}
	status := service.Status()
	if !status.Agent.Founder || !status.Agent.Active || status.Identity.DeviceID != profile.Identity.DeviceID {
		return domain.Fail(domain.Denied, "foreground identity is not this approved active Founder device")
	}
	client := api.NewMessageClient("embedded://identity.local", attachment.Dial)
	client.Local = true
	client.Credential = config.Credential{AgentID: status.Agent.ID}
	f.client = client
	state, err := local.Open(ctx, settings.Worker.StateDir)
	if err != nil {
		return err
	}
	f.state = state
	if err := state.BindIdentity(ctx, status.Agent.ID); err != nil {
		return err
	}
	embedded, err := cli.NewEmbedded(settings.Worker, state, client, r.status)
	if err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	f.cli = embedded
	r.mu.Unlock()
	succeeded = true
	return nil
}

func (r *Runtime) closeRun(f *foreground) error {
	f.cancel()
	if f.client != nil {
		f.client.Close()
	}
	f.observerMu.Lock()
	observer := f.observer
	f.observer = nil
	f.observerMu.Unlock()
	if observer != nil {
		observer.close()
	}
	<-f.done
	var closeErr error
	if f.state != nil {
		closeErr = f.state.Close()
	}
	r.mu.Lock()
	if r.run == f {
		r.run = nil
	}
	r.mu.Unlock()
	return errors.Join(f.err, closeErr)
}

func (r *Runtime) Stop() error {
	r.mu.RLock()
	for _, cancel := range r.calls {
		cancel()
	}
	f := r.run
	if f != nil {
		f.cancel()
	}
	r.mu.RUnlock()
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.RLock()
	f = r.run
	r.mu.RUnlock()
	if f == nil {
		r.mu.Lock()
		r.canceled = make(map[string]time.Time)
		r.cancellationOverflow = false
		r.mu.Unlock()
		return nil
	}
	err := r.closeRun(f)
	r.mu.Lock()
	r.canceled = make(map[string]time.Time)
	r.cancellationOverflow = false
	r.mu.Unlock()
	return err
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closed = true
	for _, cancel := range r.calls {
		cancel()
	}
	r.mu.Unlock()
	return r.Stop()
}

func (r *Runtime) Cancel(id string) {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\r\n") {
		return
	}
	r.mu.Lock()
	cancel := r.calls[id]
	if cancel == nil && !r.closed {
		for key, expiry := range r.canceled {
			if time.Now().After(expiry) {
				delete(r.canceled, key)
			}
		}
		if len(r.canceled) >= 128 {
			r.cancellationOverflow = true
			r.lastError = domain.Fail(domain.Unavailable, "mobile cancellation budget was exceeded; stop and reconnect explicitly")
		} else {
			r.canceled[id] = time.Now().Add(time.Minute)
		}
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runtime) active() (*foreground, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.run == nil || r.run.cli == nil || !r.run.service.Status().Ready || r.run.context.Err() != nil {
		return nil, domain.Fail(domain.Unavailable, "this app's foreground identity is not ready; no operation was retried")
	}
	return r.run, nil
}

func decode(data []byte, value any) error {
	if len(data) == 0 || len(data) > domain.MaxBody {
		return domain.Fail(domain.Invalid, "mobile request requires bounded JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return domain.Fail(domain.Invalid, "mobile request JSON is invalid or has unsupported fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return domain.Fail(domain.Invalid, "mobile request must contain exactly one JSON value")
	}
	return nil
}

func (r *Runtime) Call(data []byte) []byte {
	var request Request
	if err := decode(data, &request); err != nil {
		return envelope(nil, "", err)
	}
	if request.ID == "" || len(request.ID) > 128 || strings.ContainsAny(request.ID, "\x00\r\n") {
		return envelope(nil, request.OperationID, domain.Fail(domain.Invalid, "mobile call requires a bounded unique ID"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	r.mu.Lock()
	if expiry, found := r.canceled[request.ID]; found && time.Now().Before(expiry) {
		delete(r.canceled, request.ID)
		r.mu.Unlock()
		cancel()
		return envelope(nil, request.OperationID, domain.Fail(domain.Cancelled, "mobile call was cancelled before dispatch"))
	}
	if r.cancellationOverflow {
		r.mu.Unlock()
		cancel()
		return envelope(nil, request.OperationID, domain.Fail(domain.Unavailable, "mobile cancellation budget requires an explicit stop/reconnect"))
	}
	if r.closed || r.calls[request.ID] != nil || len(r.calls) >= 16 {
		r.mu.Unlock()
		cancel()
		return envelope(nil, request.OperationID, domain.Fail(domain.Conflict, "mobile runtime is closed or its bounded call IDs are already active"))
	}
	r.calls[request.ID] = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.calls, request.ID)
		r.mu.Unlock()
	}()
	value, err := r.execute(ctx, request)
	if err != nil {
		r.mu.Lock()
		r.lastError = domain.AsFault(err)
		r.mu.Unlock()
	}
	return envelope(value, request.OperationID, err)
}

func (r *Runtime) execute(ctx context.Context, q Request) (any, error) {
	switch q.Op {
	case "runtime.status":
		return r.status(), nil
	case "runtime.prepare":
		return r.prepare(q.Name)
	case "runtime.connect":
		return r.connect(ctx, q.Profile)
	case "runtime.start":
		err := r.start(ctx)
		return r.status(), err
	case "runtime.stop":
		err := r.Stop()
		return r.status(), err
	case "query":
		if slices.Contains([]string{"version", "help", "guides", "guide"}, q.Query.Kind) {
			return cli.EmbeddedOffline(ctx, q.Query)
		}
		f, err := r.active()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(f.context, cancel)
		defer func() { stop(); cancel() }()
		select {
		case r.readers <- struct{}{}:
			defer func() { <-r.readers }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return f.cli.Snapshot(ctx, q.Query)
	case "mutate":
		f, err := r.active()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(f.context, cancel)
		defer func() { stop(); cancel() }()
		select {
		case r.writers <- struct{}{}:
			defer func() { <-r.writers }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return f.cli.Mutate(ctx, q.Arguments, q.Body, q.OperationID, q.Revision)
	case "subscribe":
		err := r.subscribe(ctx, q.Queries)
		return map[string]int{"subscriptions": len(q.Queries)}, err
	default:
		return nil, domain.Fail(domain.Denied, "operation is outside the foreground mobile capability")
	}
}

func envelope(value any, operation string, err error) []byte {
	response := api.Envelope{OK: err == nil, OperationID: operation, Warnings: []string{}}
	if err != nil {
		response.Error = domain.AsFault(err)
	} else {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			response.OK, response.Error = false, domain.AsFault(marshalErr)
		} else {
			response.Data = data
		}
	}
	data, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return []byte(`{"ok":false,"error":{"code":"unavailable","message":"mobile response encoding failed","retryable":false}}`)
	}
	return data
}
