package provider

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

const pathStart = "# >>> Foundation managed provider PATH >>>"
const pathEnd = "# <<< Foundation managed provider PATH <<<"

type ShellCommands struct {
	Directory string   `json:"directory"`
	Profile   string   `json:"profile"`
	Commands  []string `json:"commands"`
	Changed   bool     `json:"changed"`
}

type shellFile struct {
	path   string
	old    []byte
	next   []byte
	mode   os.FileMode
	exists bool
}

func ownedShellDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return domain.Fail(domain.Denied, "provider command directories must be real owned directories without group/other write access")
	}
	return secure.CheckOwner(info)
}

func readShellFile(path string, defaultMode os.FileMode) (shellFile, error) {
	result := shellFile{path: path, mode: defaultMode}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 256<<10 {
		return result, domain.Fail(domain.Denied, "provider launchers/login profiles must be bounded owned regular files without group/other write access")
	}
	if err := secure.CheckOwner(info); err != nil {
		return result, err
	}
	result.old, err = io.ReadAll(io.LimitReader(file, 256<<10+1))
	if err != nil {
		return result, err
	}
	if len(result.old) > 256<<10 {
		return result, domain.Fail(domain.Invalid, "login profile exceeds its size limit")
	}
	result.mode = info.Mode().Perm()
	result.exists = true
	return result, nil
}

func (file shellFile) write() (err error) {
	temp, err := os.CreateTemp(filepath.Dir(file.path), ".fdn-shell-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanup := os.Remove(temp.Name()); cleanup != nil && !os.IsNotExist(cleanup) {
			err = errors.Join(err, cleanup)
		}
	}()
	_, writeErr := temp.Write(file.next)
	if err := errors.Join(writeErr, temp.Chmod(file.mode), temp.Sync(), temp.Close()); err != nil {
		return err
	}
	current, err := readShellFile(file.path, file.mode)
	if err != nil {
		return err
	}
	if !bytes.Equal(current.old, file.old) {
		return domain.Fail(domain.Conflict, "provider launcher/login profile changed during installation")
	}
	if err := os.Rename(temp.Name(), file.path); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(file.path))
}

func loginProfile(home, shell string) (string, error) {
	switch filepath.Base(shell) {
	case "zsh":
		return filepath.Join(home, ".zprofile"), nil
	case "bash":
		for _, name := range []string{".bash_profile", ".bash_login"} {
			path := filepath.Join(home, name)
			if _, err := os.Lstat(path); err == nil {
				return path, nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
	case "", "sh", "dash":
		if shell == "" && runtime.GOOS == "darwin" {
			return filepath.Join(home, ".zprofile"), nil
		}
	default:
		return "", domain.Fail(domain.Prerequisite, "automatic provider PATH setup supports sh, bash and zsh login shells")
	}
	return filepath.Join(home, ".profile"), nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func InstallShellCommands(home, configFile, cliPath, shell string) (result ShellCommands, err error) {
	for _, value := range []string{home, configFile, cliPath} {
		if !filepath.IsAbs(value) || strings.ContainsAny(value, "\r\n\x00") {
			return result, domain.Fail(domain.Invalid, "provider shell integration requires absolute single-line HOME/configuration/CLI paths")
		}
	}
	if filepath.Clean(home) == string(filepath.Separator) {
		return result, domain.Fail(domain.Invalid, "filesystem root is not an account HOME")
	}
	if err := ownedShellDirectory(home); err != nil {
		return result, err
	}
	if err := ownedShellDirectory(filepath.Join(home, ".local")); err != nil {
		return result, err
	}
	result.Directory = filepath.Join(home, ".local", "bin")
	if err := ownedShellDirectory(result.Directory); err != nil {
		return result, err
	}
	lock, err := secure.Acquire(filepath.Join(home, ".local", "share", "fdn", "provider-shell", "install.lock"))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	result.Profile, err = loginProfile(home, shell)
	if err != nil {
		return result, err
	}
	files := []shellFile{}
	for _, name := range []string{"codex", "copilot"} {
		file, err := readShellFile(filepath.Join(result.Directory, name), 0700)
		if err != nil {
			return result, err
		}
		marker := "#!/bin/sh\n# Foundation managed provider launcher v1 " + name + " " + domain.Hash(configFile) + "\n"
		if file.exists && !bytes.HasPrefix(file.old, []byte(marker)) {
			return result, domain.Fail(domain.Conflict, "an unmanaged command occupies codex/copilot in the account's .local/bin; preserve it rather than overwrite")
		}
		file.next = []byte(marker + "exec " + shellQuote(cliPath) + " --config " + shellQuote(configFile) + " provider exec --provider " + name + " -- \"$@\"\n")
		file.mode = 0700
		files = append(files, file)
		result.Commands = append(result.Commands, file.path)
	}
	profile, err := readShellFile(result.Profile, 0600)
	if err != nil {
		return result, err
	}
	block := pathStart + "\ncase \":$PATH:\" in\n  *\":$HOME/.local/bin:\"*) ;;\n  *) PATH=\"$HOME/.local/bin:$PATH\" ;;\nesac\nexport PATH\n" + pathEnd
	starts, ends := bytes.Count(profile.old, []byte(pathStart)), bytes.Count(profile.old, []byte(pathEnd))
	if starts == 0 && ends == 0 {
		profile.next = append(append([]byte{}, profile.old...), []byte("\n"+block+"\n")...)
	} else if starts == 1 && ends == 1 {
		start, end := bytes.Index(profile.old, []byte(pathStart)), bytes.Index(profile.old, []byte(pathEnd))
		if end < start {
			return result, domain.Fail(domain.Conflict, "login profile contains an ambiguous Foundation PATH block")
		}
		profile.next = append(append(append([]byte{}, profile.old[:start]...), []byte(block)...), profile.old[end+len(pathEnd):]...)
	} else {
		return result, domain.Fail(domain.Conflict, "login profile contains ambiguous Foundation PATH blocks")
	}
	files = append(files, profile)
	for _, file := range files {
		if bytes.Equal(file.old, file.next) {
			continue
		}
		if err := file.write(); err != nil {
			return result, err
		}
		result.Changed = true
	}
	return result, nil
}
