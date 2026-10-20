package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestDeterministicArchivesAndDebianLayout(t *testing.T) {
	entries := []Entry{{Name: "bin/z", Data: []byte("z"), Mode: 0755}, {Name: "bin/a", Data: []byte("a"), Mode: 0755}}
	var first, second bytes.Buffer
	if err := TarGzip(&first, entries, 123); err != nil {
		t.Fatal(err)
	}
	if err := TarGzip(&second, entries, 123); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("archive not reproducible")
	}
	gz, err := gzip.NewReader(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	for _, expected := range []string{"bin/a", "bin/z"} {
		header, err := reader.Next()
		if err != nil || header.Name != expected || header.Uid != 0 || header.Gid != 0 || header.ModTime.Unix() != 123 {
			t.Fatal(header, err)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatal(err)
	}
	if err := TarGzip(io.Discard, []Entry{{Name: "../escape"}}, 0); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := TarGzip(io.Discard, []Entry{{Name: "same"}, {Name: "same"}}, 0); err == nil {
		t.Fatal("duplicate accepted")
	}
	var deb bytes.Buffer
	if err := Debian(&deb, []Entry{{Name: "control", Data: []byte("Package: fdn-test\n"), Mode: 0644}}, entries, 123); err != nil {
		t.Fatal(err)
	}
	data := deb.Bytes()
	if !bytes.HasPrefix(data, []byte("!<arch>\n")) || !bytes.Contains(data, []byte("debian-binary/")) || !bytes.Contains(data, []byte("control.tar.gz/")) || !bytes.Contains(data, []byte("data.tar.gz/")) {
		t.Fatal("invalid Debian ar members")
	}
}

func TestOnlyMergedReleasePRMayPackage(t *testing.T) {
	commit := strings.Repeat("a", 40)
	var event MergeEvent
	event.Action = "closed"
	event.PullRequest.Merged = true
	event.PullRequest.Base.Ref = "release"
	event.PullRequest.MergeCommitSHA = commit
	if err := CheckReleaseEvent("true", "pull_request", event, commit); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*MergeEvent){
		func(e *MergeEvent) { e.Action = "opened" }, func(e *MergeEvent) { e.PullRequest.Merged = false },
		func(e *MergeEvent) { e.PullRequest.Base.Ref = "main" }, func(e *MergeEvent) { e.PullRequest.MergeCommitSHA = strings.Repeat("b", 40) },
	} {
		copy := event
		change(&copy)
		if CheckReleaseEvent("true", "pull_request", copy, commit) == nil {
			t.Fatal("forbidden event admitted")
		}
	}
	for _, name := range []string{"push", "workflow_dispatch", "pull_request_target", "release"} {
		if CheckReleaseEvent("true", name, event, commit) == nil {
			t.Fatal(name)
		}
	}
	if CheckReleaseEvent("false", "pull_request", event, commit) == nil {
		t.Fatal("local packaging admitted")
	}
}
