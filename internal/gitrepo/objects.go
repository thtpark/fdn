package gitrepo

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
)

type treeMode uint32

const (
	directoryMode  treeMode = 040000
	regularMode    treeMode = 0100644
	executableMode treeMode = 0100755
	symlinkMode    treeMode = 0120000
	gitlinkMode    treeMode = 0160000
)

type treeEntry struct {
	name []byte
	mode treeMode
	id   objectID
}

func nextTreeEntry(data []byte, pos int) (treeEntry, int, error) {
	var e treeEntry
	if pos == len(data) {
		return e, pos, io.EOF
	}
	n := bytes.IndexByte(data[pos:min(len(data), pos+7)], ' ')
	if n < 0 {
		return e, pos, fmt.Errorf("invalid tree file mode")
	}
	switch string(data[pos : pos+n]) {
	case "40000":
		e.mode = directoryMode
	case "100644":
		e.mode = regularMode
	case "100755":
		e.mode = executableMode
	case "120000":
		e.mode = symlinkMode
	case "160000":
		e.mode = gitlinkMode
	default:
		return e, pos, fmt.Errorf("unsupported tree file mode")
	}
	pos += n + 1
	n = bytes.IndexByte(data[pos:min(len(data), pos+maxComponent+1)], 0)
	if n < 0 || len(data)-pos-n-1 < len(e.id) {
		return e, pos, fmt.Errorf("truncated or oversized tree entry")
	}
	e.name = data[pos : pos+n]
	pos += n + 1
	copy(e.id[:], data[pos:pos+len(e.id)])
	return e, pos + len(e.id), nil
}

func treeOrder(a, b treeEntry) int {
	n := min(len(a.name), len(b.name))
	if result := bytes.Compare(a.name[:n], b.name[:n]); result != 0 {
		return result
	}
	var nextA, nextB byte
	if len(a.name) > n {
		nextA = a.name[n]
	} else if a.mode == directoryMode {
		nextA = '/'
	}
	if len(b.name) > n {
		nextB = b.name[n]
	} else if b.mode == directoryMode {
		nextB = '/'
	}
	return int(nextA) - int(nextB)
}

func (b *Bundle) validateTree(o *object) error {
	names := make(map[string]struct{})
	var previous treeEntry
	for pos, count := 0, 0; pos < len(o.data); count++ {
		if count == maxTreeEntries {
			return fmt.Errorf("tree has too many entries")
		}
		e, next, err := nextTreeEntry(o.data, pos)
		if err != nil {
			return err
		}
		if err := validComponent(e.name); err != nil {
			return err
		}
		if _, exists := names[string(e.name)]; exists {
			return fmt.Errorf("duplicate tree entry name")
		}
		names[string(e.name)] = struct{}{}
		if count != 0 && treeOrder(previous, e) >= 0 {
			return fmt.Errorf("tree entries are not in Git order")
		}
		if e.mode == gitlinkMode {
			return fmt.Errorf("submodule gitlinks are not supported")
		}
		target := b.objects[e.id]
		expected := blobObject
		if e.mode == directoryMode {
			expected = treeObject
		}
		if target == nil || target.kind != expected {
			return fmt.Errorf("tree entry has a missing or wrong-type object")
		}
		previous = e
		pos = next
	}
	o.treeEntries = len(names)
	return nil
}

func headerEnd(data []byte) (int, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return 0, fmt.Errorf("NUL in commit/tag object")
	}
	end := bytes.Index(data[:min(len(data), maxObjectHeader+2)], []byte("\n\n"))
	if end < 0 || end > maxObjectHeader {
		return 0, fmt.Errorf("missing or oversized commit/tag header")
	}
	return end, nil
}

func headerLine(data []byte, end, pos int) ([]byte, int) {
	n := bytes.IndexByte(data[pos:end], '\n')
	if n < 0 {
		return data[pos:end], end
	}
	return data[pos : pos+n], pos + n + 1
}

