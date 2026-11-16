package gitrepo

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type testRecord struct {
	kind       objectType
	data       []byte
	size       uint64
	forceSize  bool
	baseID     objectID
	baseIndex  int
	offset     []byte
	header     []byte
	compressed []byte
}

func testID(kind objectType, data []byte) objectID {
	return sha1.Sum(append([]byte(fmt.Sprintf("%s %d\x00", kind.name(), len(data))), data...))
}

func encodedObjectHeader(kind objectType, size uint64) []byte {
	first := byte(kind)<<4 | byte(size&15)
	size >>= 4
	if size != 0 {
		first |= 128
	}
	result := []byte{first}
	for size != 0 {
		c := byte(size & 127)
		size >>= 7
		if size != 0 {
			c |= 128
		}
		result = append(result, c)
	}
	return result
}

func encodedOffset(distance int) []byte {
	result := []byte{byte(distance & 127)}
	for distance >>= 7; distance != 0; distance >>= 7 {
		distance--
		result = append(result, 128|byte(distance&127))
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

func testPack(t testing.TB, records ...testRecord) []byte {
	t.Helper()
	pack := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2}
	pack = binary.BigEndian.AppendUint32(pack, uint32(len(records)))
	starts := make([]int, len(records))
	for i, record := range records {
		starts[i] = len(pack)
		size := uint64(len(record.data))
		if record.forceSize {
			size = record.size
		}
		header := record.header
		if header == nil {
			header = encodedObjectHeader(record.kind, size)
		}
		pack = append(pack, header...)
		switch record.kind {
		case refDelta:
			pack = append(pack, record.baseID[:]...)
		case offsetDelta:
			offset := record.offset
			if offset == nil {
				offset = encodedOffset(starts[i] - starts[record.baseIndex])
			}
			pack = append(pack, offset...)
		}
		compressed := record.compressed
		if compressed == nil {
			var buffer bytes.Buffer
			writer := zlib.NewWriter(&buffer)
			if _, err := writer.Write(record.data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			compressed = buffer.Bytes()
		}
		pack = append(pack, compressed...)
	}
	checksum := sha1.Sum(pack)
	return append(pack, checksum[:]...)
}

func checksumPack(pack []byte) []byte {
	checksum := sha1.Sum(pack[:len(pack)-sha1.Size])
	copy(pack[len(pack)-sha1.Size:], checksum[:])
	return pack
}

func testBundle(refs map[string]objectID, pack []byte) []byte {
	var header strings.Builder
	header.WriteString("# v2 git bundle\n")
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&header, "%s %s\n", refs[name].String(), name)
	}
	header.WriteByte('\n')
	return append([]byte(header.String()), pack...)
}

type testTreeEntry struct {
	mode string
	name string
	id   objectID
}

func rawTestTree(entries ...testTreeEntry) []byte {
	var result []byte
	for _, e := range entries {
		result = append(result, []byte(e.mode+" "+e.name)...)
		result = append(result, 0)
		result = append(result, e.id[:]...)
	}
	return result
}

