//go:build e2e

package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Keep owned fixture roots private, short, and independent of the host's TMPDIR.
func makeFixtureRoot() (string, error) {
	root, err := os.MkdirTemp(".", ".fdn-")
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(root))
	}
	return absolute, nil
}

func fixtureRoot(t testing.TB) string {
	t.Helper()
	root, err := makeFixtureRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}
