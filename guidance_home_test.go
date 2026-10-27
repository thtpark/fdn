package guidance_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	guidance "github.com/thtpark/fdn"
)

func TestGuideLayoutMovesManagedDiscoveryWithoutOverwritingLegacyEvidence(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".local", "share", "fdn", "guidance")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(legacy, "retained-evidence")
	if err := os.WriteFile(evidence, []byte("retained legacy guide"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := guidance.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{".agents", ".copilot"} {
		base := filepath.Join(home, directory, "skills")
		if err := os.MkdirAll(base, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range catalog.Skills {
			if err := os.Symlink(filepath.Join(legacy, "current", ".agents", "skills", name), filepath.Join(base, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("Preserve my instructions.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	installed, err := guidance.Install(home)
	if err != nil || installed.Directory != filepath.Join(home, ".foundation", "guidance", "current") {
		t.Fatal(installed, err)
	}
	for _, root := range installed.SkillRoots {
		for _, name := range catalog.Skills {
			target, err := os.Readlink(filepath.Join(root, name))
			if err != nil || target != filepath.Join(installed.Directory, ".agents", "skills", name) {
				t.Fatal("managed discovery did not follow new Foundation root", target, err)
			}
		}
	}
	if data, err := os.ReadFile(evidence); err != nil || string(data) != "retained legacy guide" {
		t.Fatal("legacy guide evidence was changed", err)
	}
	if data, err := os.ReadFile(installed.Instructions); err != nil || !bytes.HasPrefix(data, []byte("Preserve my instructions.\n")) {
		t.Fatal("unrelated home instructions changed", err)
	}
	second, err := guidance.Install(home)
	if err != nil || second.Changed {
		t.Fatal("managed layout migration was not idempotent", second, err)
	}
}