func sortedTestTree(entries ...testTreeEntry) []byte {
	key := func(e testTreeEntry) string {
		if e.mode == "40000" {
			return e.name + "/"
		}
		return e.name + "\x00"
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
	return rawTestTree(entries...)
}

func testCommit(tree objectID, parents ...objectID) []byte {
	var result strings.Builder
	fmt.Fprintf(&result, "tree %s\n", tree.String())
	for _, parent := range parents {
		fmt.Fprintf(&result, "parent %s\n", parent.String())
	}
	result.WriteString("author Fixture <fixture@example.invalid> 1700000000 +0000\n")
	result.WriteString("committer Fixture <fixture@example.invalid> 1700000000 +0000\n\noriginal fixture\n")
	return []byte(result.String())
}

func basicRecords() ([]testRecord, objectID, objectID) {
	blob := []byte("original fixture\n")
	blobID := testID(blobObject, blob)
	tree := sortedTestTree(testTreeEntry{"100644", "file", blobID})
	treeID := testID(treeObject, tree)
	commit := testCommit(treeID)
	id := testID(commitObject, commit)
	return []testRecord{{kind: blobObject, data: blob}, {kind: treeObject, data: tree}, {kind: commitObject, data: commit}}, id, treeID
}

func basicBundle(t testing.TB) ([]byte, string, objectID) {
	t.Helper()
	records, id, tree := basicRecords()
	return testBundle(map[string]objectID{"refs/heads/main": id}, testPack(t, records...)), id.String(), tree
}

func requireRejected(t testing.TB, data []byte, reason string) {
	t.Helper()
	bundle, err := Open(data)
	if err == nil || bundle != nil {
		t.Fatalf("malformed bundle was accepted; wanted %q", reason)
	}
	if reason != "" && !strings.Contains(err.Error(), reason) {
		t.Fatalf("refused for %q, want reason containing %q", err, reason)
	}
}

func TestBundleHeaderValidation(t *testing.T) {
	records, id, _ := basicRecords()
	pack := testPack(t, records...)
	row := id.String() + " refs/heads/main\n"
	for _, test := range []struct {
		name   string
		header string
		reason string
	}{
		{"v1", "# v1 git bundle\n" + row + "\n", "signature"},
		{"v4", "# v4 git bundle\n" + row + "\n", "signature"},
		{"crlf", "# v2 git bundle\r\n" + row + "\n", "signature"},
		{"no-refs", "# v2 git bundle\n\n", "no references"},
		{"prerequisite", "# v2 git bundle\n-" + id.String() + " required history\n" + row + "\n", "prerequisites"},
		{"v2-capability", "# v2 git bundle\n@object-format=sha1\n" + row + "\n", "capability"},
		{"sha256", "# v3 git bundle\n@object-format=sha256\n" + row + "\n", "capability"},
		{"filter", "# v3 git bundle\n@filter=blob:none\n" + row + "\n", "capability"},
		{"unknown-feature", "# v3 git bundle\n@unknown\n" + row + "\n", "capability"},
		{"duplicate-capability", "# v3 git bundle\n@object-format=sha1\n@object-format=sha1\n" + row + "\n", "capability"},
		{"late-capability", "# v3 git bundle\n" + row + "@object-format=sha1\n\n", "capability"},
		{"duplicate-ref", "# v2 git bundle\n" + row + row + "\n", "duplicate reference"},
		{"traversal-ref", "# v2 git bundle\n" + id.String() + " refs/heads/../main\n\n", "ref"},
		{"short-ref", "# v2 git bundle\n" + id.String() + " main\n\n", "ref"},
		{"uppercase-id", "# v2 git bundle\n" + strings.ToUpper(id.String()) + " refs/heads/main\n\n", "declaration"},
		{"wrong-id-length", "# v2 git bundle\n" + id.String()[:39] + " refs/heads/main\n\n", "declaration"},
		{"no-separator", "# v2 git bundle\n" + row, ""},
		{"long-line", "# v2 git bundle\n" + id.String() + " refs/heads/" + strings.Repeat("x", maxRefBytes) + "\n\n", "header line"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireRejected(t, append([]byte(test.header), pack...), test.reason)
		})
	}
	for _, capability := range []string{"", "@object-format=sha1\n"} {
		data := append([]byte("# v3 git bundle\n"+capability+row+"\n"), pack...)
		if _, err := Open(data); err != nil {
			t.Fatalf("supported v3 SHA1 bundle rejected: %v", err)
		}
	}
	conflicting := map[string]objectID{
		"refs/heads/a": id, "refs/heads/a-b": id, "refs/heads/a/c": id,
	}
	requireRejected(t, testBundle(conflicting, pack), "conflicting reference")
	refs := make(map[string]objectID)
	for i := 0; i < maxRefs; i++ {
		refs[fmt.Sprintf("refs/heads/branch-%03d", i)] = id
	}
	if _, err := Open(testBundle(refs, pack)); err != nil {
		t.Fatalf("exact reference limit was rejected: %v", err)
	}
	refs["refs/heads/excess"] = id
	requireRejected(t, testBundle(refs, pack), "too many references")
}

