package remote

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestViewerUsesExplicitPortAndDisablesClipboard(t *testing.T) {
	args := viewerArguments(12345)
	for _, expected := range []string{"-AcceptClipboard=0", "-SendClipboard=0", "127.0.0.1::12345"} {
		if !slices.Contains(args, expected) {
			t.Fatal("missing private viewer policy", expected)
		}
	}
	c := config.DefaultWorker(t.TempDir())
	if _, err := (Native{}).Open(context.Background(), domain.Session{Mode: "vnc"}, c); err == nil {
		t.Fatal("unverified desktop endpoint accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Native{}).Open(ctx, domain.Session{Mode: "tmux"}, c); err == nil {
		t.Fatal("expired/cancelled consent launched terminal")
	}
}

func TestBridgeCancellationStopsBothDirections(t *testing.T) {
	left, leftPeer := net.Pipe()
	right, rightPeer := net.Pipe()
	defer leftPeer.Close()
	defer rightPeer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Bridge(ctx, left, right) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream cancellation leaked blocked relay goroutines")
	}
}
