package software

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func gzipTar(t *testing.T, headers []*tar.Header, contents [][]byte) []byte {
	t.Helper()
	var result bytes.Buffer
	writer := gzip.NewWriter(&result)
	if _, err := writer.Write(writeRawTar(t, headers, contents)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestBundleDeterministicInspectAndSafeExtraction(t *testing.T) {
	root := testDirectory(t)
	writeTestFile(t, filepath.Join(root, "bin", "tool"), []byte("synthetic tool"), 0755)
	writeTestFile(t, filepath.Join(root, "share", "notice"), []byte("notice"), 0644)
	outputs := []domain.BuildOutput{{Path: "share/notice", Mode: 0644}, {Path: "bin/tool", Mode: 0755}}
	var first, second bytes.Buffer
	payloads, err := CreateBundle(&first, root, outputs, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []domain.BuildOutput{outputs[1], outputs[0]}
	other, err := CreateBundle(&second, root, reversed, 1700000000)
	if err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) || !reflect.DeepEqual(payloads, other) {
		t.Fatalf("nondeterministic archive: %v", err)
	}
	inspected, err := InspectBundle(first.Bytes())
	if err != nil || !reflect.DeepEqual(inspected, payloads) {
		t.Fatalf("inspect: %#v %v", inspected, err)
	}
	platform := domain.SoftwarePlatform{Payloads: payloads}
	if err := VerifyPayloads(inspected, platform); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "extracted")
	if err := ExtractBundle(first.Bytes(), destination, payloads); err != nil {
		t.Fatal(err)
	}
	if err := verifyTree(destination, payloads, false); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBundle(first.Bytes(), destination, payloads); err == nil {
		t.Fatal("existing extraction destination was overwritten")
	}
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBundle(first.Bytes(), empty, payloads); err == nil {
		t.Fatal("existing empty destination was adopted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(destination, link); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBundle(first.Bytes(), filepath.Join(link, "escape"), payloads); err == nil {
		t.Fatal("symlink ancestor was accepted")
	}
	payloads[0].SHA256 = domain.Hash("different")
	if err := ExtractBundle(first.Bytes(), filepath.Join(root, "mismatched"), payloads); err == nil {
		t.Fatal("wrong signed payload hash was accepted")
	}
	if err := VerifyPayloads(inspected, domain.SoftwarePlatform{Payloads: payloads}); err == nil {
		t.Fatal("wrong signed inventory was accepted")
	}
}

func TestBundleRejectsPathsLinksDuplicatesModesAndExtensions(t *testing.T) {
	cases := []struct {
		name    string
		headers []*tar.Header
		data    [][]byte
	}{
		{"absolute", []*tar.Header{{Name: "/escape", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"traversal", []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"unclean", []*tar.Header{{Name: "a/../escape", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"backslash", []*tar.Header{{Name: `a\escape`, Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"symlink", []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape", Mode: 0755, Format: tar.FormatUSTAR}}, nil},
		{"hardlink", []*tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "other", Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"directory", []*tar.Header{{Name: "directory/", Typeflag: tar.TypeDir, Mode: 0755, Format: tar.FormatUSTAR}}, nil},
		{"setuid", []*tar.Header{{Name: "tool", Typeflag: tar.TypeReg, Mode: 04755, Format: tar.FormatUSTAR}}, nil},
		{"world-write", []*tar.Header{{Name: "tool", Typeflag: tar.TypeReg, Mode: 0666, Format: tar.FormatUSTAR}}, nil},
		{"missing-mode", []*tar.Header{{Name: "tool", Typeflag: tar.TypeReg, Mode: 0, Format: tar.FormatUSTAR}}, nil},
		{"duplicate", []*tar.Header{{Name: "tool", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR},
			{Name: "tool", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"file-parent", []*tar.Header{{Name: "tool", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR},
			{Name: "tool/child", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"parent-file", []*tar.Header{{Name: "tool/child", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR},
			{Name: "tool", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"fifo", []*tar.Header{{Name: "fifo", Typeflag: tar.TypeFifo, Mode: 0644, Format: tar.FormatUSTAR}}, nil},
		{"pax", []*tar.Header{{Name: "tool", Typeflag: tar.TypeReg, Mode: 0644, Format: tar.FormatPAX,
			PAXRecords: map[string]string{"comment": "opaque"}}}, nil},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := InspectBundle(gzipTar(t, test.headers, test.data)); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func TestBundleBuildRootPinsOwnedWorkspaceBelowSharedAncestor(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "owned-workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tool"), []byte("approved build output"), 0755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := CreateBundle(&archive, root, []domain.BuildOutput{{Path: "tool", Mode: 0755}}, 1); err != nil {
		t.Fatal("owned workspace was confused with a privileged installer path", err)
	}
	if err := protectedPath(root, uint32(os.Geteuid()), false); err == nil {
		t.Fatal("native installer-policy ancestor protection was weakened")
	}
	link := filepath.Join(parent, "linked-workspace")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBundle(io.Discard, link, []domain.BuildOutput{{Path: "tool", Mode: 0755}}, 1); err == nil {
		t.Fatal("direct symlink build root was adopted")
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBundle(io.Discard, root, []domain.BuildOutput{{Path: "tool", Mode: 0755}}, 1); err == nil {
		t.Fatal("foreign-writable build root was adopted")
	}
}

func TestBundleRejectsCorruptionTrailingStreamsAndBombs(t *testing.T) {
	valid := gzipTar(t, []*tar.Header{{Name: "file", Mode: 0644, Size: 4, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}}, [][]byte{[]byte("data")})
	corrupt := append([]byte{}, valid...)
	corrupt[len(corrupt)-8] ^= 1
	for name, data := range map[string][]byte{
		"checksum": corrupt, "truncated": valid[:len(valid)-4],
		"concatenated": append(append([]byte{}, valid...), valid...),
		"trailing":     append(append([]byte{}, valid...), 'x'),
		"empty":        {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectBundle(data); err == nil {
				t.Fatal("corrupt or ambiguous archive was accepted")
			}
		})
	}
	if _, err := InspectBundleReader(bytes.NewReader(valid), int64(len(valid)-1)); err == nil {
		t.Fatal("compressed size limit was ignored")
	}
	var bomb bytes.Buffer
	gz := gzip.NewWriter(&bomb)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "bomb", Mode: 0644, Size: domain.MaxSoftwareBundleBytes + 1,
		Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBundle(bomb.Bytes()); err == nil {
		t.Fatal("expanded archive bomb was accepted")
	}
	headers := make([]*tar.Header, MaxPayloads+1)
	for index := range headers {
		headers[index] = &tar.Header{Name: "f" + strconv.Itoa(index), Mode: 0644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
	}
	if _, err := InspectBundle(gzipTar(t, headers, nil)); err == nil {
		t.Fatal("archive file-count bomb was accepted")
	}
}

func TestBundleCreationRefusesLinkedAndUnboundedSources(t *testing.T) {
	root := testDirectory(t)
	writeTestFile(t, filepath.Join(root, "file"), []byte("source"), 0644)
	if err := os.Symlink("file", filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "file"), filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"symlink", "hardlink", "../escape"} {
		if _, err := CreateBundle(io.Discard, root, []domain.BuildOutput{{Path: name, Mode: 0644}}, time.Now().Unix()); err == nil {
			t.Fatal("linked or escaping build source was accepted", name)
		}
	}
	large := filepath.Join(root, "large")
	file, err := os.OpenFile(large, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(domain.MaxSoftwareBundleBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBundle(io.Discard, root, []domain.BuildOutput{{Path: "large", Mode: 0644}}, 0); err == nil {
		t.Fatal("oversized build source was read")
	}
	writer := &outputBudget{writer: io.Discard, limit: 3}
	if _, err := writer.Write([]byte("four")); err == nil {
		t.Fatal("compressed output bound was ignored")
	}
	if _, err := os.Lstat(filepath.Join(root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive extraction escaped its root")
	}
}