func TestBundlePackChecksumAndFraming(t *testing.T) {
	data, _, _ := basicBundle(t)
	for i := 0; i < len(data); i++ {
		requireRejected(t, data[:i], "")
	}
	requireRejected(t, nil, "input")
	requireRejected(t, []byte{0}, "header")
	requireRejected(t, []byte("# v2 git bundle\n"), "header")
	requireRejected(t, make([]byte, maxBundleBytes+1), "input")
	records, id, _ := basicRecords()
	for _, version := range []uint32{0, 1, 4, 0xffffffff} {
		pack := testPack(t, records...)
		binary.BigEndian.PutUint32(pack[4:8], version)
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, checksumPack(pack)), "pack version")
	}
	for _, count := range []uint32{0, maxObjects + 1, 0xffffffff, 2, 4} {
		pack := testPack(t, records...)
		binary.BigEndian.PutUint32(pack[8:12], count)
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, checksumPack(pack)), "")
	}
	pack := testPack(t, records...)
	pack[len(pack)-1] ^= 1
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, pack), "checksum")
	pack = testPack(t, records...)
	extra := append(bytes.Clone(pack[:len(pack)-sha1.Size]), 0)
	hash := sha1.Sum(extra)
	extra = append(extra, hash[:]...)
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, extra), "trailing pack")
	pack = testPack(t, records...)
	pack[0] = 'X'
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, checksumPack(pack)), "missing pack")
	for _, kind := range []objectType{0, 5} {
		pack := testPack(t, testRecord{kind: kind, data: []byte("invalid")})
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, pack), "object type")
	}
	overflow := append([]byte{byte(blobObject)<<4 | 128}, bytes.Repeat([]byte{255}, 10)...)
	pack = testPack(t, testRecord{kind: blobObject, header: overflow})
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, pack), "object size")
	for _, size := range []uint64{0, 2, maxExpandedBytes + 1} {
		pack := testPack(t, testRecord{kind: blobObject, data: []byte("x"), forceSize: true, size: size})
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, pack), "")
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write([]byte("x"))
	_ = writer.Close()
	badChecksum := bytes.Clone(compressed.Bytes())
	badChecksum[len(badChecksum)-1] ^= 1
	pack = testPack(t, testRecord{kind: blobObject, data: []byte("x"), compressed: badChecksum})
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, pack), "")
	pack = testPack(t, append(records, records[0])...)
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, pack), "duplicate pack object")
}

func TestBundleRejectsPresetZlibDictionary(t *testing.T) {
	records, head, _ := basicRecords()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(records[0].data)
	_ = writer.Close()
	original := compressed.Bytes()
	flags := (original[1] | 32) & 224
	value := uint16(original[0])<<8 | uint16(flags)
	flags += byte((31 - value%31) % 31)
	withDictionary := []byte{original[0], flags, 0, 0, 0, 1}
	withDictionary = append(withDictionary, original[2:]...)
	records[0].compressed = withDictionary
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": head}, testPack(t, records...)), "preset zlib dictionaries")
}

func TestBundleObjectIdentityAndClosure(t *testing.T) {
	records, id, tree := basicRecords()
	pack := testPack(t, records...)
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": {}}, pack), "contained commit")
	for _, wrong := range []objectID{testID(blobObject, records[0].data), tree} {
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": wrong}, pack), "contained commit")
	}
	modified := append([]testRecord(nil), records...)
	modified[2].data = append(bytes.Clone(modified[2].data), 'x')
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, testPack(t, modified...)), "contained commit")
	blobID := testID(blobObject, records[0].data)
	for _, test := range []struct {
		name   string
		commit []byte
		reason string
	}{
		{"missing-tree", testCommit(objectID{}), "missing or wrong-type tree"},
		{"blob-tree", testCommit(blobID), "missing or wrong-type tree"},
		{"missing-parent", testCommit(tree, objectID{}), "missing or wrong-type parent"},
		{"blob-parent", testCommit(tree, blobID), "missing or wrong-type parent"},
		{"duplicate-parent", testCommit(tree, id, id), "duplicate or excessive"},
		{"no-author", []byte("tree " + tree.String() + "\n\nmessage\n"), "author"},
		{"invalid-identity", []byte("tree " + tree.String() + "\nauthor invalid\ncommitter invalid\n\nmessage\n"), "identity"},
		{"no-header-end", []byte("tree " + tree.String()), "header"},
		{"nul-message", append(testCommit(tree), 0), "NUL"},
		{"oversized-header", bytes.Replace(testCommit(tree), []byte("\n\n"), []byte("\ngpgsig "+strings.Repeat("x", maxObjectHeader)+"\n\n"), 1), "header"},
		{"duplicate-tree", bytes.Replace(testCommit(tree), []byte("\n\n"), []byte("\ntree "+tree.String()+"\n\n"), 1), "structural"},
		{"bad-continuation", bytes.Replace(testCommit(tree), []byte("\n\n"), []byte("\n orphan\n\n"), 1), "continuation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := append(append([]testRecord(nil), records[:2]...), testRecord{kind: commitObject, data: test.commit})
			ref := testID(commitObject, test.commit)
			requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": ref}, testPack(t, bad...)), test.reason)
		})
	}
	parents := make([]objectID, maxParents+1)
	for i := range parents {
		binary.BigEndian.PutUint32(parents[i][16:], uint32(i))
	}
	excess := testCommit(tree, parents...)
	bad := append(append([]testRecord(nil), records[:2]...), testRecord{kind: commitObject, data: excess})
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": testID(commitObject, excess)}, testPack(t, bad...)), "excessive commit parents")
	unreachable := append(append([]testRecord(nil), records...), testRecord{kind: commitObject, data: testCommit(objectID{})})
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": id}, testPack(t, unreachable...)), "missing or wrong-type tree")
	signed := bytes.Replace(records[2].data, []byte("\n\n"), []byte("\ngpgsig -----BEGIN ORIGINAL FIXTURE-----\n data\n -----END ORIGINAL FIXTURE-----\n\n"), 1)
	signedRecords := append(append([]testRecord(nil), records[:2]...), testRecord{kind: commitObject, data: signed})
	if _, err := Open(testBundle(map[string]objectID{"refs/heads/main": testID(commitObject, signed)}, testPack(t, signedRecords...))); err != nil {
		t.Fatalf("bounded opaque signature metadata rejected: %v", err)
	}
}