func identity(value []byte) error {
	last := bytes.LastIndexByte(value, ' ')
	if last < 0 || len(value)-last-1 != 5 {
		return fmt.Errorf("invalid author/committer/tagger identity")
	}
	zone := value[last+1:]
	if zone[0] != '+' && zone[0] != '-' {
		return fmt.Errorf("invalid identity timezone")
	}
	for _, c := range zone[1:] {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid identity timezone")
		}
	}
	if (zone[1]-'0')*10+zone[2]-'0' > 23 || (zone[3]-'0')*10+zone[4]-'0' > 59 {
		return fmt.Errorf("invalid identity timezone")
	}
	value = value[:last]
	last = bytes.LastIndexByte(value, ' ')
	if last < 0 || last == len(value)-1 {
		return fmt.Errorf("missing identity timestamp")
	}
	date := value[last+1:]
	if date[0] == '-' {
		date = date[1:]
	}
	if len(date) == 0 {
		return fmt.Errorf("invalid identity timestamp")
	}
	for _, c := range date {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid identity timestamp")
		}
	}
	if _, err := strconv.ParseInt(string(value[last+1:]), 10, 64); err != nil {
		return fmt.Errorf("invalid identity timestamp")
	}
	person := value[:last]
	open := bytes.IndexByte(person, '<')
	if open < 1 || person[open-1] != ' ' || len(person) == 0 || person[len(person)-1] != '>' ||
		bytes.ContainsAny(person[:open], "<>\r\n") || bytes.ContainsAny(person[open+1:len(person)-1], "<>\r\n") {
		return fmt.Errorf("invalid identity name/email")
	}
	return nil
}

func additionalHeaders(data []byte, end, pos int, tag bool) error {
	continuation := false
	tagger := false
	for pos < end {
		line, next := headerLine(data, end, pos)
		pos = next
		if len(line) == 0 || bytes.ContainsAny(line, "\r\x00") {
			return fmt.Errorf("invalid object header")
		}
		if line[0] == ' ' {
			if !continuation {
				return fmt.Errorf("misplaced header continuation")
			}
			continue
		}
		split := bytes.IndexByte(line, ' ')
		if split < 1 || split+1 == len(line) {
			return fmt.Errorf("invalid object header field")
		}
		key := string(line[:split])
		for _, c := range line[:split] {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return fmt.Errorf("invalid object header key")
			}
		}
		switch key {
		case "tree", "parent", "author", "committer", "object", "type", "tag":
			return fmt.Errorf("duplicate or misplaced structural header")
		case "tagger":
			if !tag || tagger {
				return fmt.Errorf("duplicate or misplaced tagger")
			}
			if err := identity(line[split+1:]); err != nil {
				return err
			}
			tagger = true
			continuation = false
		default:
			continuation = true
		}
	}
	return nil
}

func parseCommit(o *object) error {
	end, err := headerEnd(o.data)
	if err != nil {
		return err
	}
	line, pos := headerLine(o.data, end, 0)
	if len(line) != 45 || !bytes.HasPrefix(line, []byte("tree ")) {
		return fmt.Errorf("invalid commit tree header")
	}
	o.tree, err = decodeID(line[5:])
	if err != nil {
		return err
	}
	o.parentsStart = pos
	parents := make(map[objectID]struct{})
	for {
		if pos >= end {
			return fmt.Errorf("missing commit author/committer")
		}
		line, next := headerLine(o.data, end, pos)
		if !bytes.HasPrefix(line, []byte("parent ")) {
			break
		}
		id, err := decodeID(line[7:])
		if err != nil {
			return err
		}
		if _, exists := parents[id]; exists || len(parents) == maxParents {
			return fmt.Errorf("duplicate or excessive commit parents")
		}
		parents[id] = struct{}{}
		pos = next
	}
	o.parentsEnd = pos
	line, pos = headerLine(o.data, end, pos)
	if !bytes.HasPrefix(line, []byte("author ")) {
		return fmt.Errorf("missing commit author")
	}
	if err := identity(line[7:]); err != nil {
		return err
	}
	if pos >= end {
		return fmt.Errorf("missing commit committer")
	}
	line, pos = headerLine(o.data, end, pos)
	if !bytes.HasPrefix(line, []byte("committer ")) {
		return fmt.Errorf("missing commit committer")
	}
	if err := identity(line[10:]); err != nil {
		return err
	}
	return additionalHeaders(o.data, end, pos, false)
}

