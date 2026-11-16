package gitrepo

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
)

const fingerprintDomain = "foundation/gitrepo/fingerprint/v1\x00"

// Fingerprint returns a lowercase SHA-256 Merkle binding of the actual commit
// bytes, its complete tree contents and its full parent history. Modes, path
// components, regular-file and symlink bytes, and all raw commit metadata are
// bound, not merely their SHA-1 locators. Parent order is preserved.
//
// Each object is hashed once during Open's bounded DAG validation. Shared trees
// are never expanded into all logical paths, and subsequent concurrent calls
// read an immutable cached digest. Pack encoding, ref names and unrelated
// objects do not affect the result. This integrity binding grants no authority.
func (b *Bundle) Fingerprint(commit string) (string, error) {
	o, err := b.commit(commit)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(o.fingerprint[:]), nil
}

func (b *Bundle) fingerprintObject(o *object) error {
	var children uint64
	switch o.kind {
	case commitObject:
		children = 1 + uint64((o.parentsEnd-o.parentsStart)/48)
	case treeObject:
		children = uint64(o.treeEntries)
	case tagObject:
		children = 1
	}
	// The v1 encoding is domain, kind, raw length/body, child count, then fixed-
	// size child digests in tree-entry or tree-then-parent order.
	h := sha256.New()
	_, _ = io.WriteString(h, fingerprintDomain)
	_, _ = h.Write([]byte{byte(o.kind)})
	var field [8]byte
	binary.BigEndian.PutUint64(field[:], uint64(len(o.data)))
	_, _ = h.Write(field[:])
	_, _ = h.Write(o.data)
	binary.BigEndian.PutUint64(field[:], children)
	_, _ = h.Write(field[:])
	f := frame(o)
	for {
		child, err := b.nextChild(&f)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		_, _ = h.Write(child.fingerprint[:])
	}
	copy(o.fingerprint[:], h.Sum(nil))
	return nil
}
