package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestHumanTerminalBytesStayOutsideJSONAndRequireExactOwnedHandle(t *testing.T) {
	r := testRuntime(t)
	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &humanSession{id: 1, conn: left, cancel: cancel, done: make(chan struct{})}
	r.human = h
	go func() {
		<-ctx.Done()
		left.Close()
		right.Close()
		close(h.done)
	}()
	secret := []byte("fictional-protected-terminal-fixture")
	go func() {
		if _, err := right.Write(secret); err != nil {
			t.Error(err)
		}
	}()
	buffer := make([]byte, 4096)
	n, err := r.HumanRead(1, buffer)
	if err != nil || !bytes.Equal(buffer[:n], secret) {
		t.Fatal("binary protected output was not delivered to its owned human reader", n, err)
	}
	if _, err := r.HumanRead(2, buffer); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another viewer handle accessed protected bytes", err)
	}
	response := call(t, r, Request{ID: "no-transcript-query", Op: "human.read", HumanID: 1})
	encoded, err := json.Marshal(response)
	if err != nil || response.OK || strings.Contains(string(encoded), string(secret)) {
		t.Fatal("terminal bytes became a structured query result", err)
	}
	read := make(chan []byte, 1)
	go func() {
		data := make([]byte, len(secret))
		if _, err := io.ReadFull(right, data); err != nil {
			t.Error(err)
		}
		read <- data
	}()
	if n, err := r.HumanWrite(1, secret); err != nil || n != len(secret) {
		t.Fatal("protected binary input failed", n, err)
	}
	if !bytes.Equal(<-read, secret) {
		t.Fatal("protected input changed")
	}
	if _, err := r.HumanWrite(1, make([]byte, (64<<10)+1)); err == nil {
		t.Fatal("unbounded protected input accepted")
	}
	if err := r.CloseHuman(1); err != nil {
		t.Fatal(err)
	}
	<-h.done
	if _, err := r.HumanRead(1, buffer); err == nil {
		t.Fatal("closed viewer retained an active terminal stream")
	}
}

func TestHumanBootstrapIsPrivateAndForegroundStopJoinsViewer(t *testing.T) {
	r := testRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := &humanSession{id: 7, cancel: cancel, done: make(chan struct{})}
	r.human = h
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		h.bootstrap = ""
		h.mu.Unlock()
		close(h.done)
	}()
	url := "http://127.0.0.1:12345/bootstrap?key=" + strings.Repeat("x", 43)
	_, err := (humanBootstrap{h}).Write([]byte("Human viewer only\n" + url + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.HumanURL(7)
	if err != nil || got != url {
		t.Fatal("owned human view could not obtain its private bootstrap", err)
	}
	response := call(t, r, Request{ID: "metadata-only", Op: "runtime.status"})
	data, _ := json.Marshal(response)
	if bytes.Contains(data, []byte(url)) || bytes.Contains(data, []byte("bootstrap?key")) {
		t.Fatal("credential-bearing viewer URL leaked into status metadata")
	}
	if err := r.Stop(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	got, err = r.HumanURL(7)
	if err != nil || got != "" {
		t.Fatal("foreground stop retained protected browser credentials", err)
	}
}