func TestBundleTreeValidation(t *testing.T) {
	blob := []byte("original fixture\n")
	blobID := testID(blobObject, blob)
	emptyTreeID := testID(treeObject, nil)
	cases := []struct {
		name string
		tree []byte
	}{
		{"parent-component", rawTestTree(testTreeEntry{"100644", "..", blobID})},
		{"dot-component", rawTestTree(testTreeEntry{"100644", ".", blobID})},
		{"slash", rawTestTree(testTreeEntry{"100644", "a/b", blobID})},
		{"backslash", rawTestTree(testTreeEntry{"100644", "a\\b", blobID})},
		{"colon", rawTestTree(testTreeEntry{"100644", "C:recipe", blobID})},
		{"git-directory", rawTestTree(testTreeEntry{"100644", ".gIt", blobID})},
		{"trailing-dot", rawTestTree(testTreeEntry{"100644", "recipe.", blobID})},
		{"trailing-space", rawTestTree(testTreeEntry{"100644", "recipe ", blobID})},
		{"control", rawTestTree(testTreeEntry{"100644", "a\nb", blobID})},
		{"invalid-utf8", rawTestTree(testTreeEntry{"100644", "\xff", blobID})},
		{"format-control", rawTestTree(testTreeEntry{"100644", "a\u200eb", blobID})},
		{"long-component", rawTestTree(testTreeEntry{"100644", strings.Repeat("x", maxComponent+1), blobID})},
		{"empty-component", rawTestTree(testTreeEntry{"100644", "", blobID})},
		{"wrong-mode", rawTestTree(testTreeEntry{"100664", "file", blobID})},
		{"submodule", rawTestTree(testTreeEntry{"160000", "external", objectID{}})},
		{"missing-blob", rawTestTree(testTreeEntry{"100644", "file", objectID{}})},
		{"wrong-blob-type", rawTestTree(testTreeEntry{"100644", "file", emptyTreeID})},
		{"wrong-tree-type", rawTestTree(testTreeEntry{"40000", "dir", blobID})},
		{"truncated", []byte("100644 file\x00")},
		{"unsorted", rawTestTree(testTreeEntry{"100644", "z", blobID}, testTreeEntry{"100644", "a", blobID})},
		{"duplicate", rawTestTree(testTreeEntry{"100644", "a", blobID}, testTreeEntry{"100644", "a", blobID})},
		{"nonadjacent-duplicate", rawTestTree(testTreeEntry{"100644", "a", blobID}, testTreeEntry{"100644", "a-", blobID}, testTreeEntry{"40000", "a", emptyTreeID})},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			commit := testCommit(testID(treeObject, test.tree))
			records := []testRecord{
				{kind: blobObject, data: blob}, {kind: treeObject},
				{kind: treeObject, data: test.tree}, {kind: commitObject, data: commit},
			}
			requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": testID(commitObject, commit)}, testPack(t, records...)), "")
		})
	}
	commit := testCommit(emptyTreeID)
	empty := testBundle(map[string]objectID{"HEAD": testID(commitObject, commit)}, testPack(t,
		testRecord{kind: treeObject}, testRecord{kind: commitObject, data: commit}))
	if _, err := Open(empty); err != nil {
		t.Fatalf("a commit with an empty tree is not an empty bundle: %v", err)
	}
	for _, test := range []struct {
		name  string
		count int
		width int
	}{{"deep", maxPathDepth + 1, 1}, {"long-path", 20, maxComponent}} {
		t.Run(test.name, func(t *testing.T) {
			records := []testRecord{{kind: blobObject, data: blob}}
			child := blobID
			mode := "100644"
			for i := 0; i < test.count; i++ {
				data := rawTestTree(testTreeEntry{mode, strings.Repeat("x", test.width), child})
				child = testID(treeObject, data)
				records = append(records, testRecord{kind: treeObject, data: data})
				mode = "40000"
			}
			commit := testCommit(child)
			records = append(records, testRecord{kind: commitObject, data: commit})
			requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": testID(commitObject, commit)}, testPack(t, records...)), "path length or depth")
		})
	}
	t.Run("wide-tree", func(t *testing.T) {
		entries := make([]testTreeEntry, maxTreeEntries+1)
		for i := range entries {
			entries[i] = testTreeEntry{"100644", fmt.Sprintf("file-%06d", i), blobID}
		}
		tree := rawTestTree(entries...)
		commit := testCommit(testID(treeObject, tree))
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": testID(commitObject, commit)}, testPack(t,
			testRecord{kind: blobObject, data: blob}, testRecord{kind: treeObject, data: tree},
			testRecord{kind: commitObject, data: commit})), "too many entries")
	})
}

