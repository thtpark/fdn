package observe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

const Protocol = 1
const MaxFrameBytes = 16 << 20
const MaxQueries = 32

type Source interface {
	Identity(context.Context) (domain.AgentIdentity, error)
	Snapshot(context.Context, Query) (json.RawMessage, error)
	Updates(context.Context, string, domain.UpdateFilter, bool) (domain.Page[domain.Update], error)
	Events(context.Context, string, domain.UpdateFilter, bool) (domain.Page[domain.Event], error)
}

type Request struct {
	ID      string  `json:"id"`
	Op      string  `json:"op"`
	Query   *Query  `json:"query,omitempty"`
	Queries []Query `json:"queries,omitempty"`
}

type Frame struct {
	Protocol int             `json:"protocol"`
	Stream   string          `json:"stream"`
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Query    *Query          `json:"query,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Error    *domain.Fault   `json:"error,omitempty"`
}

type entry struct {
	query      Query
	subscribed bool
	waiters    []string
	inflight   []string
	dirty      bool
	busy       bool
	hash       string
	nextSample time.Time
	nextExpiry time.Time
	threadID   string
	resolve    bool
}

type completion struct {
	key  string
	data json.RawMessage
	err  error
}
type changeBatch struct {
	epoch   uint64
	updates []domain.Update
	err     error
	resync  bool
	public  bool
}
type job struct {
	key   string
	query Query
}

func sameBinding(a, b domain.AgentIdentity) bool {
	return a.AgentID == b.AgentID && a.InstanceID == b.InstanceID && a.DeviceID == b.DeviceID && a.Generation == b.Generation
}

func Run(parent context.Context, input io.Reader, output io.Writer, source Source) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	identity, err := source.Identity(ctx)
	if err != nil {
		return err
	}
	stream := domain.NewID("observe")
	deadlined := false
	if file, ok := output.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			descriptor, err := unix.Dup(int(file.Fd()))
			if err != nil {
				return err
			}
			unix.CloseOnExec(descriptor)
			flags, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFL, 0)
			if err != nil {
				unix.Close(descriptor)
				return err
			}
			if err := unix.SetNonblock(descriptor, true); err != nil {
				unix.Close(descriptor)
				return err
			}
			pipe := os.NewFile(uintptr(descriptor), "observer-output")
			defer func() {
				unix.FcntlInt(uintptr(descriptor), unix.F_SETFL, flags)
				pipe.Close()
			}()
			output, deadlined = pipe, true
		}
	}
	var outputMu sync.Mutex
	emit := func(frame Frame) error {
		frame.Protocol, frame.Stream = Protocol, stream
		data, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if len(data) > MaxFrameBytes {
			return domain.Fail(domain.Prerequisite, "observer snapshot exceeds 16 MiB; reduce the query page size")
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		if file, ok := output.(*os.File); ok && deadlined {
			if err := file.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return domain.Fail(domain.Unavailable, "observer stdout does not support a bounded pipe deadline")
			}
		}
		_, err = output.Write(append(data, '\n'))
		return err
	}
	ready, err := json.Marshal(map[string]any{"version": domain.Version, "commit": domain.Commit,
		"api_version": domain.APIVersion, "schema_version": domain.SchemaVersion,
		"identity": map[string]any{"agent_id": identity.AgentID, "instance_id": identity.InstanceID,
			"device_id": identity.DeviceID, "generation": identity.Generation}})
	if err != nil {
		return err
	}
	if err := emit(Frame{Type: "ready", Data: ready}); err != nil {
		return err
	}
	requests := make(chan Request, 8)
	readErrors := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), domain.MaxBody)
		for scanner.Scan() {
			var request Request
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				readErrors <- domain.Fail(domain.Invalid, "invalid observer input frame")
				return
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				readErrors <- domain.Fail(domain.Invalid, "observer frames require one JSON object per line")
				return
			}
			select {
			case requests <- request:
			case <-ctx.Done():
				return
			}
		}
		readErrors <- scanner.Err()
	}()
	defer func() {
		if closer, ok := input.(io.Closer); ok {
			closer.Close()
		}
	}()
	entries := make(map[string]*entry)
	jobs := make(chan job, MaxQueries)
	results := make(chan completion, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case work := <-jobs:
					read, stop := context.WithTimeout(ctx, 30*time.Second)
					data, err := source.Snapshot(read, work.query)
					stop()
					select {
					case results <- completion{work.key, data, err}:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	defer func() { cancel(); workers.Wait() }()
	changes := make(chan changeBatch, 8)
	var feedCancel context.CancelFunc
	defer func() {
		if feedCancel != nil {
			feedCancel()
		}
	}()
	var epoch uint64
	var filterKey string
	feedHealthy := map[bool]bool{false: true, true: true}
	threadIndex := make(map[string]string)
	schedule := func() {
		for key, item := range entries {
			if item.dirty && !item.busy {
				select {
				case jobs <- job{key, item.query}:
					item.dirty, item.busy = false, true
					item.inflight, item.waiters = item.waiters, nil
				default:
					return
				}
			}
		}
	}
	restart := func(queries []Query) error {
		private, public, err := Filters(queries, identity.AgentID)
		if err != nil {
			return err
		}
		key := private.Fingerprint() + ":" + public.Fingerprint()
		if len(queries) != 0 && key == filterKey {
			return nil
		}
		if len(queries) == 0 {
			if feedCancel != nil {
				feedCancel()
			}
			epoch++
			filterKey = ""
			return nil
		}
		feed, stop := context.WithCancel(ctx)
		// Capture both watermarks before scheduling newly subscribed snapshots.
		first, err := source.Updates(feed, "", private, false)
		if err != nil {
			stop()
			return err
		}
		var shared domain.Page[domain.Event]
		if !public.Empty() {
			shared, err = source.Events(feed, "", public, false)
			if err != nil {
				stop()
				return err
			}
		}
		if feedCancel != nil {
			feedCancel()
		}
		feedCancel = stop
		filterKey = key
		epoch++
		feedHealthy[false], feedHealthy[true] = true, true
		go receive(feed, source, identity, epoch, false, first.NextCursor, private, changes)
		if !public.Empty() {
			go receive(feed, source, identity, epoch, true, shared.NextCursor, public, changes)
		}
		return nil
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErrors:
			return err
		case request := <-requests:
			if !domain.ValidOperation(request.ID) {
				return domain.Fail(domain.Invalid, "observer requests require a bounded correlation ID")
			}
			switch request.Op {
			case "get":
				if request.Query == nil {
					if err := emit(Frame{Type: "result", ID: request.ID, Error: domain.Fail(domain.Invalid, "get requires a query")}); err != nil {
						return err
					}
					continue
				}
				query := *request.Query
				if err := query.Validate(); err != nil {
					if err := emit(Frame{Type: "result", ID: request.ID, Error: domain.AsFault(err)}); err != nil {
						return err
					}
					continue
				}
				key := query.Key()
				item := entries[key]
				if item == nil {
					if len(entries) >= MaxQueries*2 {
						return domain.Fail(domain.Invalid, "observer query limit reached")
					}
					item = &entry{query: query}
					entries[key] = item
				}
				if len(item.waiters) >= 8 {
					return domain.Fail(domain.Invalid, "too many concurrent requests for one query")
				}
				item.waiters = append(item.waiters, request.ID)
				item.dirty = true
				schedule()
			case "subscribe":
				if len(request.Queries) > MaxQueries {
					return domain.Fail(domain.Invalid, "observer supports at most 32 active queries")
				}
				for _, query := range request.Queries {
					if err := query.Validate(); err != nil || !query.Subscribable() {
						return domain.Fail(domain.Invalid, "unsupported automatic subscription")
					}
					pendingKeys := make(map[string]bool)
					for key, item := range entries {
						if item.busy || len(item.waiters) != 0 {
							pendingKeys[key] = true
						}
					}
					for _, query := range request.Queries {
						pendingKeys[query.Key()] = true
					}
					if len(pendingKeys) > MaxQueries*2 {
						if err := emit(Frame{Type: "result", ID: request.ID, Error: domain.Fail(domain.Unavailable, "observer has too many pending queries; allow cancellation/completion before replacing subscriptions")}); err != nil {
							return err
						}
						continue
					}
				}
				if err := restart(request.Queries); err != nil {
					if err := emit(Frame{Type: "result", ID: request.ID, Error: domain.AsFault(err)}); err != nil {
						return err
					}
					continue
				}
				for _, item := range entries {
					item.subscribed = false
				}
				for _, query := range request.Queries {
					key := query.Key()
					item := entries[key]
					if item == nil {
						item = &entry{query: query, dirty: true}
						entries[key] = item
					}
					item.subscribed = true
				}
				for key, item := range entries {
					if !item.subscribed && !item.busy && len(item.waiters) == 0 {
						delete(entries, key)
					}
				}
				if err := emit(Frame{Type: "result", ID: request.ID, Data: json.RawMessage(`{"subscribed":true}`)}); err != nil {
					return err
				}
				schedule()
			case "cancel":
				for _, item := range entries {
					for _, ids := range []*[]string{&item.waiters, &item.inflight} {
						for i, id := range *ids {
							if id == request.ID {
								*ids = append((*ids)[:i], (*ids)[i+1:]...)
								break
							}
						}
					}
				}
			default:
				return domain.Fail(domain.Invalid, "observer accepts get, subscribe and cancel only; writes are not supported")
			}
		case result := <-results:
			item := entries[result.key]
			if item == nil {
				continue
			}
			item.busy = false
			if result.err == nil && (item.query.Kind == "message" || item.query.Kind == "thread" || item.query.Kind == "threadPage") {
				roots, err := messageRoots(result.data, item.query.Kind)
				if err != nil {
					result.err = err
				}
				for id, thread := range roots {
					if len(threadIndex) >= 1024 {
						clear(threadIndex)
					}
					threadIndex[id] = thread
					if item.query.Kind != "message" {
						item.threadID = thread
					}
				}
			}
			if item.resolve {
				if result.err != nil {
					for _, candidate := range entries {
						if candidate.subscribed && (candidate.query.Kind == "thread" || candidate.query.Kind == "threadPage") {
							if err := emit(Frame{Type: "snapshot", Query: &candidate.query, Error: domain.AsFault(result.err)}); err != nil {
								return err
							}
						}
					}
				}
				if thread := threadIndex[item.query.ID]; thread != "" {
					for _, candidate := range entries {
						if candidate.subscribed && (candidate.query.Kind == "thread" || candidate.query.Kind == "threadPage") &&
							(candidate.threadID == thread || candidate.query.ID == item.query.ID) {
							candidate.dirty = true
						}
					}
				}
				delete(entries, result.key)
				schedule()
				continue
			}
			if len(result.data) > MaxFrameBytes-4096 {
				result.err = domain.Fail(domain.Prerequisite, "observer snapshot too large; request a smaller page")
			}
			frame := Frame{Type: "result", Query: &item.query, Data: result.data}
			if result.err != nil {
				frame.Data, frame.Error = nil, domain.AsFault(result.err)
			}
			for _, id := range item.inflight {
				frame.ID = id
				if err := emit(frame); err != nil {
					return err
				}
			}
			item.inflight = nil
			if item.subscribed && (result.err != nil || item.hash != domain.Hash(string(result.data))) {
				frame.Type, frame.ID = "snapshot", ""
				if err := emit(frame); err != nil {
					return err
				}
				if result.err == nil {
					item.hash = domain.Hash(string(result.data))
				}
			}
			item.nextSample = time.Now().Add(item.query.SampleInterval())
			item.nextExpiry = earliestExpiry(result.data, time.Now())
			if !item.subscribed && len(item.waiters) == 0 {
				delete(entries, result.key)
			}
			schedule()
		case batch := <-changes:
			if batch.epoch != epoch {
				continue
			}
			if batch.err != nil {
				feedHealthy[batch.public] = false
				if err := emit(Frame{Type: "connection", Error: domain.AsFault(batch.err)}); err != nil {
					return err
				}
			}
			for _, item := range entries {
				if !item.subscribed {
					continue
				}
				for _, update := range batch.updates {
					if update.Topic == "message" && (item.query.Kind == "thread" || item.query.Kind == "threadPage") {
						if thread := threadIndex[update.SourceID]; thread != "" {
							if thread == item.threadID || update.SourceID == item.query.ID {
								item.dirty = true
							}
						} else if len(entries) < MaxQueries*2 {
							key := "thread-metadata:" + update.SourceID
							if entries[key] == nil {
								entries[key] = &entry{query: Query{Kind: "message", ID: update.SourceID}, dirty: true, resolve: true}
							}
						}
						continue
					}
					if item.query.Matches(update, identity.AgentID) {
						item.dirty = true
					}
				}
				if batch.resync {
					item.dirty = true
				}
			}
			if batch.resync {
				feedHealthy[batch.public] = true
				if feedHealthy[false] && feedHealthy[true] {
					if err := emit(Frame{Type: "connection", Data: json.RawMessage(`{"live":true,"resynchronized":true}`)}); err != nil {
						return err
					}
				}
			}
		case now := <-ticker.C:
			for _, item := range entries {
				if item.subscribed && ((item.query.SampleInterval() != 0 && !now.Before(item.nextSample)) || (!item.nextExpiry.IsZero() && !now.Before(item.nextExpiry))) {
					item.dirty = true
					item.nextExpiry = time.Time{}
				}
			}
			schedule()
		}
	}
}

func messageRoots(data json.RawMessage, kind string) (map[string]string, error) {
	type metadata struct {
		ID       string `json:"id"`
		ThreadID string `json:"thread_id"`
	}
	values := []metadata{}
	switch kind {
	case "message":
		var value metadata
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, domain.Fail(domain.Internal, "invalid observed message metadata")
		}
		values = append(values, value)
	case "thread":
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, domain.Fail(domain.Internal, "invalid observed thread metadata")
		}
	case "threadPage":
		var page struct {
			Items []metadata `json:"items"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, domain.Fail(domain.Internal, "invalid observed thread page metadata")
		}
		values = page.Items
	}
	result := make(map[string]string)
	for _, value := range values {
		if value.ID == "" {
			return nil, domain.Fail(domain.Internal, "observed message metadata omitted an ID")
		}
		if value.ThreadID == "" {
			value.ThreadID = value.ID
		}
		result[value.ID] = value.ThreadID
	}
	return result, nil
}

