package filestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type runnerFunc func(context.Context, process.Spec) error

func (f runnerFunc) Run(ctx context.Context, spec process.Spec) error { return f(ctx, spec) }

func TestDarwinExactBoundedPinnedNativeCommands(t *testing.T) {
	path := filepath.Join(fixtureDirectory(t), "original.txt")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	id := strconv.Itoa(os.Geteuid())
	var commands []string
	p := nativePermissions{runner: runnerFunc(func(ctx context.Context, spec process.Spec) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > nativeTimeout || time.Until(deadline) <= 0 {
			t.Fatal("native command has no bounded lifetime", deadline)
		}
		if !slices.Contains(spec.Env, "LC_ALL=C") {
			t.Fatal("native command lacks deterministic locale")
		}
		commands = append(commands, spec.Path+" "+strings.Join(spec.Args, " "))
		switch spec.Path {
		case "/usr/bin/id":
			if spec.Stdin != nil {
				t.Fatal("identity command received the file descriptor")
			}
			if spec.Args[0] == "-nu" {
				if !slices.Equal(spec.Args, []string{"-nu", id}) {
					t.Fatal(spec.Args)
				}
				_, err = fmt.Fprintln(spec.Stdout, "fixture")
			} else {
				if !slices.Equal(spec.Args, []string{"-u", "fixture"}) {
					t.Fatal(spec.Args)
				}
				_, err = fmt.Fprintln(spec.Stdout, id)
			}
			return err
		case "/bin/chmod":
			if spec.Stdin != file || spec.Args[len(spec.Args)-1] != "/dev/fd/0" {
				t.Fatal("chmod is not bound to the exact inherited descriptor", spec)
			}
			return nil
		case "/bin/ls":
			if spec.Stdin != nil || !slices.Equal(spec.Args, []string{"-Llde", path}) {
				t.Fatal(spec)
			}
			_, err := fmt.Fprintln(spec.Stdout, "-rw-------+ 1 owner group 0 Jan 1 00:00 original.txt\n 0: user:fixture allow read")
			return err
		}
		return errors.New("unexpected native command")
	})}
	if err := p.applyFile(t.Context(), file, []uint32{uint32(os.Geteuid()), uint32(os.Geteuid())}); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"/usr/bin/id -nu " + id,
		"/usr/bin/id -u fixture",
		"/bin/chmod -N /dev/fd/0",
		"/bin/chmod +a user:fixture allow read /dev/fd/0",
		"/bin/ls -Llde " + path,
	}
	if !slices.Equal(commands, expected) {
		t.Fatal(commands)
	}
}

func TestDarwinEmptyReaderNativePlanAndFailures(t *testing.T) {
	path := filepath.Join(fixtureDirectory(t), "original.txt")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	calls := 0
	p := nativePermissions{runner: runnerFunc(func(_ context.Context, spec process.Spec) error {
		calls++
		if calls == 1 {
			if spec.Path != "/bin/chmod" || !slices.Equal(spec.Args, []string{"-N", "/dev/fd/0"}) || spec.Stdin != file {
				t.Fatal(spec)
			}
			return nil
		}
		if calls != 2 || spec.Path != "/bin/ls" {
			t.Fatal(spec)
		}
		_, err := fmt.Fprintln(spec.Stdout, "-rw------- 1 owner group 0 Jan 1 00:00 original.txt")
		return err
	})}
	if err := p.applyFile(t.Context(), file, nil); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	sentinel := errors.New("native ACL unsupported")
	p.runner = runnerFunc(func(context.Context, process.Spec) error { return sentinel })
	if err := p.applyFile(t.Context(), file, nil); !errors.Is(err, sentinel) {
		t.Fatal("native failure was ignored", err)
	}
	requireCode(t, p.applyFile(t.Context(), file, []uint32{4294967293}), domain.Prerequisite)
	p.runner = runnerFunc(func(_ context.Context, spec process.Spec) error {
		_, err := spec.Stdout.Write(make([]byte, 65537))
		return err
	})
	if err := p.applyFile(t.Context(), file, nil); err == nil {
		t.Fatal("unbounded native output accepted")
	}
	p.runner = runnerFunc(func(context.Context, process.Spec) error { t.Fatal("canceled native command ran"); return nil })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.applyFile(ctx, file, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestParseDarwinACLRejectsBroaderUnresolvedAndInheritedEntries(t *testing.T) {
	for _, output := range []string{
		"",
		"          ",
		"not a native file listing",
		"-rw-------+ 1 owner group 0 original",
		"-rw------- 1 owner group 0 original\n 0: user:reader allow read",
		"-rw-------+ 1 owner group 0 original\n 0: group:staff allow read",
		"-rw-------+ 1 owner group 0 original\n 0: user:reader allow read,write",
		"-rw-------+ 1 owner group 0 original\n 0: user:reader allow execute",
		"-rw-------+ 1 owner group 0 original\n 0: user:reader deny read",
		"-rw-------+ 1 owner group 0 original\n 0: UUID allow read",
		"-rw-------+ 1 owner group 0 original\n 0: user:reader inherited allow read",
		"-rw-------+ 1 owner group 0 original\n 0: user:reader allow read\n 1: user:reader allow read",
		"-rw-------+ 1 owner group 0 original\n 9: user:reader allow read",
	} {
		_, err := parseDarwinACL([]byte(output))
		requireCode(t, err, domain.Denied)
	}
	for _, output := range []string{
		"-rw------- 1 owner group 0 original",
		"-rw-------@ 1 owner group 0 original",
		"-rw-------+ 1 owner group 0 original\n 0: user:reader allow read",
		"-rw-------@ 1 owner group 0 original\n 0: user:reader allow read",
	} {
		if _, err := parseDarwinACL([]byte(output)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDarwinOnlyNormalizesVerifiedSystemVarAlias(t *testing.T) {
	path, err := canonicalAbsolutePath("/var/foundation-filestore-fixture/file")
	if err != nil || path != "/private/var/foundation-filestore-fixture/file" {
		t.Fatal(path, err)
	}
	for _, original := range []string{"/variable/keep", "/Users/var/keep", "/private/var/keep"} {
		path, err := canonicalAbsolutePath(original)
		if err != nil || path != original {
			t.Fatal("non-system alias path was rewritten", path, err)
		}
	}
	base := fixtureDirectory(t)
	link := filepath.Join(base, "var")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(link, "files"), fixturePermissions); err == nil {
		t.Fatal("task-controlled alias was followed")
	}
}

func TestDarwinNativeACLActuallyAddsAndRevokesCurrentUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("native fixture intentionally runs without root")
	}
	store, _ := fixtureStore(t, nil)
	if err := store.Create("file_1", "original.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetReaders(t.Context(), "file_1", "original.txt", []uint32{uint32(os.Geteuid())}); err != nil {
		t.Fatal(err)
	}
	file, err := store.OpenFile("file_1", "original.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	p := nativePermissions{runner: process.Native{}}
	readers, err := p.inspect(t.Context(), file)
	if err != nil || len(readers) != 1 {
		t.Fatal("native named ACL was not installed", readers, err)
	}
	if err := store.SetReaders(t.Context(), "file_1", "original.txt", nil); err != nil {
		t.Fatal(err)
	}
	readers, err = p.inspect(t.Context(), file)
	if err != nil || len(readers) != 0 {
		t.Fatal("native named ACL was not revoked", readers, err)
	}
}