func TestBundleTagClosureAndCommitOnlyRefs(t *testing.T) {
	records, head, _ := basicRecords()
	tag := []byte("object " + head.String() + "\ntype commit\ntag original-fixture\ntagger Fixture <fixture@example.invalid> 1700000000 +0000\n\noriginal tag\n")
	tagID := testID(tagObject, tag)
	withTag := append(append([]testRecord(nil), records...), testRecord{kind: tagObject, data: tag})
	pack := testPack(t, withTag...)
	if _, err := Open(testBundle(map[string]objectID{"refs/heads/main": head, "refs/tags/lightweight": head}, pack)); err != nil {
		t.Fatal(err)
	}
	requireRejected(t, testBundle(map[string]objectID{"refs/tags/annotated": tagID}, pack), "contained commit")
	for _, data := range [][]byte{
		[]byte("object " + objectID{}.String() + "\ntype commit\ntag missing\n\nmessage\n"),
		[]byte("object " + head.String() + "\ntype blob\ntag wrong-type\n\nmessage\n"),
		[]byte("object " + head.String() + "\ntype unknown\ntag unknown\n\nmessage\n"),
		[]byte("object " + head.String() + "\ntype commit\ntag ../unsafe\n\nmessage\n"),
		[]byte("invalid tag"),
	} {
		bad := append(append([]testRecord(nil), records...), testRecord{kind: tagObject, data: data})
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": head}, testPack(t, bad...)), "")
	}
}

func TestBundleBoundsBeforeLargeAllocations(t *testing.T) {
	refs := map[string]objectID{"refs/heads/main": {}}
	for _, test := range []struct {
		name    string
		records []testRecord
		reason  string
	}{
		{"lying-large-object", []testRecord{{kind: blobObject, data: []byte("x"), forceSize: true, size: maxExpandedBytes}}, "size does not match"},
		{"oversized-object", []testRecord{{kind: blobObject, forceSize: true, size: maxExpandedBytes + 1}}, "byte limit"},
		{"cumulative-inflation", []testRecord{{kind: blobObject, data: []byte("x")}, {kind: blobObject, forceSize: true, size: maxExpandedBytes}}, "inflated pack"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := testBundle(refs, testPack(t, test.records...))
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			requireRejected(t, data, test.reason)
			runtime.ReadMemStats(&after)
			if used := after.TotalAlloc - before.TotalAlloc; used > 4<<20 {
				t.Fatalf("malformed size caused a large allocation: %d bytes", used)
			}
		})
	}
	t.Run("invalid-ref-before-retaining-contents", func(t *testing.T) {
		content := bytes.Repeat([]byte{'x'}, 8<<20)
		pack := testPack(t, testRecord{kind: blobObject, data: content})
		data := testBundle(map[string]objectID{"refs/heads/main": testID(blobObject, content)}, pack)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		requireRejected(t, data, "contained commit")
		runtime.ReadMemStats(&after)
		if used := after.TotalAlloc - before.TotalAlloc; used > 4<<20 {
			t.Fatalf("invalid ref caused content retention before refusal: %d bytes", used)
		}
	})
	t.Run("invalid-large-object-table", func(t *testing.T) {
		pack := make([]byte, 12+maxObjects*9+sha1.Size)
		copy(pack, []byte("PACK"))
		binary.BigEndian.PutUint32(pack[4:8], 2)
		binary.BigEndian.PutUint32(pack[8:12], maxObjects)
		data := testBundle(refs, checksumPack(pack))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		requireRejected(t, data, "object type")
		runtime.ReadMemStats(&after)
		if used := after.TotalAlloc - before.TotalAlloc; used > 4<<20 {
			t.Fatalf("unvalidated object count caused a large table allocation: %d bytes", used)
		}
	})
	t.Run("oversized-commit-argument", func(t *testing.T) {
		data, _, _ := basicBundle(t)
		b, err := Open(data)
		if err != nil {
			t.Fatal(err)
		}
		value := strings.Repeat("x", 8<<20)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if _, err := b.Tree(value); err == nil {
			t.Fatal("oversized commit argument was accepted")
		}
		runtime.ReadMemStats(&after)
		if used := after.TotalAlloc - before.TotalAlloc; used > 4<<20 {
			t.Fatalf("commit argument was copied before syntax refusal: %d bytes", used)
		}
	})
}

