package filestore

import (
	"bytes"
	"encoding/binary"

	"github.com/thtpark/fdn/internal/domain"
)

const (
	posixACLVersion = 2
	aclUserObject   = 0x01
	aclUser         = 0x02
	aclGroupObject  = 0x04
	aclMask         = 0x10
	aclOther        = 0x20
	aclUndefinedID  = ^uint32(0)
)

// Linux's xattr ABI uses little endian, independently of the host architecture.
func encodeAccessACL(uids []uint32) ([]byte, error) {
	readers, err := readerUIDs(uids)
	if err != nil {
		return nil, err
	}
	count := 3
	if len(readers) != 0 {
		count += len(readers) + 1
	}
	data := make([]byte, 4, 4+count*8)
	binary.LittleEndian.PutUint32(data, posixACLVersion)
	appendEntry := func(tag, permissions uint16, uid uint32) {
		data = binary.LittleEndian.AppendUint16(data, tag)
		data = binary.LittleEndian.AppendUint16(data, permissions)
		data = binary.LittleEndian.AppendUint32(data, uid)
	}
	appendEntry(aclUserObject, 6, aclUndefinedID)
	for _, uid := range readers {
		appendEntry(aclUser, 4, uid)
	}
	appendEntry(aclGroupObject, 0, aclUndefinedID)
	if len(readers) != 0 {
		appendEntry(aclMask, 4, aclUndefinedID)
	}
	appendEntry(aclOther, 0, aclUndefinedID)
	return data, nil
}

func decodeAccessACL(data []byte) ([]uint32, error) {
	if len(data) < 28 || len(data) > 65536 || (len(data)-4)%8 != 0 ||
		binary.LittleEndian.Uint32(data) != posixACLVersion {
		return nil, domain.Fail(domain.Denied, "file has an invalid native POSIX ACL")
	}
	var readers []uint32
	for offset := 12; offset+8 <= len(data) && binary.LittleEndian.Uint16(data[offset:]) == aclUser; offset += 8 {
		readers = append(readers, binary.LittleEndian.Uint32(data[offset+4:]))
	}
	expected, err := encodeAccessACL(readers)
	if err != nil || !bytes.Equal(data, expected) {
		return nil, domain.Fail(domain.Denied, "file ACL must grant only owner read/write and exact named user reads")
	}
	return readers, nil
}