func parseTag(o *object) error {
	end, err := headerEnd(o.data)
	if err != nil {
		return err
	}
	line, pos := headerLine(o.data, end, 0)
	if !bytes.HasPrefix(line, []byte("object ")) {
		return fmt.Errorf("missing tag object")
	}
	o.target, err = decodeID(line[7:])
	if err != nil || pos >= end {
		return fmt.Errorf("invalid tag target")
	}
	line, pos = headerLine(o.data, end, pos)
	switch string(line) {
	case "type commit":
		o.targetType = commitObject
	case "type tree":
		o.targetType = treeObject
	case "type blob":
		o.targetType = blobObject
	case "type tag":
		o.targetType = tagObject
	default:
		return fmt.Errorf("invalid tag target type")
	}
	if pos >= end {
		return fmt.Errorf("missing tag name")
	}
	line, pos = headerLine(o.data, end, pos)
	if !bytes.HasPrefix(line, []byte("tag ")) {
		return fmt.Errorf("missing tag name")
	}
	if err := ValidateRef("refs/tags/" + string(line[4:])); err != nil {
		return fmt.Errorf("invalid tag name")
	}
	return additionalHeaders(o.data, end, pos, true)
}

func (b *Bundle) validateObjects(ordered []*object) error {
	for _, o := range ordered {
		var err error
		switch o.kind {
		case commitObject:
			err = parseCommit(o)
		case treeObject:
			err = b.validateTree(o)
		case tagObject:
			err = parseTag(o)
		}
		if err != nil {
			return err
		}
	}
	for _, o := range ordered {
		switch o.kind {
		case commitObject:
			if tree := b.objects[o.tree]; tree == nil || tree.kind != treeObject {
				return fmt.Errorf("commit has a missing or wrong-type tree")
			}
			for pos := o.parentsStart; pos < o.parentsEnd; pos += 48 {
				id, _ := decodeID(o.data[pos+7 : pos+47])
				if parent := b.objects[id]; parent == nil || parent.kind != commitObject {
					return fmt.Errorf("commit has a missing or wrong-type parent")
				}
			}
		case tagObject:
			if target := b.objects[o.target]; target == nil || target.kind != o.targetType {
				return fmt.Errorf("tag has a missing or wrong-type target")
			}
		}
	}
	return b.validateGraph(ordered)
}

type graphFrame struct {
	object *object
	next   int
}

func frame(o *object) graphFrame {
	pos := 0
	if o.kind == commitObject {
		pos = -1
	}
	return graphFrame{object: o, next: pos}
}

func (b *Bundle) nextChild(f *graphFrame) (*object, error) {
	o := f.object
	switch o.kind {
	case commitObject:
		if f.next == -1 {
			f.next = o.parentsStart
			return b.objects[o.tree], nil
		}
		if f.next < o.parentsEnd {
			id, _ := decodeID(o.data[f.next+7 : f.next+47])
			f.next += 48
			return b.objects[id], nil
		}
	case treeObject:
		if f.next < len(o.data) {
			e, next, err := nextTreeEntry(o.data, f.next)
			if err != nil {
				return nil, err
			}
			f.next = next
			return b.objects[e.id], nil
		}
	case tagObject:
		if f.next == 0 {
			f.next = 1
			return b.objects[o.target], nil
		}
	}
	return nil, io.EOF
}

func (b *Bundle) treeBounds(o *object) error {
	for pos := 0; pos < len(o.data); {
		e, next, err := nextTreeEntry(o.data, pos)
		if err != nil {
			return err
		}
		pos = next
		length, depth := len(e.name), 1
		if e.mode == directoryMode {
			child := b.objects[e.id]
			if child.pathBytes > 0 {
				length += 1 + child.pathBytes
			}
			depth += child.pathDepth
		}
		if length > maxPathBytes || depth > maxPathDepth {
			return fmt.Errorf("tree exceeds path length or depth limit")
		}
		o.pathBytes = max(o.pathBytes, length)
		o.pathDepth = max(o.pathDepth, depth)
	}
	return nil
}

func (b *Bundle) validateGraph(ordered []*object) error {
	for _, root := range ordered {
		if root.state != 0 {
			continue
		}
		root.state = 1
		stack := []graphFrame{frame(root)}
		for len(stack) > 0 {
			current := &stack[len(stack)-1]
			child, err := b.nextChild(current)
			if err == io.EOF {
				if current.object.kind == treeObject {
					if err := b.treeBounds(current.object); err != nil {
						return err
					}
				}
				if err := b.fingerprintObject(current.object); err != nil {
					return err
				}
				current.object.state = 2
				stack = stack[:len(stack)-1]
				continue
			}
			if err != nil {
				return err
			}
			if child.state == 1 {
				return fmt.Errorf("cyclic object graph")
			}
			if child.state == 0 {
				child.state = 1
				stack = append(stack, frame(child))
			}
		}
	}
	return nil
}
