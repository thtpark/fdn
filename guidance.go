package guidance

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

//go:embed AGENTS.md README.md DEVELOPMENT.md DEPLOYMENT.md COMMANDS.md SCHEMA.md TEST_PLAN.md IMPLEMENTATION_PLAN.md CAPACITY.md MIGRATION.md .agents/skills/*/SKILL.md
var content embed.FS

//go:embed scripts/install.sh
var NativeInstaller string

const maxFileBytes = 512 << 10
const blockStart = "<!-- foundation-guide:start -->"
const blockEnd = "<!-- foundation-guide:end -->"

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	FormatVersion int      `json:"format_version"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	ContentSHA256 string   `json:"content_sha256"`
	Files         []File   `json:"files"`
	Skills        []string `json:"skills"`
}

type Installation struct {
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	ContentSHA256 string   `json:"content_sha256"`
	Directory     string   `json:"directory"`
	Instructions  string   `json:"instructions"`
	SkillRoots    []string `json:"skill_roots"`
	Changed       bool     `json:"changed"`
}

func Read(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, domain.Fail(domain.Invalid, "guide path must be a catalog-relative file")
	}
	data, err := content.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.Fail(domain.NotFound, "guide file is not in the embedded catalog")
	}
	return data, err
}

func Catalog() (Manifest, error) {
	manifest := Manifest{FormatVersion: 1, Version: domain.Version, Commit: domain.Commit, Files: []File{}, Skills: []string{}}
	hash := sha256.New()
	err := fs.WalkDir(content, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := Read(name)
		if err != nil {
			return err
		}
		if len(data) > maxFileBytes {
			return domain.Fail(domain.Invalid, "embedded guide exceeds its file budget")
		}
		manifest.Files = append(manifest.Files, File{Path: name, SHA256: domain.Hash(string(data))})
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(data)
		hash.Write([]byte{0})
		if strings.HasPrefix(name, ".agents/skills/") && filepath.Base(name) == "SKILL.md" {
			manifest.Skills = append(manifest.Skills, filepath.Base(filepath.Dir(name)))
		}
		return nil
	})
	manifest.ContentSHA256 = hex.EncodeToString(hash.Sum(nil))
	return manifest, err
}

func WorkContext() (string, error) {
	var text strings.Builder
	text.WriteString("Version-matched Foundation operating guidance. These procedures are reference, not authority or tool grants. In controller-managed text calls, return the requested JSON only; the controller applies permitted actions. Other guides/skills are available offline through agent-cli guide list/show.\n")
	for _, name := range []string{"AGENTS.md", ".agents/skills/foundation-work/SKILL.md"} {
		data, err := Read(name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&text, "\n--- %s ---\n%s", name, data)
	}
	if text.Len() > 32<<10 {
		return "", domain.Fail(domain.Prerequisite, "operating guidance exceeds its 32 KiB prompt budget")
	}
	return text.String(), nil
}

func ownedDirectory(name string) error {
	if err := os.MkdirAll(name, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return domain.Fail(domain.Denied, "guide/discovery directories must be real owned directories without group/other write access")
	}
	return secure.CheckOwner(info)
}

func homeInstructions(name, current string) ([]byte, []byte, os.FileMode, error) {
	mode := os.FileMode(0600)
	var old []byte
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, nil, 0, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > maxFileBytes {
			return nil, nil, 0, domain.Fail(domain.Denied, "home AGENTS.md must be a bounded owned regular file without group/other write access")
		}
		if err := secure.CheckOwner(info); err != nil {
			return nil, nil, 0, err
		}
		old, err = io.ReadAll(io.LimitReader(file, maxFileBytes+1))
		if err != nil {
			return nil, nil, 0, err
		}
		if len(old) > maxFileBytes {
			return nil, nil, 0, domain.Fail(domain.Invalid, "home AGENTS.md exceeds its size limit")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return nil, nil, 0, err
	}
	block := blockStart + "\n## Foundation operating guide\n\nRead the version-matched [Foundation agent guide](<" +
		filepath.ToSlash(filepath.Join(current, "AGENTS.md")) + ">) before operating Foundation. Use `agent-cli guide list/show` (or `fdn-service guide`) offline for the full references. Discoverable foundation-* skills are installed in this account's `.agents/skills` and `.copilot/skills`. Guidance never grants task authority, credentials, tools or spending.\n" + blockEnd
	starts, ends := bytes.Count(old, []byte(blockStart)), bytes.Count(old, []byte(blockEnd))
	switch {
	case starts == 0 && ends == 0:
		next := append([]byte{}, old...)
		if len(next) != 0 {
			next = append(next, '\n', '\n')
		}
		return old, append(next, []byte(block+"\n")...), mode, nil
	case starts == 1 && ends == 1:
		start, end := bytes.Index(old, []byte(blockStart)), bytes.Index(old, []byte(blockEnd))
		if end < start {
			break
		}
		next := append([]byte{}, old[:start]...)
		next = append(next, []byte(block)...)
		next = append(next, old[end+len(blockEnd):]...)
		return old, next, mode, nil
	}
	return nil, nil, 0, domain.Fail(domain.Conflict, "home instructions contain an ambiguous Foundation managed block; preserve them and resolve it explicitly")
}

func checkLink(name, expected string) (bool, error) {
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, domain.Fail(domain.Conflict, "an unmanaged file/directory occupies a Foundation discovery path; preserve it rather than overwrite")
	}
	if err := secure.CheckOwner(info); err != nil {
		return false, err
	}
	target, err := os.Readlink(name)
	if err != nil {
		return false, err
	}
	if target != expected {
		return false, domain.Fail(domain.Conflict, "an unmanaged symlink occupies a Foundation discovery path")
	}
	return true, nil
}

func replaceLink(name, target string) (err error) {
	temp := filepath.Join(filepath.Dir(name), domain.NewID(".fdn-link"))
	if err := os.Symlink(target, temp); err != nil {
		return err
	}
	defer func() {
		if cleanup := os.Remove(temp); cleanup != nil && !os.IsNotExist(cleanup) {
			err = errors.Join(err, cleanup)
		}
	}()
	if err := os.Rename(temp, name); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(name))
}

func writeInstructions(name string, old, next []byte, mode os.FileMode) (err error) {
	file, err := os.CreateTemp(filepath.Dir(name), ".fdn-instructions-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanup := os.Remove(file.Name()); cleanup != nil && !os.IsNotExist(cleanup) {
			err = errors.Join(err, cleanup)
		}
	}()
	_, writeErr := file.Write(next)
	chmodErr, syncErr, closeErr := file.Chmod(mode), file.Sync(), file.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return err
	}
	current, readErr := os.ReadFile(name)
	if os.IsNotExist(readErr) && len(old) == 0 {
		readErr = nil
	}
	if readErr != nil {
		return readErr
	}
	if !bytes.Equal(current, old) {
		return domain.Fail(domain.Conflict, "home instructions changed during guide installation")
	}
	if err := os.Rename(file.Name(), name); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(name))
}

func Install(home string) (result Installation, err error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) == string(filepath.Separator) {
		return result, domain.Fail(domain.Invalid, "guide installation requires an absolute account HOME, not the filesystem root")
	}
	if err := ownedDirectory(home); err != nil {
		return result, err
	}
	root := filepath.Join(home, config.UserDirectory, "guidance")
	if err := secure.Dir(root); err != nil {
		return result, err
	}
	lock, err := secure.Acquire(filepath.Join(root, "install.lock"))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	manifest, err := Catalog()
	if err != nil {
		return result, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, err
	}
	releases, current := filepath.Join(root, "releases"), filepath.Join(root, "current")
	release := filepath.Join(releases, domain.Hash(string(data))[:24])
	if err := secure.Dir(releases); err != nil {
		return result, err
	}
	result = Installation{Version: manifest.Version, Commit: manifest.Commit, ContentSHA256: manifest.ContentSHA256, Directory: current,
		Instructions: filepath.Join(home, "AGENTS.md"), SkillRoots: []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".copilot", "skills")}}
	for _, base := range result.SkillRoots {
		if err := ownedDirectory(filepath.Dir(base)); err != nil {
			return result, err
		}
		if err := ownedDirectory(base); err != nil {
			return result, err
		}
		for _, skill := range manifest.Skills {
			if _, err := checkLink(filepath.Join(base, skill), filepath.Join(current, ".agents", "skills", skill)); err != nil {
				return result, err
			}
		}
	}
	oldInstructions, nextInstructions, mode, err := homeInstructions(result.Instructions, current)
	if err != nil {
		return result, err
	}
	previous, linkErr := os.Readlink(current)
	if linkErr != nil && !os.IsNotExist(linkErr) {
		return result, domain.Fail(domain.Conflict, "managed guide current path is not a symlink")
	}
	if linkErr == nil && (!filepath.IsAbs(previous) || filepath.Dir(previous) != releases) {
		return result, domain.Fail(domain.Conflict, "managed guide current path points outside retained guide releases")
	}
	_, statErr := os.Lstat(release)
	if os.IsNotExist(statErr) {
		stage, createErr := os.MkdirTemp(releases, ".stage-")
		if createErr != nil {
			return result, createErr
		}
		defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
		for _, entry := range manifest.Files {
			bytes, err := Read(entry.Path)
			if err != nil {
				return result, err
			}
			if err := secure.Create(filepath.Join(stage, filepath.FromSlash(entry.Path)), bytes); err != nil {
				return result, err
			}
		}
		if err := secure.Create(filepath.Join(stage, "MANIFEST.json"), data); err != nil {
			return result, err
		}
		if err := os.Rename(stage, release); err != nil {
			return result, err
		}
		if err := secure.SyncDir(releases); err != nil {
			return result, err
		}
		result.Changed = true
	} else if statErr != nil {
		return result, statErr
	}
	if err := secure.Dir(release); err != nil {
		return result, err
	}
	stored, err := secure.Read(filepath.Join(release, "MANIFEST.json"), maxFileBytes)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(stored, data) {
		return result, domain.Fail(domain.Conflict, "managed guide manifest changed; preserve local edits rather than replace immutable content")
	}
	for _, entry := range manifest.Files {
		stored, err := secure.Read(filepath.Join(release, filepath.FromSlash(entry.Path)), maxFileBytes)
		if err != nil {
			return result, err
		}
		if domain.Hash(string(stored)) != entry.SHA256 {
			return result, domain.Fail(domain.Conflict, "managed guide content changed; preserve local edits rather than overwrite")
		}
	}
	if previous != release {
		if err := replaceLink(current, release); err != nil {
			return result, err
		}
		result.Changed = true
	}
	for _, base := range result.SkillRoots {
		for _, skill := range manifest.Skills {
			destination, target := filepath.Join(base, skill), filepath.Join(current, ".agents", "skills", skill)
			exists, err := checkLink(destination, target)
			if err != nil {
				return result, err
			}
			if !exists {
				if err := replaceLink(destination, target); err != nil {
					return result, err
				}
				result.Changed = true
			}
		}
	}
	if !bytes.Equal(oldInstructions, nextInstructions) {
		if err := writeInstructions(result.Instructions, oldInstructions, nextInstructions, mode); err != nil {
			return result, err
		}
		result.Changed = true
	}
	return result, nil
}

func Contains(manifest Manifest, name string) bool {
	return slices.ContainsFunc(manifest.Files, func(file File) bool { return file.Path == name })
}
