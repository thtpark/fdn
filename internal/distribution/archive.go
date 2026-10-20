package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type Entry struct {
	Name string
	Path string
	Data []byte
	Mode int64
}

func TarGzip(w io.Writer, entries []Entry, epoch int64) (err error) {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	compressed, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	compressed.Header.ModTime = time.Unix(0, 0)
	archive := tar.NewWriter(compressed)
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Name == "" || path.Clean(entry.Name) != entry.Name || strings.HasPrefix(entry.Name, "/") || strings.HasPrefix(entry.Name, "../") || seen[entry.Name] {
			return domain.Fail(domain.Invalid, "archive entry name must be unique, relative, and canonical")
		}
		seen[entry.Name] = true
		var reader io.Reader = bytes.NewReader(entry.Data)
		size := int64(len(entry.Data))
		var file *os.File
		if entry.Path != "" {
			if len(entry.Data) != 0 {
				return domain.Fail(domain.Invalid, "archive entry cannot contain both data and a source path")
			}
			file, err = os.Open(entry.Path)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return err
			}
			if !info.Mode().IsRegular() {
				file.Close()
				return domain.Fail(domain.Invalid, "release inputs must be regular files")
			}
			size, reader = info.Size(), file
		}
		header := &tar.Header{Name: entry.Name, Mode: entry.Mode, Size: size, ModTime: time.Unix(epoch, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		headerErr := archive.WriteHeader(header)
		var copyErr error
		if headerErr == nil {
			_, copyErr = io.CopyN(archive, reader, size)
		}
		var closeErr error
		if file != nil {
			closeErr = file.Close()
		}
		if err := errors.Join(headerErr, copyErr, closeErr); err != nil {
			return err
		}
	}
	return errors.Join(archive.Close(), compressed.Close())
}

func Debian(w io.Writer, control, data []Entry, epoch int64) error {
	var controlBytes, dataBytes bytes.Buffer
	if err := TarGzip(&controlBytes, control, epoch); err != nil {
		return err
	}
	if err := TarGzip(&dataBytes, data, epoch); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "!<arch>\n"); err != nil {
		return err
	}
	for _, entry := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", controlBytes.Bytes()}, {"data.tar.gz", dataBytes.Bytes()}} {
		header := fmt.Sprintf("%-16s%-12s%-6s%-6s%-8s%-10s`\n", entry.name+"/", strconv.FormatInt(epoch, 10), "0", "0", "100644", strconv.Itoa(len(entry.data)))
		if len(header) != 60 {
			return domain.Fail(domain.Internal, "invalid deterministic Debian ar header")
		}
		if _, err := io.WriteString(w, header); err != nil {
			return err
		}
		if _, err := w.Write(entry.data); err != nil {
			return err
		}
		if len(entry.data)%2 != 0 {
			if _, err := w.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	return nil
}

func SHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type MergeEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Base           struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

func CheckReleaseEvent(actions, eventName string, event MergeEvent, commit string) error {
	if actions != "true" || eventName != "pull_request" || event.Action != "closed" || !event.PullRequest.Merged || event.PullRequest.Base.Ref != "release" || event.PullRequest.MergeCommitSHA != commit || len(commit) != 40 {
		return domain.Fail(domain.Denied, "release packaging is allowed only in GitHub Actions for the exact PR merge commit into release")
	}
	return nil
}
