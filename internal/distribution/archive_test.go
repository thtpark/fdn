package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

func TestDebianPayloadExtractsIntoAnEmptyRoot(t *testing.T) {
	entries := []Entry{
		{Name: "usr/bin/agent-cli", Data: []byte("fixture-cli"), Mode: 0755},
		{Name: "usr/bin/agent-service", Data: []byte("fixture-runner"), Mode: 0755},
		{Name: "usr/bin/agent-identity", Data: []byte("fixture-identity"), Mode: 0755},
		{Name: "usr/share/doc/fdn-agent/copyright", Data: []byte("license notices"), Mode: 0644},
	}
	var archive bytes.Buffer
	if err := Debian(&archive, []Entry{{Name: "control", Data: []byte("Package: fdn-agent\n"), Mode: 0644}}, entries, 123); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()[8:]
	var payload []byte
	for len(data) != 0 {
		if len(data) < 60 {
			t.Fatal("truncated Debian member header")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(data[:16])), "/")
		size, err := strconv.Atoi(strings.TrimSpace(string(data[48:58])))
		if err != nil || size < 0 || len(data) < 60+size {
			t.Fatal("invalid Debian member length", err)
		}
		if name == "data.tar.gz" {
			payload = data[60 : 60+size]
		}
		data = data[60+size+size%2:]
	}
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	root := t.TempDir()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, filepath.FromSlash(header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Mode != 0755 || header.Uid != 0 || header.Gid != 0 || header.ModTime.Unix() != 123 {
				t.Fatal("noncanonical directory metadata", header)
			}
			if err := os.Mkdir(target, os.FileMode(header.Mode)); err != nil {
				t.Fatal("parent directories were not emitted before their children", err)
			}
		case tar.TypeReg:
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, content, os.FileMode(header.Mode)); err != nil {
				t.Fatal("payload cannot install into an empty root", err)
			}
		default:
			t.Fatal("unexpected payload entry type")
		}
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Name)))
		if err != nil || !bytes.Equal(content, entry.Data) {
			t.Fatal("extracted payload differs", entry.Name, err)
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry.Name))); err != nil || info.Mode().Perm() != os.FileMode(entry.Mode) {
			t.Fatal("extracted payload permissions differ", entry.Name, err)
		}
	}
	for _, invalid := range [][]Entry{
		{{Name: "usr", Data: []byte("not a directory")}, {Name: "usr/bin/tool"}},
		{{Name: "../escape"}},
		{{Name: "same"}, {Name: "same"}},
	} {
		if err := Debian(io.Discard, nil, invalid, 0); err == nil {
			t.Fatal("invalid Debian payload accepted")
		}
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
