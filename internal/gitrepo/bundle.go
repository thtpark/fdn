package gitrepo

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxBundleBytes   = 64 << 20
	maxExpandedBytes = 256 << 20
	maxObjects       = 65536
	maxRefs          = 128
	maxRefBytes      = 1024
	maxComponent     = 255
	maxPathBytes     = 4096
	maxPathDepth     = 128
	maxObjectHeader  = 64 << 10
	maxParents       = 1024
	maxTreeEntries   = 65536
)

type objectID [20]byte

type objectType byte

const (
	commitObject objectType = 1
	treeObject   objectType = 2
	blobObject   objectType = 3
	tagObject    objectType = 4
	offsetDelta  objectType = 6
	refDelta     objectType = 7
)

func (t objectType) name() string {
	switch t {
	case commitObject:
		return "commit"
	case treeObject:
		return "tree"
	case blobObject:
		return "blob"
	case tagObject:
		return "tag"
	default:
		return ""
	}
}

type object struct {
	kind         objectType
	id           objectID
	data         []byte
	tree         objectID
	parentsStart int
	parentsEnd   int
	target       objectID
	targetType   objectType
	treeEntries  int
	fingerprint  [32]byte
	state        byte
	pathBytes    int
	pathDepth    int
}

// Bundle is a validated, immutable in-memory object graph. Its methods are safe
// for concurrent reads and never return aliases of its retained contents.
type Bundle struct {
	refs    map[string]string
	objects map[objectID]*object
}

// Open validates a complete bundle, including every pack entry, object hash,
// reference, history edge and tree edge. It retains no alias of data.
func Open(data []byte) (*Bundle, error) {
	if len(data) == 0 || len(data) > maxBundleBytes {
		return nil, fmt.Errorf("git bundle: input must be nonempty and at most %d bytes", maxBundleBytes)
	}
	refs, start, err := bundleHeader(data)
	if err != nil {
		return nil, fmt.Errorf("git bundle: %w", err)
	}
	objects, ordered, err := unpack(data[start:], refs)
	if err != nil {
		return nil, fmt.Errorf("git bundle: %w", err)
	}
	b := &Bundle{refs: refs, objects: objects}
	for _, value := range refs {
		id, _ := decodeID([]byte(value))
		if o := objects[id]; o == nil || o.kind != commitObject {
			return nil, fmt.Errorf("git bundle: reference does not identify a contained commit")
		}
	}
	if err := b.validateObjects(ordered); err != nil {
		return nil, fmt.Errorf("git bundle: %w", err)
	}
	return b, nil
}

// Commit reports whether value is exactly 40 lowercase hexadecimal characters.
// It checks syntax, not object existence or authority.
func Commit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for i := range value {
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f')) {
			return false
		}
	}
	return true
}

// ValidateRef accepts safe, fully qualified refs/... names, or the HEAD bundle
// pseudo-reference. Names are bounded and never cleaned or normalized.
func ValidateRef(name string) error {
	if name == "HEAD" {
		return nil
	}
	if len(name) > maxRefBytes || !strings.HasPrefix(name, "refs/") || !utf8.ValidString(name) {
		return fmt.Errorf("git ref: expected a bounded, fully qualified refs/... name")
	}
	if strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\") {
		return fmt.Errorf("git ref: forbidden character or sequence")
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxPathDepth {
		return fmt.Errorf("git ref: excessive depth")
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxComponent || part[0] == '.' ||
			strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") ||
			strings.Contains(part, "..") {
			return fmt.Errorf("git ref: invalid component")
		}
		for _, r := range part {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return fmt.Errorf("git ref: control characters are forbidden")
			}
		}
	}
	return nil
}

func bundleHeader(data []byte) (map[string]string, int, error) {
	pos := 0
	line := func() ([]byte, error) {
		end := min(len(data), pos+maxRefBytes+42)
		n := bytes.IndexByte(data[pos:end], '\n')
		if n < 0 {
			return nil, fmt.Errorf("missing or oversized header line")
		}
		result := data[pos : pos+n]
		pos += n + 1
		return result, nil
	}
	signature, err := line()
	if err != nil {
		return nil, 0, err
	}
	version3 := bytes.Equal(signature, []byte("# v3 git bundle"))
	if !version3 && !bytes.Equal(signature, []byte("# v2 git bundle")) {
		return nil, 0, fmt.Errorf("unsupported bundle signature")
	}
	refs := make(map[string]string)
	capability := false
	for {
		current, err := line()
		if err != nil {
			return nil, 0, err
		}
		if len(current) == 0 {
			break
		}
		switch current[0] {
		case '-':
			return nil, 0, fmt.Errorf("prerequisites are not self-contained")
		case '@':
			if !version3 || capability || len(refs) != 0 || !bytes.Equal(current, []byte("@object-format=sha1")) {
				return nil, 0, fmt.Errorf("unsupported or misplaced bundle capability")
			}
			capability = true
			continue
		}
		if len(refs) == maxRefs {
			return nil, 0, fmt.Errorf("too many references")
		}
		if len(current) < 42 || current[40] != ' ' || !Commit(string(current[:40])) {
			return nil, 0, fmt.Errorf("invalid reference declaration")
		}
		name := string(current[41:])
		if err := ValidateRef(name); err != nil {
			return nil, 0, err
		}
		if _, exists := refs[name]; exists {
			return nil, 0, fmt.Errorf("duplicate reference")
		}
		refs[name] = string(current[:40])
	}
	if len(refs) == 0 {
		return nil, 0, fmt.Errorf("bundle has no references")
	}
	for name := range refs {
		for i := range name {
			if name[i] == '/' {
				if _, exists := refs[name[:i]]; exists {
					return nil, 0, fmt.Errorf("conflicting reference namespaces")
				}
			}
		}
	}
	return refs, pos, nil
}

