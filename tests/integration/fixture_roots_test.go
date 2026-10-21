//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp(".", ".fdn-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	absolute, err := filepath.Abs(root)
	if err == nil {
		err = os.Setenv("TMPDIR", absolute)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(root)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
