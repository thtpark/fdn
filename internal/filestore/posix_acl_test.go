package filestore

import (
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestExactPOSIXACLPlans(t *testing.T) {
	for _, test := range []struct {
		name string
		uids []uint32
		hex  string
	}{
		{
			"revoke-all", nil,
			"02000000 01000600ffffffff 04000000ffffffff 20000000ffffffff",
		},
		{
			"sorted-deduplicated-readers", []uint32{42, 7, 42},
			"02000000 01000600ffffffff 0200040007000000 020004002a000000 04000000ffffffff 10000400ffffffff 20000000ffffffff",
		},
		{
			"root-only-when-explicit", []uint32{0},
			"02000000 01000600ffffffff 0200040000000000 04000000ffffffff 10000400ffffffff 20000000ffffffff",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := slices.Clone(test.uids)
			data, err := encodeAccessACL(input)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(data) != strings.ReplaceAll(test.hex, " ", "") {
				t.Fatalf("ACL encoding = %x", data)
			}
			actual, err := decodeAccessACL(data)
			expected, normalizeErr := readerUIDs(test.uids)
			if err != nil || normalizeErr != nil || !slices.Equal(actual, expected) || !slices.Equal(input, test.uids) {
				t.Fatal(actual, expected, input, err, normalizeErr)
			}
		})
	}
}

func TestPOSIXACLRejectsBroaderOrMalformedGrants(t *testing.T) {
	valid, err := encodeAccessACL([]uint32{7, 42})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"owner-execute", func(data []byte) []byte { data[6] = 7; return data }},
		{"named-write", func(data []byte) []byte { data[14] = 6; return data }},
		{"named-execute", func(data []byte) []byte { data[14] = 5; return data }},
		{"group-read", func(data []byte) []byte { data[30] = 4; return data }},
		{"mask-write", func(data []byte) []byte { data[38] = 6; return data }},
		{"other-read", func(data []byte) []byte { data[46] = 4; return data }},
		{"named-group", func(data []byte) []byte { data[12] = 8; return data }},
		{"undefined-uid", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[16:], aclUndefinedID); return data }},
		{"duplicate-uid", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[24:], 7); return data }},
		{"unsorted-uids", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[16:], 100); return data }},
		{"future-version", func(data []byte) []byte { data[0] = 3; return data }},
		{"truncated", func(data []byte) []byte { return data[:len(data)-1] }},
		{"extra-entry", func(data []byte) []byte { return append(data, make([]byte, 8)...) }},
		{"empty", func([]byte) []byte { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeAccessACL(test.change(slices.Clone(valid)))
			requireCode(t, err, domain.Denied)
		})
	}
}

func TestReaderUIDBoundsAndAdapter(t *testing.T) {
	if _, err := readerUIDs(make([]uint32, maxReaders+1)); err == nil {
		t.Fatal("oversized ACL input accepted")
	}
	if _, err := encodeAccessACL([]uint32{aclUndefinedID}); err == nil {
		t.Fatal("undefined native UID accepted")
	}
	var adapter PermissionFunc
	requireCode(t, adapter.Apply(t.Context(), "", nil), domain.Prerequisite)
}
