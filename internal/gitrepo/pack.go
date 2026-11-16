package gitrepo

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"strconv"
)

type packEntry struct {
	object          *object
	kind            objectType
	size            uint64
	compressedStart int
	compressedEnd   int
	baseIndex       int
	baseID          objectID
	sourceSize      uint64
	targetSize      uint64
}

type packInflater struct {
	reader io.ReadCloser
}

func (i *packInflater) reset(source *bytes.Reader) (io.ReadCloser, error) {
	var header [2]byte
	if _, err := source.ReadAt(header[:], source.Size()-int64(source.Len())); err != nil {
		return nil, fmt.Errorf("truncated zlib stream")
	}
	if header[1]&32 != 0 {
		return nil, fmt.Errorf("preset zlib dictionaries are not supported")
	}
	if i.reader == nil {
		reader, err := zlib.NewReader(source)
		if err != nil {
			return nil, err
		}
		i.reader = reader
	} else if err := i.reader.(zlib.Resetter).Reset(source, nil); err != nil {
		return nil, err
	}
	return i.reader, nil
}

func (i *packInflater) close() {
	if i.reader != nil {
		_ = i.reader.Close()
	}
}

func objectHasher(kind objectType, size uint64) hash.Hash {
	h := sha1.New()
	header := append([]byte(kind.name()), ' ')
	header = strconv.AppendUint(header, size, 10)
	header = append(header, 0)
	_, _ = h.Write(header)
	return h
}

func objectHash(kind objectType, data []byte) objectID {
	h := objectHasher(kind, uint64(len(data)))
	_, _ = h.Write(data)
	var id objectID
	copy(id[:], h.Sum(nil))
	return id
}

func packObjectHeader(r *bytes.Reader) (objectType, uint64, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, 0, fmt.Errorf("truncated pack object header")
	}
	kind := objectType((c >> 4) & 7)
	if kind.name() == "" && kind != offsetDelta && kind != refDelta {
		return 0, 0, fmt.Errorf("unsupported pack object type")
	}
	size := uint64(c & 15)
	shift := uint(4)
	for c&128 != 0 {
		c, err = r.ReadByte()
		if err != nil || shift >= 64 || (shift == 60 && c&127 > 15) {
			return 0, 0, fmt.Errorf("invalid pack object size")
		}
		size |= uint64(c&127) << shift
		shift += 7
	}
	if size > maxExpandedBytes {
		return 0, 0, fmt.Errorf("pack object exceeds expanded byte limit")
	}
	return kind, size, nil
}

func deltaOffset(r *bytes.Reader, start int) (int, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("truncated delta offset")
	}
	distance := uint64(c & 127)
	for c&128 != 0 {
		c, err = r.ReadByte()
		if err != nil || distance >= uint64(start)>>7 {
			return 0, fmt.Errorf("invalid delta offset")
		}
		distance = ((distance + 1) << 7) | uint64(c&127)
	}
	if distance == 0 || distance > uint64(start) {
		return 0, fmt.Errorf("invalid delta offset")
	}
	return start - int(distance), nil
}

