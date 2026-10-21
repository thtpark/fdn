package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
)

func TestIdentityCommandOfflineHelpAndStrictPrivateConfiguration(t *testing.T) {
	var output, errors bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &output, &errors); code != 0 {
		t.Fatal(code)
	}
	for _, word := range []string{"prepare", "connect", "serve", "dedicated", "public", "No bearer"} {
		if !strings.Contains(output.String(), word) {
			t.Fatal("identity command help omitted its security contract", word)
		}
	}
	root, err := os.MkdirTemp(filepath.Join("..", ".."), ".id-command-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	file := filepath.Join(root, "identity.json")
	c := config.IdentityService{StateDir: filepath.Join(root, "private"), Socket: filepath.Join(root, "ipc", "identity.sock"), WorkerUID: os.Geteuid(), WorkerGID: os.Getegid()}
	if err := config.Write(file, c); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"prepare", "connect", "serve"} {
		output.Reset()
		if code := run(context.Background(), []string{"--config", file, command}, &output, &errors); code != 4 {
			t.Fatal("identity native entrypoint accepted its own runner UID", command, code)
		}
		var result api.Envelope
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.OK || result.Error == nil || strings.Contains(output.String(), "PRIVATE KEY") {
			t.Fatal("invalid or sensitive native error envelope", err)
		}
	}
	if err := os.WriteFile(file, []byte(`{"state_dir":"/private","socket":"/ipc/s","worker_uid":5,"worker_gid":5,"test_uid":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := run(context.Background(), []string{"--config", file, "serve"}, &output, &errors); code != 2 {
		t.Fatal("native configuration accepted a runtime test-UID override", code)
	}
}
