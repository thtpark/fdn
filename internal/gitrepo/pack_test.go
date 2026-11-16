package gitrepo

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"strings"
	"testing"
)

func encodedDeltaSize(size uint64) []byte {
	var result []byte
	for {
		c := byte(size & 127)
		size >>= 7
		if size != 0 {
			c |= 128
		}
		result = append(result, c)
		if size == 0 {
			return result
		}
	}
}

func copyInstruction(offset, length uint32) []byte {
	command := byte(128)
	var operands []byte
	for i := uint(0); i < 4; i++ {
		value := byte(offset >> (8 * i))
		if value != 0 {
			command |= 1 << i
			operands = append(operands, value)
		}
	}
	if length != 65536 {
		for i := uint(0); i < 3; i++ {
			value := byte(length >> (8 * i))
			if value != 0 {
				command |= 1 << (i + 4)
				operands = append(operands, value)
			}
		}
	}
	return append([]byte{command}, operands...)
}

func deltaLiterals(source uint64, content []byte) []byte {
	result := append(encodedDeltaSize(source), encodedDeltaSize(uint64(len(content)))...)
	for len(content) > 0 {
		n := min(len(content), 127)
		result = append(result, byte(n))
		result = append(result, content[:n]...)
		content = content[n:]
	}
	return result
}

func deltaAppend(base, suffix []byte) []byte {
	result := append(encodedDeltaSize(uint64(len(base))), encodedDeltaSize(uint64(len(base)+len(suffix)))...)
	if len(base) > 0 {
		result = append(result, copyInstruction(0, uint32(len(base)))...)
	}
	for len(suffix) > 0 {
		n := min(len(suffix), 127)
		result = append(result, byte(n))
		result = append(result, suffix[:n]...)
		suffix = suffix[n:]
	}
	return result
}

func blobDeltaBundle(t testing.TB, content []byte, records ...testRecord) ([]byte, string) {
	t.Helper()
	tree := rawTestTree(testTreeEntry{"100644", "file", testID(blobObject, content)})
	commit := testCommit(testID(treeObject, tree))
	id := testID(commitObject, commit)
	records = append(records, testRecord{kind: treeObject, data: tree}, testRecord{kind: commitObject, data: commit})
	return testBundle(map[string]objectID{"refs/heads/main": id}, testPack(t, records...)), id.String()
}

func TestBundleOffsetAndForwardReferenceDeltas(t *testing.T) {
	base := make([]byte, 512)
	for i := range base {
		base[i] = byte(i)
	}
	middle := append(bytes.Clone(base), []byte(" middle")...)
	target := append(bytes.Clone(middle), []byte(" target")...)
	baseID := testID(blobObject, base)
	middleID := testID(blobObject, middle)
	for _, test := range []struct {
		name    string
		records []testRecord
	}{
		{"offset-chain", []testRecord{
			{kind: blobObject, data: base},
			{kind: offsetDelta, baseIndex: 0, data: deltaAppend(base, []byte(" middle"))},
			{kind: offsetDelta, baseIndex: 1, data: deltaAppend(middle, []byte(" target"))},
		}},
		{"forward-reference-chain", []testRecord{
			{kind: refDelta, baseID: middleID, data: deltaAppend(middle, []byte(" target"))},
			{kind: refDelta, baseID: baseID, data: deltaAppend(base, []byte(" middle"))},
			{kind: blobObject, data: base},
		}},
		{"mixed-chain", []testRecord{
			{kind: blobObject, data: base},
			{kind: refDelta, baseID: baseID, data: deltaAppend(base, []byte(" middle"))},
			{kind: offsetDelta, baseIndex: 1, data: deltaAppend(middle, []byte(" target"))},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, head := blobDeltaBundle(t, target, test.records...)
			b, err := Open(data)
			if err != nil {
				t.Fatal(err)
			}
			file, err := b.File(head, "file", int64(len(target)))
			if err != nil || !bytes.Equal(file, target) {
				t.Fatalf("delta file = %q, %v", file, err)
			}
		})
	}
}

