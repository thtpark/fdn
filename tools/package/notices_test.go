package main

import (
	"bytes"
	"testing"
)

func TestRuntimeDependencyNoticesCanBeCollectedWithoutPackaging(t *testing.T) {
	data, err := thirdPartyNotices()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{[]byte("Go runtime"), []byte("modernc.org/sqlite"), []byte("filippo.io/age"), []byte("golang.org/x/term")} {
		if !bytes.Contains(data, expected) {
			t.Fatal("missing required notice", string(expected))
		}
	}
	if bytes.Contains(data, []byte("github.com/thtpark/fdn")) {
		t.Fatal("private project source was treated as a third-party module")
	}
}