func decodeID(value []byte) (objectID, error) {
	var result objectID
	if len(value) != 40 {
		return result, fmt.Errorf("invalid object ID")
	}
	for i, c := range value {
		var n byte
		switch {
		case c >= '0' && c <= '9':
			n = c - '0'
		case c >= 'a' && c <= 'f':
			n = c - 'a' + 10
		default:
			return result, fmt.Errorf("invalid object ID")
		}
		if i%2 == 0 {
			result[i/2] = n << 4
		} else {
			result[i/2] |= n
		}
	}
	return result, nil
}

func (id objectID) String() string {
	return hex.EncodeToString(id[:])
}

// Refs returns a copy of the advertised names and commit IDs, including HEAD
// when present. Advertised metadata grants no authority to update those refs.
func (b *Bundle) Refs() map[string]string {
	if b == nil {
		return nil
	}
	result := make(map[string]string, len(b.refs))
	for name, value := range b.refs {
		result[name] = value
	}
	return result
}

// Contains reports whether commit is a lowercase SHA-1 locator identifying a
// contained commit. It does not resolve ref names; membership alone does not
// establish ancestry, integration or authority.
func (b *Bundle) Contains(commit string) bool {
	if b == nil || !Commit(commit) {
		return false
	}
	id, _ := decodeID([]byte(commit))
	o := b.objects[id]
	return o != nil && o.kind == commitObject
}

func (b *Bundle) commit(value string) (*object, error) {
	if b == nil {
		return nil, fmt.Errorf("git bundle: no bundle")
	}
	if !Commit(value) {
		return nil, fmt.Errorf("invalid object ID")
	}
	id, _ := decodeID([]byte(value))
	o := b.objects[id]
	if o == nil || o.kind != commitObject {
		return nil, fmt.Errorf("git bundle: commit is not contained")
	}
	return o, nil
}

// Tree returns the exact root tree object ID of a contained commit.
func (b *Bundle) Tree(commit string) (string, error) {
	o, err := b.commit(commit)
	if err != nil {
		return "", err
	}
	return o.tree.String(), nil
}

// Parents returns the contained commit's direct parent IDs in their Git order.
func (b *Bundle) Parents(commit string) ([]string, error) {
	o, err := b.commit(commit)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, (o.parentsEnd-o.parentsStart)/48)
	for pos := o.parentsStart; pos < o.parentsEnd; pos += 48 {
		id, _ := decodeID(o.data[pos+7 : pos+47])
		result = append(result, id.String())
	}
	return result, nil
}

// IsAncestor reports whether ancestor is commit itself or is reachable through
// its parents. Both arguments must identify contained commits.
func (b *Bundle) IsAncestor(ancestor, commit string) (bool, error) {
	a, err := b.commit(ancestor)
	if err != nil {
		return false, err
	}
	c, err := b.commit(commit)
	if err != nil {
		return false, err
	}
	seen := map[objectID]bool{c.id: true}
	pending := []*object{c}
	for len(pending) > 0 {
		o := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if o == a {
			return true, nil
		}
		for pos := o.parentsStart; pos < o.parentsEnd; pos += 48 {
			id, _ := decodeID(o.data[pos+7 : pos+47])
			if !seen[id] {
				seen[id] = true
				pending = append(pending, b.objects[id])
			}
		}
	}
	return false, nil
}

func validComponent(name []byte) error {
	if len(name) == 0 || len(name) > maxComponent || !utf8.Valid(name) ||
		bytes.Equal(name, []byte(".")) || bytes.Equal(name, []byte("..")) ||
		name[len(name)-1] == '.' || name[len(name)-1] == ' ' ||
		bytes.ContainsAny(name, "/\\:") || strings.EqualFold(string(name), ".git") {
		return fmt.Errorf("unsafe tree/path component")
	}
	for len(name) > 0 {
		r, n := utf8.DecodeRune(name)
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("control characters in tree/path component")
		}
		name = name[n:]
	}
	return nil
}

// File returns a copy of the exact regular-file bytes at a clean repository-
// relative path. It never follows symlinks, reads gitlinks or truncates contents;
// files larger than limit fail. A zero limit permits only an empty file.
func (b *Bundle) File(commit, path string, limit int64) ([]byte, error) {
	o, err := b.commit(commit)
	if err != nil {
		return nil, err
	}
	if limit < 0 || len(path) == 0 || len(path) > maxPathBytes {
		return nil, fmt.Errorf("git bundle: invalid path or file limit")
	}
	parts := strings.Split(path, "/")
	if len(parts) > maxPathDepth {
		return nil, fmt.Errorf("git bundle: excessive path depth")
	}
	for _, part := range parts {
		if err := validComponent([]byte(part)); err != nil {
			return nil, err
		}
	}
	tree := b.objects[o.tree]
	for i, part := range parts {
		var found *treeEntry
		for pos := 0; pos < len(tree.data); {
			entry, next, err := nextTreeEntry(tree.data, pos)
			if err != nil {
				return nil, err
			}
			pos = next
			if string(entry.name) == part {
				found = &entry
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("git bundle: file path is not contained")
		}
		target := b.objects[found.id]
		if i+1 < len(parts) {
			if found.mode != directoryMode {
				return nil, fmt.Errorf("git bundle: intermediate path is not a directory")
			}
			tree = target
			continue
		}
		if found.mode != regularMode && found.mode != executableMode {
			return nil, fmt.Errorf("git bundle: path is not a regular file")
		}
		if int64(len(target.data)) > limit {
			return nil, fmt.Errorf("git bundle: file exceeds read limit")
		}
		return bytes.Clone(target.data), nil
	}
	return nil, fmt.Errorf("git bundle: invalid file path")
}