func unpack(data []byte, refs map[string]string) (map[objectID]*object, []*object, error) {
	if len(data) < 32 || !bytes.Equal(data[:4], []byte("PACK")) {
		return nil, nil, fmt.Errorf("missing pack")
	}
	version := binary.BigEndian.Uint32(data[4:8])
	if version != 2 && version != 3 {
		return nil, nil, fmt.Errorf("unsupported pack version")
	}
	count := binary.BigEndian.Uint32(data[8:12])
	payload := data[:len(data)-sha1.Size]
	// Each entry needs a type byte and at least eight bytes of zlib framing/data.
	if count == 0 || count > maxObjects || uint64(count) > uint64(len(payload)-12)/9 {
		return nil, nil, fmt.Errorf("invalid or excessive pack object count")
	}
	checksum := sha1.Sum(payload)
	if !bytes.Equal(checksum[:], data[len(payload):]) {
		return nil, nil, fmt.Errorf("pack checksum mismatch")
	}
	r := bytes.NewReader(payload[12:])
	entries := make([]packEntry, 0, min(int(count), 1024))
	ordered := make([]*object, 0, min(int(count), 1024))
	objects := make(map[objectID]*object)
	offsets := make(map[int]int)
	var inflated, expanded uint64
	buffer := make([]byte, 32<<10)
	var inflater packInflater
	defer inflater.close()
	for i := uint32(0); i < count; i++ {
		start := len(payload) - r.Len()
		kind, size, err := packObjectHeader(r)
		if err != nil {
			return nil, nil, err
		}
		if size > maxExpandedBytes-inflated {
			return nil, nil, fmt.Errorf("inflated pack exceeds byte limit")
		}
		inflated += size
		e := packEntry{kind: kind, size: size, baseIndex: -1, object: &object{}}
		switch kind {
		case offsetDelta:
			base, err := deltaOffset(r, start)
			if err != nil {
				return nil, nil, err
			}
			index, exists := offsets[base]
			if !exists {
				return nil, nil, fmt.Errorf("delta offset is not an earlier object")
			}
			e.baseIndex = index
		case refDelta:
			if _, err := io.ReadFull(r, e.baseID[:]); err != nil {
				return nil, nil, fmt.Errorf("truncated delta base ID")
			}
		default:
			if size > maxExpandedBytes-expanded {
				return nil, nil, fmt.Errorf("objects exceed expanded byte limit")
			}
			expanded += size
		}
		e.compressedStart = len(payload) - r.Len()
		zr, err := inflater.reset(r)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid compressed pack object: %w", err)
		}
		if kind == offsetDelta || kind == refDelta {
			e.sourceSize, e.targetSize, _, err = deltaProgram(zr, size, nil, false)
			if err == nil {
				if e.targetSize > maxExpandedBytes-expanded {
					err = fmt.Errorf("delta objects exceed expanded byte limit")
				} else {
					expanded += e.targetSize
				}
			}
		} else {
			h := objectHasher(kind, size)
			var n int64
			n, err = io.CopyBuffer(h, &io.LimitedReader{R: zr, N: int64(size) + 1}, buffer)
			if err == nil && uint64(n) != size {
				err = fmt.Errorf("inflated object size does not match pack header")
			}
			if err == nil {
				e.object.kind = kind
				copy(e.object.id[:], h.Sum(nil))
				if _, exists := objects[e.object.id]; exists {
					err = fmt.Errorf("duplicate pack object")
				} else {
					objects[e.object.id] = e.object
				}
			}
		}
		if err != nil {
			return nil, nil, err
		}
		e.compressedEnd = len(payload) - r.Len()
		offsets[start] = len(entries)
		entries = append(entries, e)
		ordered = append(ordered, e.object)
	}
	if r.Len() != 0 {
		return nil, nil, fmt.Errorf("trailing pack data or object-count mismatch")
	}
	for _, value := range refs {
		id, _ := decodeID([]byte(value))
		o := objects[id]
		if (o != nil && o.kind != commitObject) || (o == nil && len(objects) == len(entries)) {
			return nil, nil, fmt.Errorf("reference does not identify a contained commit")
		}
	}

	// The first pass checked all compressed streams and both byte budgets.
	// Only now retain contents, without retaining any delta instruction buffers.
	pending := make([]int, 0, len(entries))
	byOffset := make(map[int][]int)
	byHash := make(map[objectID][]int)
	for i := range entries {
		e := &entries[i]
		if e.kind == offsetDelta {
			byOffset[e.baseIndex] = append(byOffset[e.baseIndex], i)
		} else if e.kind == refDelta {
			byHash[e.baseID] = append(byHash[e.baseID], i)
		} else {
			content, err := plainContents(&inflater, payload[e.compressedStart:e.compressedEnd], e.size)
			if err != nil || objectHash(e.kind, content) != e.object.id {
				return nil, nil, fmt.Errorf("pack object changed or failed to inflate")
			}
			e.object.data = content
			pending = append(pending, i)
		}
	}
	resolved := len(pending)
	for pos := 0; pos < len(pending); pos++ {
		index := pending[pos]
		base := entries[index].object
		children := byOffset[index]
		children = append(children, byHash[base.id]...)
		for _, child := range children {
			e := &entries[child]
			zr, err := inflater.reset(bytes.NewReader(payload[e.compressedStart:e.compressedEnd]))
			if err != nil {
				return nil, nil, fmt.Errorf("invalid compressed delta")
			}
			source, target, content, err := deltaProgram(zr, e.size, base.data, true)
			if err != nil || source != e.sourceSize || target != e.targetSize {
				return nil, nil, fmt.Errorf("delta base, contents or sizes are invalid")
			}
			o := e.object
			o.kind = base.kind
			o.data = content
			o.id = objectHash(o.kind, content)
			if _, exists := objects[o.id]; exists {
				return nil, nil, fmt.Errorf("duplicate pack object")
			}
			objects[o.id] = o
			pending = append(pending, child)
			resolved++
		}
	}
	if resolved != len(entries) {
		return nil, nil, fmt.Errorf("missing delta base or cyclic delta dependencies")
	}
	return objects, ordered, nil
}

