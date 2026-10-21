package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/distribution"
)

func TestReleaseBuildListsIncludeIsolatedIdentityService(t *testing.T) {
	if !slices.Equal(nativeBinaries, []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"}) {
		t.Fatal("native archive/reproducible build list is incomplete", nativeBinaries)
	}
	if !slices.Equal(workerBinaries, []string{"agent-cli", "agent-service", "agent-identity"}) {
		t.Fatal("Debian worker payload is incomplete or includes the server", workerBinaries)
	}
	for _, name := range workerBinaries {
		if !slices.Contains(nativeBinaries, name) {
			t.Fatal("worker binary is absent from the reproducible build list", name)
		}
	}
}

func TestFoundationPackageUnitUsesSeparateWritableIngress(t *testing.T) {
	unit, err := foundationUnit()
	if err != nil {
		t.Fatal(err)
	}
	if unit.Path != "/lib/systemd/system/fdn-foundation.service" {
		t.Fatal("unexpected packaged Foundation definition", unit.Path)
	}
	for _, required := range []string{
		"User=fdn\nGroup=fdn\n", "SupplementaryGroups=fdn_ingress\n", "ProtectSystem=strict\n",
		"ReadWritePaths=\"/var/lib/fdn\" \"/run/fdn-ingress\"\n",
		"ExecStart=\"/usr/bin/fdn-service\" --config \"/var/lib/fdn/server.json\" serve\n",
	} {
		if !strings.Contains(unit.Content, required) {
			t.Fatal("packaged Foundation unit lost secure ingress configuration", required)
		}
	}
}

func TestBinaryPayloadEntriesPreserveNamesModesAndContents(t *testing.T) {
	directory := t.TempDir()
	for _, name := range nativeBinaries {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, prefix string
		binaries     []string
	}{{"native", "bin", nativeBinaries}, {"debian-worker", "usr/bin", workerBinaries}} {
		t.Run(test.name, func(t *testing.T) {
			entries := binaryEntries(directory, test.prefix, test.binaries)
			if len(entries) != len(test.binaries) {
				t.Fatal("incomplete binary entries", entries)
			}
			var archive bytes.Buffer
			if err := distribution.TarGzip(&archive, entries, 123); err != nil {
				t.Fatal(err)
			}
			compressed, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer compressed.Close()
			reader := tar.NewReader(compressed)
			seen := map[string]bool{}
			for {
				header, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				name := filepath.Base(header.Name)
				if !slices.Contains(test.binaries, name) || header.Name != test.prefix+"/"+name || seen[name] || header.Mode != 0755 || header.Typeflag != tar.TypeReg {
					t.Fatal("unexpected executable payload", header)
				}
				content, err := io.ReadAll(reader)
				if err != nil || string(content) != "fixture-"+name {
					t.Fatal("binary content changed", name, err)
				}
				seen[name] = true
			}
			if len(seen) != len(test.binaries) {
				t.Fatal("missing executable payload", seen)
			}
		})
	}
}

func TestVerifyExtractedBinaryRejectsChangesAndNonexecutables(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	if err := os.WriteFile(source, []byte("identity-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"exact", "changed", "nonexecutable", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			installed := filepath.Join(directory, kind)
			switch kind {
			case "symlink":
				if err := os.Symlink(source, installed); err != nil {
					t.Fatal(err)
				}
			case "exact", "changed", "nonexecutable":
				content, mode := []byte("identity-binary"), os.FileMode(0755)
				if kind == "changed" {
					content = []byte("wrong-binary")
				}
				if kind == "nonexecutable" {
					mode = 0644
				}
				if err := os.WriteFile(installed, content, mode); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyBinary(source, installed)
			if (err == nil) != (kind == "exact") {
				t.Fatal("unexpected extracted-binary verification", kind, err)
			}
		})
	}
}