func TestBundleDeltasPreserveObjectTypesAndEmptyBases(t *testing.T) {
	blob := []byte("original fixture")
	tree := rawTestTree(testTreeEntry{"100644", "file", testID(blobObject, blob)})
	treeID := testID(treeObject, tree)
	root := testCommit(treeID)
	rootID := testID(commitObject, root)
	child := testCommit(treeID, rootID)
	childID := testID(commitObject, child)
	records := []testRecord{
		{kind: treeObject},
		{kind: refDelta, baseID: testID(treeObject, nil), data: deltaLiterals(0, tree)},
		{kind: blobObject},
		{kind: refDelta, baseID: testID(blobObject, nil), data: deltaLiterals(0, blob)},
		{kind: commitObject, data: root},
		{kind: refDelta, baseID: rootID, data: deltaLiterals(uint64(len(root)), child)},
	}
	data := testBundle(map[string]objectID{"refs/heads/main": childID}, testPack(t, records...))
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	file, err := b.File(childID.String(), "file", int64(len(blob)))
	if err != nil || !bytes.Equal(file, blob) {
		t.Fatal("tree/blob delta did not preserve exact bytes")
	}
	parents, err := b.Parents(childID.String())
	if err != nil || len(parents) != 1 || parents[0] != rootID.String() {
		t.Fatal("commit delta did not preserve history")
	}
}

func TestBundleMalformedDeltaRefusal(t *testing.T) {
	base := []byte("base")
	baseID := testID(blobObject, base)
	headers := func(source, target uint64, commands ...byte) []byte {
		return append(append(encodedDeltaSize(source), encodedDeltaSize(target)...), commands...)
	}
	for _, test := range []struct {
		name    string
		program []byte
	}{
		{"reserved-command", headers(4, 0, 0)},
		{"copy-offset-overrun", append(headers(4, 1), copyInstruction(4, 1)...)},
		{"copy-length-overrun", append(headers(4, 5), copyInstruction(0, 5)...)},
		{"copy-target-overrun", append(headers(4, 1), copyInstruction(0, 2)...)},
		{"literal-target-overrun", headers(4, 1, 2, 'x', 'y')},
		{"truncated-literal", headers(4, 2, 2, 'x')},
		{"truncated-copy", headers(4, 1, 0x81)},
		{"incomplete-output", headers(4, 2, 1, 'x')},
		{"extra-output", headers(4, 1, 1, 'x', 1, 'y')},
		{"source-size-mismatch", deltaLiterals(5, []byte("target"))},
		{"oversized-source", headers(maxExpandedBytes+1, 0)},
		{"oversized-target", headers(4, maxExpandedBytes+1)},
		{"overflowing-size", append(bytes.Repeat([]byte{255}, 10), 0)},
		{"default-copy-overrun", headers(4, 65536, 0x80)},
		{"empty-program", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			pack := testPack(t,
				testRecord{kind: blobObject, data: base},
				testRecord{kind: refDelta, baseID: baseID, data: test.program})
			requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": {}}, pack), "delta")
		})
	}
	valid := deltaAppend(base, []byte(" target"))
	for _, offset := range [][]byte{{0}, encodedOffset(1), encodedOffset(1 << 20), bytes.Repeat([]byte{255}, 12)} {
		pack := testPack(t,
			testRecord{kind: blobObject, data: base},
			testRecord{kind: offsetDelta, offset: offset, data: valid})
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": {}}, pack), "delta offset")
	}
	for _, size := range []uint64{1, uint64(len(valid) - 1), uint64(len(valid) + 1)} {
		pack := testPack(t,
			testRecord{kind: blobObject, data: base},
			testRecord{kind: refDelta, baseID: baseID, data: valid, forceSize: true, size: size})
		requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": {}}, pack), "delta")
	}
	missing := testPack(t, testRecord{kind: refDelta, baseID: objectID{9}, data: valid})
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": {}}, missing), "missing delta base")
	duplicate := testPack(t,
		testRecord{kind: blobObject, data: base},
		testRecord{kind: refDelta, baseID: baseID, data: deltaAppend(base, nil)})
	requireRejected(t, testBundle(map[string]objectID{"refs/heads/main": {}}, duplicate), "duplicate pack object")
}

