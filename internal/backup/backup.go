package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

var filenames = []string{"foundation.sqlite", "ca.pem", "ca-key.pem", "server.pem", "server-key.pem"}

type File struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Schema    int             `json:"schema_version"`
	CreatedAt int64           `json:"created_at"`
	Files     map[string]File `json:"files"`
}

func Keygen(path string) (string, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := secure.Create(path, []byte(identity.String()+"\n")); err != nil {
		return "", err
	}
	return identity.Recipient().String(), nil
}

func Create(ctx context.Context, store *foundation.Store, c config.Server, output, recipient string) (err error) {
	target, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return domain.Fail(domain.Invalid, "invalid age backup recipient")
	}
	stage, err := os.MkdirTemp(c.StateDir, ".backup-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	if err := store.Snapshot(ctx, filepath.Join(stage, "foundation.sqlite")); err != nil {
		return err
	}
	for _, name := range filenames[1:] {
		path := filepath.Join(c.StateDir, name)
		if name == "server.pem" {
			path = c.CertFile
		}
		if name == "server-key.pem" {
			path = c.KeyFile
		}
		data, err := secure.Read(path, domain.MaxBody)
		if err != nil {
			return err
		}
		if err := secure.Create(filepath.Join(stage, name), data); err != nil {
			return err
		}
	}
	manifest := Manifest{Schema: domain.SchemaVersion, CreatedAt: time.Now().UTC().Unix(), Files: map[string]File{}}
	for _, name := range filenames {
		file, err := os.Open(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		manifest.Files[name] = File{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := secure.Create(filepath.Join(stage, "manifest.json"), data); err != nil {
		return err
	}
	if err := secure.Dir(filepath.Dir(output)); err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(output))
		}
	}()
	encrypted, err := age.Encrypt(file, target)
	if err != nil {
		return err
	}
	archive := tar.NewWriter(encrypted)
	for _, name := range append(append([]string{}, filenames...), "manifest.json") {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, err := os.Open(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		info, err := source.Stat()
		if err != nil {
			source.Close()
			return err
		}
		if err := archive.WriteHeader(&tar.Header{Name: name, Size: info.Size(), Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(archive, source)
		closeErr := source.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := encrypted.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(output))
}

func Restore(ctx context.Context, input, identityFile, destination string) (err error) {
	identityBytes, err := secure.Read(identityFile, 8192)
	if err != nil {
		return err
	}
	identities, err := age.ParseIdentities(bytes.NewReader(identityBytes))
	if err != nil {
		return domain.Fail(domain.Invalid, "invalid private age identity")
	}
	if _, err := os.Lstat(destination); err == nil {
		return domain.Fail(domain.Conflict, "restore requires a new destination; existing state is never overwritten")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := secure.Dir(filepath.Dir(destination)); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".fdn-restore-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	decrypted, err := age.Decrypt(file, identities...)
	if err != nil {
		return domain.Fail(domain.Denied, "backup decryption failed")
	}
	archive := tar.NewReader(decrypted)
	seen := map[string]bool{}
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return domain.Fail(domain.Invalid, "backup is truncated or corrupt")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if (!slices.Contains(filenames, header.Name) && header.Name != "manifest.json") || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 2<<30 {
			return domain.Fail(domain.Invalid, "unexpected, repeated, or oversized backup entry")
		}
		seen[header.Name] = true
		out, err := os.OpenFile(filepath.Join(stage, header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		size, copyErr := io.Copy(out, archive)
		syncErr := out.Sync()
		closeErr := out.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
		if size != header.Size {
			return domain.Fail(domain.Invalid, "backup entry size mismatch")
		}
	}
	if _, err := io.Copy(io.Discard, decrypted); err != nil {
		return domain.Fail(domain.Invalid, "backup authentication failed")
	}
	if len(seen) != len(filenames)+1 {
		return domain.Fail(domain.Invalid, "backup is incomplete")
	}
	var manifest Manifest
	if err := config.Read(filepath.Join(stage, "manifest.json"), &manifest); err != nil {
		return err
	}
	if manifest.Schema != domain.SchemaVersion || len(manifest.Files) != len(filenames) {
		return domain.Fail(domain.Prerequisite, "incompatible backup schema")
	}
	for _, name := range filenames {
		source, err := os.Open(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, source)
		closeErr := source.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		want, ok := manifest.Files[name]
		if !ok || size != want.Size || hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
			return domain.Fail(domain.Invalid, "backup checksum mismatch")
		}
	}
	if _, err := pki.Server(filepath.Join(stage, "server.pem"), filepath.Join(stage, "server-key.pem")); err != nil {
		return err
	}
	store, err := foundation.Open(ctx, filepath.Join(stage, "foundation.sqlite"))
	if err != nil {
		return err
	}
	checkErr := store.Check(ctx)
	retireErr := store.RetireSessions(ctx)
	closeErr := store.Close()
	if err := errors.Join(checkErr, retireErr, closeErr); err != nil {
		return err
	}
	c := config.Server{StateDir: destination, Listen: "127.0.0.1:8443", CertFile: filepath.Join(destination, "server.pem"), KeyFile: filepath.Join(destination, "server-key.pem")}
	if err := config.Write(filepath.Join(stage, "server.json"), c); err != nil {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(destination))
}
