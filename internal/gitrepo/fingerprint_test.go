package gitrepo

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func testPackLevel(t testing.TB, level int, records ...testRecord) []byte {
	t.Helper()
	records = append([]testRecord(nil), records...)
	for i := range records {
		var compressed bytes.Buffer
		writer, err := zlib.NewWriterLevel(&compressed, level)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(records[i].data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		records[i].compressed = bytes.Clone(compressed.Bytes())
	}
	return testPack(t, records...)
}

func fingerprintValue(t testing.TB, b *Bundle, commit string) string {
	t.Helper()
	value, err := b.Fingerprint(commit)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(value) != value {
		t.Fatalf("fingerprint is not a lowercase SHA256 digest: %q", value)
	}
	return value
}

func TestFingerprintChangesWithSource(t *testing.T) {
	source := func(content []byte, path, mode, message string) (*Bundle, string) {
		blobID := testID(blobObject, content)
		tree := rawTestTree(testTreeEntry{mode, path, blobID})
		commit := bytes.Replace(testCommit(testID(treeObject, tree)), []byte("original fixture\n"), []byte(message+"\n"), 1)
		id := testID(commitObject, commit)
		data := testBundle(map[string]objectID{"refs/heads/main": id}, testPack(t,
			testRecord{kind: blobObject, data: content}, testRecord{kind: treeObject, data: tree},
			testRecord{kind: commitObject, data: commit}))
		b, err := Open(data)
		if err != nil {
			t.Fatal(err)
		}
		return b, id.String()
	}
	b, head := source([]byte("original fixture\n"), "file", "100644", "original fixture")
	original := fingerprintValue(t, b, head)
	for _, test := range []struct {
		name    string
		content string
		path    string
		mode    string
		message string
	}{
		{"bytes", "modified fixture\n", "file", "100644", "original fixture"},
		{"path", "original fixture\n", "code", "100644", "original fixture"},
		{"mode", "original fixture\n", "file", "100755", "original fixture"},
		{"message", "original fixture\n", "file", "100644", "modified fixture"},
		{"symlink-mode", "original fixture\n", "file", "120000", "original fixture"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, commit := source([]byte(test.content), test.path, test.mode, test.message)
			if fingerprintValue(t, changed, commit) == original {
				t.Fatal("differing source produced the same fingerprint")
			}
		})
	}
}

func independentFingerprint(kind objectType, raw []byte, children ...[32]byte) [32]byte {
	encoded := []byte("foundation/gitrepo/fingerprint/v1\x00")
	encoded = append(encoded, byte(kind))
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(raw)))
	encoded = append(encoded, raw...)
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(children)))
	for _, child := range children {
		encoded = append(encoded, child[:]...)
	}
	return sha256.Sum256(encoded)
}

func TestFingerprintCanonicalMerkleEncoding(t *testing.T) {
	data, head, _ := basicBundle(t)
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	records, _, _ := basicRecords()
	blob := independentFingerprint(blobObject, records[0].data)
	tree := independentFingerprint(treeObject, records[1].data, blob)
	commit := independentFingerprint(commitObject, records[2].data, tree)
	if fingerprintValue(t, b, head) != hex.EncodeToString(commit[:]) {
		t.Fatal("fingerprint does not bind canonical raw contents and strong child digests")
	}
	base := testID(commitObject, records[2].data)
	other := bytes.Replace(records[2].data, []byte("original fixture\n"), []byte("other fixture\n"), 1)
	otherID := testID(commitObject, other)
	treeID := testID(treeObject, records[1].data)
	merge := testCommit(treeID, base, otherID)
	mergeID := testID(commitObject, merge)
	otherFingerprint := independentFingerprint(commitObject, other, tree)
	mergeFingerprint := independentFingerprint(commitObject, merge, tree, commit, otherFingerprint)
	records = append(records, testRecord{kind: commitObject, data: other}, testRecord{kind: commitObject, data: merge})
	merged, err := Open(testBundle(map[string]objectID{"refs/heads/main": mergeID}, testPack(t, records...)))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprintValue(t, merged, mergeID.String()) != hex.EncodeToString(mergeFingerprint[:]) {
		t.Fatal("fingerprint did not bind parents in their canonical Git order")
	}
}