func TestBundleCumulativeReconstructedBytesBeforeAllocation(t *testing.T) {
	base := bytes.Repeat([]byte{'x'}, 65536)
	program := append(encodedDeltaSize(uint64(len(base))), encodedDeltaSize(maxExpandedBytes/2)...)
	program = append(program, bytes.Repeat([]byte{0x80}, maxExpandedBytes/2/65536)...)
	pack := testPack(t,
		testRecord{kind: blobObject, data: base},
		testRecord{kind: refDelta, baseID: testID(blobObject, base), data: program},
		testRecord{kind: refDelta, baseID: testID(blobObject, base), data: program})
	data := testBundle(map[string]objectID{"refs/heads/main": {}}, pack)
	if len(data) > 4096 {
		t.Fatalf("fixture is not a compact expansion attack: %d", len(data))
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	requireRejected(t, data, "delta objects exceed expanded byte limit")
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 4<<20 {
		t.Fatalf("delta size limits were enforced after allocation: %d bytes", used)
	}
}

func TestBundleLongForwardDeltaChain(t *testing.T) {
	base := []byte("base")
	contents := make([][]byte, 1024)
	records := make([]testRecord, 0, len(contents)+1)
	prior := base
	for i := range contents {
		contents[i] = append(bytes.Clone(prior), byte(i))
		prior = contents[i]
	}
	for i := len(contents) - 1; i >= 0; i-- {
		prior := base
		if i > 0 {
			prior = contents[i-1]
		}
		records = append(records, testRecord{
			kind: refDelta, baseID: testID(blobObject, prior), data: deltaAppend(prior, contents[i][len(prior):]),
		})
	}
	records = append(records, testRecord{kind: blobObject, data: base})
	final := contents[len(contents)-1]
	data, head := blobDeltaBundle(t, final, records...)
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.File(head, "file", int64(len(final)))
	if err != nil || !bytes.Equal(got, final) {
		t.Fatal("forward delta chain was not resolved in dependency order")
	}
}

func TestDeltaOffsetEncodingBoundaries(t *testing.T) {
	for _, distance := range []int{1, 127, 128, 255, 256, 16383, 16384, 1 << 20, 64 << 20} {
		reader := bytes.NewReader(encodedOffset(distance))
		start := distance + 12
		got, err := deltaOffset(reader, start)
		if err != nil || got != 12 || reader.Len() != 0 {
			t.Fatalf("offset %d = %d, %v", distance, got, err)
		}
	}
	for _, size := range []uint64{0, 15, 16, 127, 128, 65536, maxExpandedBytes} {
		kind, got, err := packObjectHeader(bytes.NewReader(encodedObjectHeader(blobObject, size)))
		if err != nil || kind != blobObject || got != size {
			t.Fatalf("object size %d = %d, %v", size, got, err)
		}
	}
}

func TestBundlePackVersionThree(t *testing.T) {
	records, head, _ := basicRecords()
	pack := testPack(t, records...)
	binary.BigEndian.PutUint32(pack[4:8], 3)
	data := testBundle(map[string]objectID{"refs/heads/main": head}, checksumPack(pack))
	data = bytes.Replace(data, []byte("# v2 git bundle\n"), []byte("# v3 git bundle\n@object-format=sha1\n"), 1)
	if _, err := Open(data); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bytes.Replace(data, []byte("@object-format=sha1"), []byte("@object-format=sha1-extra"), 1)); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatal("partial capability match was accepted")
	}
}
