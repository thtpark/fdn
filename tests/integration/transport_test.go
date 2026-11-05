//go:build integration

package integration_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/api"
)

type switchableFixtureTransport struct {
	mu      sync.RWMutex
	current http.RoundTripper
}

func (transport *switchableFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.RLock()
	current := transport.current
	transport.mu.RUnlock()
	return current.RoundTrip(request)
}

func (transport *switchableFixtureTransport) CloseIdleConnections() {
	transport.mu.RLock()
	current := transport.current
	transport.mu.RUnlock()
	if closer, ok := current.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func fixtureTransport(t *testing.T, client *api.Client) http.RoundTripper {
	t.Helper()
	transport, ok := client.HTTP.Transport.(*switchableFixtureTransport)
	if !ok {
		t.Fatal("fixture client has no immutable switchable transport")
	}
	transport.mu.RLock()
	defer transport.mu.RUnlock()
	return transport.current
}

func setFixtureTransport(t *testing.T, client *api.Client, next http.RoundTripper) {
	t.Helper()
	transport, ok := client.HTTP.Transport.(*switchableFixtureTransport)
	if !ok || next == nil || next == transport {
		t.Fatal("fixture transport switch requires its existing client and an independent handler")
	}
	transport.mu.Lock()
	transport.current = next
	transport.mu.Unlock()
}
