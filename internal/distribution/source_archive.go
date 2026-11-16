package distribution

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

type sourceArchiveEntry struct {
	name, link string
	mode       int64
	kind       byte
	data       []byte
}

func sourceArchiveEntries(snapshot sourceSnapshot) []sourceArchiveEntry {
	prefix := "fdn-" + snapshot.Version + "-source"
	directories := map[string]bool{prefix: true}
	entries := make([]sourceArchiveEntry, 0, len(snapshot.files))
	for _, file := range snapshot.files {
		name := prefix + "/" + file.name
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			directories[parent] = true
		}
		entry := sourceArchiveEntry{name: name, mode: 0644, kind: tar.TypeReg, data: file.data}
		switch file.mode {
		case "100755":
			entry.mode = 0755
		case "120000":
			entry.mode, entry.kind, entry.link, entry.data = 0777, tar.TypeSymlink, string(file.data), nil
		}
		entries = append(entries, entry)
	}
	for directory := range directories {
		entries = append(entries, sourceArchiveEntry{name: directory + "/", mode: 0755, kind: tar.TypeDir})
	}
	slices.SortFunc(entries, func(a, b sourceArchiveEntry) int { return strings.Compare(a.name, b.name) })
	return entries
}

func writeSourceArchive(w io.Writer, snapshot sourceSnapshot) error {
	compressed, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)
	for _, entry := range sourceArchiveEntries(snapshot) {
		header := &tar.Header{Name: entry.name, Linkname: entry.link, Mode: entry.mode, Size: int64(len(entry.data)),
			Typeflag: entry.kind, ModTime: time.Unix(snapshot.SourceDateEpoch, 0), Format: tar.FormatPAX}
		if err := archive.WriteHeader(header); err != nil {
			return errors.Join(err, archive.Close(), compressed.Close())
		}
		if _, err := archive.Write(entry.data); err != nil {
			return errors.Join(err, archive.Close(), compressed.Close())
		}
	}
	return errors.Join(archive.Close(), compressed.Close())
}

func verifySourceArchive(name string, snapshot sourceSnapshot) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := bufio.NewReader(file)
	compressed, err := gzip.NewReader(buffer)
	if err != nil {
		return err
	}
	defer compressed.Close()
	compressed.Multistream(false)
	if !compressed.ModTime.IsZero() || compressed.Name != "" || compressed.Comment != "" || len(compressed.Extra) != 0 || compressed.OS != 255 {
		return errors.New("source gzip metadata differs from its declared rules")
	}
	archive := tar.NewReader(compressed)
	for _, entry := range sourceArchiveEntries(snapshot) {
		header, err := archive.Next()
		if err != nil {
			return errors.New("source archive is incomplete")
		}
		if header.Name != entry.name || header.Linkname != entry.link || header.Mode != entry.mode ||
			header.Size != int64(len(entry.data)) || header.Typeflag != entry.kind || header.Uid != 0 || header.Gid != 0 ||
			header.Uname != "" || header.Gname != "" || header.Devmajor != 0 || header.Devminor != 0 ||
			header.ModTime.Unix() != snapshot.SourceDateEpoch || header.ModTime.Nanosecond() != 0 ||
			!header.AccessTime.IsZero() || !header.ChangeTime.IsZero() {
			return errors.New("source archive entry differs from the release tree or declared rules")
		}
		for key, value := range header.PAXRecords {
			if !((key == "path" && value == entry.name) || (key == "linkpath" && value == entry.link)) {
				return errors.New("source archive contains undeclared PAX metadata")
			}
		}
		data, err := io.ReadAll(archive)
		if err != nil || !bytes.Equal(data, entry.data) {
			return errors.New("source archive bytes differ from the release tree")
		}
	}
	if _, err := archive.Next(); err != io.EOF {
		return errors.New("source archive contains extra or malformed entries")
	}
	if count, err := io.Copy(io.Discard, compressed); err != nil || count != 0 {
		return errors.New("source archive has trailing bytes or invalid gzip integrity")
	}
	if _, err := buffer.Peek(1); err != io.EOF {
		return errors.New("source archive has trailing compressed data")
	}
	return nil
}