func TestBundleCountsEveryObjectAtExactLimit(t *testing.T) {
	tree := testID(treeObject, nil)
	commit := testCommit(tree)
	commitID := testID(commitObject, commit)
	pack := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2}
	pack = binary.BigEndian.AppendUint32(pack, maxObjects)
	var compressed bytes.Buffer
	writer, err := zlib.NewWriterLevel(&compressed, zlib.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	appendObject := func(kind objectType, body []byte) {
		compressed.Reset()
		writer.Reset(&compressed)
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		pack = append(pack, encodedObjectHeader(kind, uint64(len(body)))...)
		pack = append(pack, compressed.Bytes()...)
	}
	var body [8]byte
	for i := uint64(0); i < maxObjects-2; i++ {
		binary.BigEndian.PutUint64(body[:], i)
		appendObject(blobObject, body[:])
	}
	appendObject(treeObject, nil)
	appendObject(commitObject, commit)
	hash := sha1.Sum(pack)
	pack = append(pack, hash[:]...)
	data := testBundle(map[string]objectID{"refs/heads/main": commitID}, pack)
	b, err := Open(data)
	if err != nil || len(b.objects) != maxObjects {
		t.Fatalf("exact full-pack object count rejected: %v", err)
	}
	binary.BigEndian.PutUint32(pack[8:12], maxObjects+1)
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": commitID}, checksumPack(pack)), "object count")
}

func TestGraphCycleRefusal(t *testing.T) {
	a := &object{kind: treeObject, id: objectID{1}}
	b := &object{kind: treeObject, id: objectID{2}}
	a.data = rawTestTree(testTreeEntry{"40000", "a", b.id})
	b.data = rawTestTree(testTreeEntry{"40000", "b", a.id})
	bundle := &Bundle{objects: map[objectID]*object{a.id: a, b.id: b}}
	if err := bundle.validateObjects([]*object{a, b}); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic tree graph was accepted: %v", err)
	}
}

func FuzzOpen(f *testing.F) {
	data, _, _ := basicBundle(f)
	f.Add(data)
	base := []byte("original fixture")
	delta, _ := blobDeltaBundle(f, append(bytes.Clone(base), []byte(" delta")...),
		testRecord{kind: blobObject, data: base},
		testRecord{kind: offsetDelta, baseIndex: 0, data: deltaAppend(base, []byte(" delta"))})
	f.Add(delta)
	f.Add([]byte("# v3 git bundle\n@object-format=sha256\n\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		// Repair the outer checksum to let mutations reach the object parser.
		if len(input) <= maxBundleBytes {
			if _, start, err := bundleHeader(input); err == nil && len(input)-start >= 32 {
				input = bytes.Clone(input)
				pack := input[start:]
				checksumPack(pack)
			}
		}
		b, err := Open(input)
		if err != nil {
			if b != nil {
				t.Fatal("failed validation returned a bundle")
			}
			return
		}
		for _, commit := range b.Refs() {
			if !Commit(commit) {
				t.Fatal("validated ref is not a commit ID")
			}
			if _, err := b.Tree(commit); err != nil {
				t.Fatal(err)
			}
			if yes, err := b.IsAncestor(commit, commit); err != nil || !yes {
				t.Fatal("validated commit is not its own ancestor")
			}
			if _, err := b.File(commit, "../unsafe", 1); err == nil {
				t.Fatal("traversal read was accepted")
			}
		}
	})
}
