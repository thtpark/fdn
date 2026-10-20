package pki

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/secure"
)

func TestRenewalPreservesPinnedCAAndReplacesServerKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	bundle, err := Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(root); err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem")
	if err := Renew(root, certFile, keyFile, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ca, err := secure.Read(filepath.Join(root, "ca.pem"), 1<<20)
	if err != nil || !bytes.Equal(ca, bundle.CA) {
		t.Fatal("trust pins changed", err)
	}
	key, err := secure.Read(keyFile, 1<<20)
	if err != nil || bytes.Equal(key, bundle.Key) {
		t.Fatal("server key not renewed", err)
	}
	if _, err := Server(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	if err := Renew(root, certFile, keyFile, time.Now().AddDate(11, 0, 0)); err == nil {
		t.Fatal("expired CA was silently replaced")
	}
}

func TestPinnedTLS(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	bundle, err := Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(root); err != nil {
		t.Fatal(err)
	}
	serverTLS, err := Server(filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	clientTLS, err := Client(filepath.Join(root, "ca.pem"), "")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.TLS.Version != tls.VersionTLS13 {
		t.Fatal("TLS not 1.3")
	}
	wrong, err := Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "private")
	if err := wrong.Write(other); err != nil {
		t.Fatal(err)
	}
	wrongTLS, err := Client(filepath.Join(other, "ca.pem"), "")
	if err != nil {
		t.Fatal(err)
	}
	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: wrongTLS}, Timeout: time.Second}
	defer untrusted.CloseIdleConnections()
	if _, err := untrusted.Get(server.URL); err == nil {
		t.Fatal("wrong CA accepted")
	}
}
