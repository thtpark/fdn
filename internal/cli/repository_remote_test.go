package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestRepositoryRemoteAcceptsGitCanonicalAndExplicitAddresses(t *testing.T) {
	t.Setenv("GIT_DIR", "")
	for _, address := range []string{"repository_fixture", "fdn::repository_fixture"} {
		t.Run(address, func(t *testing.T) {
			var output bytes.Buffer
			a := &app{in: strings.NewReader("capabilities\n"), out: &output}
			r := &resources{config: config.Worker{StateDir: t.TempDir()}}
			if _, err := a.repositoryLocal(context.Background(), r, "remote", []string{"origin", address}); err != nil {
				t.Fatal("Git's canonical repository address was rejected", err)
			}
			if output.String() != "fetch\npush\noption\n\n" {
				t.Fatal("remote helper did not negotiate its actual supported operations", output.String())
			}
		})
	}
	for _, address := range []string{"", "https://example.invalid/repository_fixture", "fdn::fdn::repository_fixture", "fdn::other_fixture", "repository_fixture/path", "../repository_fixture"} {
		t.Run(address, func(t *testing.T) {
			var output bytes.Buffer
			a := &app{in: strings.NewReader("capabilities\n"), out: &output}
			if _, err := a.repositoryLocal(context.Background(), nil, "remote", []string{"origin", address}); domain.AsFault(err).Code != domain.Invalid || output.Len() != 0 {
				t.Fatal("non-repository address reached the transport", err, output.String())
			}
		})
	}
}

func TestRepositoryRemoteActualGitStripsHelperScheme(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("actual Git is required for the remote-helper contract", err)
	}
	directory := t.TempDir()
	capture := filepath.Join(directory, "arguments")
	helper := filepath.Join(directory, "git-remote-fdn")
	content := "#!/bin/sh\nprintf '%s\\n' \"$1\" \"$2\" > \"$FDN_TEST_GIT_ARGUMENTS\"\nexit 1\n"
	if err := os.WriteFile(helper, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(git, "ls-remote", "fdn::repository_fixture")
	command.Dir = directory
	command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FDN_TEST_GIT_ARGUMENTS="+capture, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0")
	if err := command.Run(); err == nil {
		t.Fatal("recording-only fixture unexpectedly completed a fetch")
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal("Git did not invoke its actual remote helper", err)
	}
	if string(data) != "fdn::repository_fixture\nrepository_fixture\n" {
		t.Fatal("Git's actual helper argument contract changed", string(data))
	}
}