func plainContents(inflater *packInflater, compressed []byte, size uint64) ([]byte, error) {
	zr, err := inflater.reset(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	result := make([]byte, int(size))
	if _, err := io.ReadFull(zr, result); err != nil {
		return nil, err
	}
	var extra [1]byte
	if n, err := zr.Read(extra[:]); n != 0 || err != io.EOF {
		return nil, fmt.Errorf("invalid compressed object length or checksum")
	}
	return result, nil
}

func deltaSize(r *bufio.Reader) (uint64, error) {
	var size uint64
	for shift := uint(0); shift < 64; shift += 7 {
		c, err := r.ReadByte()
		if err != nil || (shift == 63 && c&127 > 1) {
			return 0, fmt.Errorf("invalid delta size")
		}
		size |= uint64(c&127) << shift
		if c&128 == 0 {
			if size > maxExpandedBytes {
				return 0, fmt.Errorf("delta size exceeds expanded byte limit")
			}
			return size, nil
		}
	}
	return 0, fmt.Errorf("overflowing delta size")
}

func deltaProgram(reader io.Reader, size uint64, base []byte, apply bool) (uint64, uint64, []byte, error) {
	limited := &io.LimitedReader{R: reader, N: int64(size) + 1}
	r := bufio.NewReaderSize(limited, min(int(size)+1, 32<<10))
	source, err := deltaSize(r)
	if err != nil {
		return 0, 0, nil, err
	}
	target, err := deltaSize(r)
	if err != nil {
		return 0, 0, nil, err
	}
	var result []byte
	if apply {
		if source != uint64(len(base)) {
			return 0, 0, nil, fmt.Errorf("delta source size mismatch")
		}
		result = make([]byte, int(target))
	}
	var produced uint64
	var literal [127]byte
	for {
		command, err := r.ReadByte()
		if err == io.EOF {
			if produced != target || limited.N != 1 {
				return 0, 0, nil, fmt.Errorf("delta output or representation size mismatch")
			}
			return source, target, result, nil
		}
		if err != nil {
			return 0, 0, nil, fmt.Errorf("invalid compressed delta: %w", err)
		}
		if command == 0 {
			return 0, 0, nil, fmt.Errorf("reserved delta instruction")
		}
		if command&128 == 0 {
			length := uint64(command)
			if length > target-produced {
				return 0, 0, nil, fmt.Errorf("delta literal exceeds target")
			}
			if _, err := io.ReadFull(r, literal[:command]); err != nil {
				return 0, 0, nil, fmt.Errorf("truncated delta literal")
			}
			if apply {
				copy(result[int(produced):], literal[:command])
			}
			produced += length
			continue
		}
		var offset, length uint64
		for bit := uint(0); bit < 7; bit++ {
			if command&(1<<bit) == 0 {
				continue
			}
			value, err := r.ReadByte()
			if err != nil {
				return 0, 0, nil, fmt.Errorf("truncated delta copy")
			}
			if bit < 4 {
				offset |= uint64(value) << (8 * bit)
			} else {
				length |= uint64(value) << (8 * (bit - 4))
			}
		}
		if length == 0 {
			length = 65536
		}
		if offset > source || length > source-offset || length > target-produced {
			return 0, 0, nil, fmt.Errorf("delta copy exceeds source or target")
		}
		if apply {
			copy(result[int(produced):], base[int(offset):int(offset+length)])
		}
		produced += length
	}
}