// This fixture models differing bytes at retained SHA1 locators without
// claiming a real SHA1 collision. Public Open still verifies every SHA1 hash.
func fingerprintModel(t testing.TB, source *Bundle, edit func(*object)) *Bundle {
	t.Helper()
	b := &Bundle{refs: source.Refs(), objects: make(map[objectID]*object)}
	ordered := make([]*object, 0, len(source.objects))
	for id, original := range source.objects {
		o := &object{kind: original.kind, id: id, data: bytes.Clone(original.data)}
		edit(o)
		b.objects[id] = o
		ordered = append(ordered, o)
	}
	if err := b.validateObjects(ordered); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFingerprintBindsBytesBehindUnchangedLocators(t *testing.T) {
	data, head, _ := basicBundle(t)
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	expected := fingerprintValue(t, b, head)
	headID, _ := decodeID([]byte(head))
	for _, test := range []struct {
		name string
		edit func(*object)
	}{
		{"blob-bytes", func(o *object) {
			if o.kind == blobObject {
				o.data = bytes.Replace(o.data, []byte("original"), []byte("modified"), 1)
			}
		}},
		{"tree-mode", func(o *object) {
			if o.kind == treeObject {
				o.data = bytes.Replace(o.data, []byte("100644 "), []byte("100755 "), 1)
			}
		}},
		{"tree-path", func(o *object) {
			if o.kind == treeObject {
				o.data = bytes.Replace(o.data, []byte("file\x00"), []byte("code\x00"), 1)
			}
		}},
		{"commit-metadata", func(o *object) {
			if o.kind == commitObject {
				o.data = bytes.Replace(o.data, []byte("original fixture\n"), []byte("modified fixture\n"), 1)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := fingerprintModel(t, b, test.edit)
			if !changed.Contains(head) || changed.objects[headID].tree != b.objects[headID].tree {
				t.Fatal("fixture did not retain its commit/tree locators")
			}
			if fingerprintValue(t, changed, head) == expected {
				t.Fatal("fingerprint trusted SHA1 locators rather than actual bytes")
			}
		})
	}
}

func TestFingerprintBindsCompleteParentHistory(t *testing.T) {
	records, base, _ := basicRecords()
	emptyTree := testID(treeObject, nil)
	child := testCommit(emptyTree, base)
	childID := testID(commitObject, child)
	records = append(records, testRecord{kind: treeObject}, testRecord{kind: commitObject, data: child})
	data := testBundle(map[string]objectID{"refs/heads/main": childID}, testPack(t, records...))
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	expected := fingerprintValue(t, b, childID.String())
	for _, test := range []struct {
		name string
		edit func(*object)
	}{
		{"ancestor-file", func(o *object) {
			if o.kind == blobObject {
				o.data = bytes.Replace(o.data, []byte("original"), []byte("modified"), 1)
			}
		}},
		{"ancestor-metadata", func(o *object) {
			if o.id == base {
				o.data = bytes.Replace(o.data, []byte("original fixture\n"), []byte("modified fixture\n"), 1)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := fingerprintModel(t, b, test.edit)
			if !bytes.Equal(changed.objects[childID].data, b.objects[childID].data) ||
				!bytes.Equal(changed.objects[emptyTree].data, b.objects[emptyTree].data) {
				t.Fatal("fixture unexpectedly changed the child's commit or snapshot")
			}
			if fingerprintValue(t, changed, childID.String()) == expected {
				t.Fatal("parent bytes changed behind an unchanged parent locator")
			}
		})
	}
}

func TestFingerprintStableAcrossPacksAndParentBase(t *testing.T) {
	records, base, tree := basicRecords()
	retainedPack := testPackLevel(t, zlib.NoCompression, records...)
	retained, err := Open(testBundle(map[string]objectID{"refs/heads/main": base}, retainedPack))
	if err != nil {
		t.Fatal(err)
	}
	child := testCommit(tree, base)
	childID := testID(commitObject, child)
	unrelated := testRecord{kind: blobObject, data: []byte("unrelated packed content")}
	incomingRecords := append(append([]testRecord(nil), records...), unrelated, testRecord{kind: commitObject, data: child})
	incomingPack := testPackLevel(t, zlib.BestCompression, incomingRecords...)
	incomingData := testBundle(map[string]objectID{"refs/heads/main": childID}, incomingPack)
	incoming, err := Open(incomingData)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(retainedPack, incomingPack) {
		t.Fatal("fixture did not change pack representation")
	}
	baseFingerprint := fingerprintValue(t, retained, base.String())
	if fingerprintValue(t, incoming, base.String()) != baseFingerprint {
		t.Fatal("approved base/parent fingerprint changed with compression or extra objects")
	}
	childFingerprint := fingerprintValue(t, incoming, childID.String())
	if childFingerprint == baseFingerprint {
		t.Fatal("child's parent/commit metadata was not bound")
	}
	deltaRecords := []testRecord{
		{kind: refDelta, baseID: base, data: deltaLiterals(uint64(len(records[2].data)), child)},
		unrelated, records[2], records[1], records[0],
	}
	deltaPack := testPackLevel(t, zlib.BestSpeed, deltaRecords...)
	binary.BigEndian.PutUint32(deltaPack[4:8], 3)
	deltaData := testBundle(map[string]objectID{"HEAD": childID, "refs/heads/other": childID}, checksumPack(deltaPack))
	deltaData = bytes.Replace(deltaData, []byte("# v2 git bundle\n"), []byte("# v3 git bundle\n@object-format=sha1\n"), 1)
	repacked, err := Open(deltaData)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprintValue(t, repacked, base.String()) != baseFingerprint ||
		fingerprintValue(t, repacked, childID.String()) != childFingerprint {
		t.Fatal("fingerprints depend on pack version/order/deltas or advertised refs")
	}
	clear(incomingData)
	if fingerprintValue(t, incoming, childID.String()) != childFingerprint {
		t.Fatal("fingerprint retained caller-owned input")
	}
}

func TestFingerprintSharedTreeDAGIsBoundedAndCached(t *testing.T) {
	blob := []byte("shared original fixture\n")
	blobID := testID(blobObject, blob)
	tree := rawTestTree(testTreeEntry{"100644", "leaf", blobID})
	child := testID(treeObject, tree)
	records := []testRecord{{kind: blobObject, data: blob}, {kind: treeObject, data: tree}}
	for range 64 {
		tree = rawTestTree(testTreeEntry{"40000", "a", child}, testTreeEntry{"40000", "b", child})
		child = testID(treeObject, tree)
		records = append(records, testRecord{kind: treeObject, data: tree})
	}
	commit := testCommit(child)
	head := testID(commitObject, commit)
	records = append(records, testRecord{kind: commitObject, data: commit})
	data := testBundle(map[string]objectID{"refs/heads/main": head}, testPack(t, records...))
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	locator := head.String()
	value := fingerprintValue(t, b, locator)
	allocations := testing.AllocsPerRun(64, func() {
		got, err := b.Fingerprint(locator)
		if err != nil || got != value {
			t.Fatal("cached fingerprint changed")
		}
	})
	if allocations > 3 {
		t.Fatalf("fingerprint was not an O(1) cached read: %.1f allocations", allocations)
	}
	file, err := b.File(locator, strings.Repeat("a/", 64)+"leaf", int64(len(blob)))
	if err != nil || !bytes.Equal(file, blob) {
		t.Fatal("shared DAG did not retain exact file reads")
	}
	var workers sync.WaitGroup
	failures := make(chan string, 16)
	for range 16 {
		workers.Go(func() {
			for range 64 {
				if got, err := b.Fingerprint(locator); err != nil || got != value || !b.Contains(locator) {
					failures <- "concurrent fingerprint/membership read changed"
					return
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

func TestFingerprintRejectsMissingAndInvalidCommits(t *testing.T) {
	data, head, tree := basicBundle(t)
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"HEAD", "", strings.ToUpper(head), strings.Repeat("0", 40), tree.String()} {
		if fingerprint, err := b.Fingerprint(value); err == nil || fingerprint != "" {
			t.Errorf("invalid fingerprint commit %q accepted", value)
		}
	}
	var absent *Bundle
	if value, err := absent.Fingerprint(head); err == nil || value != "" {
		t.Fatal("nil bundle fingerprint was accepted")
	}
}

func TestContainsCommitMembership(t *testing.T) {
	data, head, tree := basicBundle(t)
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Contains(head) {
		t.Fatal("contained commit was not found")
	}
	for _, value := range []string{"HEAD", "", strings.ToUpper(head), strings.Repeat("0", 40), tree.String(),
		testID(blobObject, []byte("original fixture\n")).String()} {
		if b.Contains(value) {
			t.Errorf("invalid/noncommit membership %q accepted", value)
		}
	}
	var absent *Bundle
	if absent.Contains(head) {
		t.Fatal("nil bundle reported membership")
	}
	work := fmt.Sprintf("%040x", 1)
	if b.Contains(work) {
		t.Fatal("absent work commit was found in the main bundle")
	}
	if _, err := b.IsAncestor(work, head); err == nil {
		t.Fatal("ancestry silently changed its missing-commit behavior")
	}
}