func receive(ctx context.Context, source Source, identity domain.AgentIdentity, epoch uint64, public bool, cursor string, filter domain.UpdateFilter, changes chan<- changeBatch) {
	backoff := time.Second
	resync := false
	for ctx.Err() == nil {
		var batch changeBatch
		batch.epoch, batch.public = epoch, public
		var next string
		if public {
			page, err := source.Events(ctx, cursor, filter, cursor != "")
			batch.err, next = err, page.NextCursor
			for _, event := range page.Items {
				batch.updates = append(batch.updates, domain.Update{Sequence: event.Sequence, Topic: event.Topic, SourceID: event.SourceID, TaskID: event.TaskID, ProjectID: event.ProjectID})
			}
		} else {
			page, err := source.Updates(ctx, cursor, filter, cursor != "")
			batch.err, next, batch.updates = err, page.NextCursor, page.Items
		}
		if ctx.Err() != nil {
			return
		}
		batch.resync = resync && batch.err == nil
		select {
		case changes <- batch:
		case <-ctx.Done():
			return
		}
		if batch.err == nil {
			cursor, backoff, resync = next, time.Second, false
			continue
		}
		current, err := source.Identity(ctx)
		if err == nil && !sameBinding(current, identity) {
			select {
			case changes <- changeBatch{epoch: epoch, err: domain.Fail(domain.Denied, "observer identity binding changed; reconnect explicitly")}:
			case <-ctx.Done():
			}
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		cursor, resync = "", true
		backoff = min(backoff*2, 30*time.Second)
	}
}

func earliestExpiry(data []byte, now time.Time) time.Time {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return time.Time{}
	}
	var first time.Time
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if expiry, ok := v["expires_at"].(float64); ok {
				at := time.Unix(int64(expiry), 0)
				if at.After(now) && (first.IsZero() || at.Before(first)) {
					first = at
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(value)
	return first
}
