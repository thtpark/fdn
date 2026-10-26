package transport

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/thtpark/fdn/internal/domain"
)

// ServeConnections owns only the listener and connections it accepts. Protocol
// handlers remain independent, and cancellation joins every owned connection.
func ServeConnections(ctx context.Context, listener net.Listener, serve func(context.Context, net.Conn) error, report func(error)) error {
	if listener == nil || serve == nil || report == nil {
		return domain.Fail(domain.Invalid, "message listener requires a handler and explicit error reporting")
	}
	const maxConnections = 2048
	slots := make(chan struct{}, maxConnections)
	var mu sync.Mutex
	connections := map[net.Conn]struct{}{}
	closeConnections := func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for conn := range connections {
			_ = conn.Close()
		}
	}
	stop := context.AfterFunc(ctx, closeConnections)
	var workers sync.WaitGroup
	defer func() {
		stop()
		closeConnections()
		workers.Wait()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			report(domain.Fail(domain.Unavailable, "message connection limit reached"))
			continue
		}
		mu.Lock()
		connections[conn] = struct{}{}
		mu.Unlock()
		if ctx.Err() != nil {
			_ = conn.Close()
		}
		workers.Go(func() {
			defer func() {
				_ = conn.Close()
				mu.Lock()
				delete(connections, conn)
				mu.Unlock()
				<-slots
			}()
			if err := serve(ctx, conn); err != nil && ctx.Err() == nil &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) &&
				domain.AsFault(err).Code != domain.Cancelled {
				report(err)
			}
		})
	}
}
