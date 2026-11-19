package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/software"
)

func TestInternalRecipesMatchVersionRoleAndActualPayloadLayout(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(data))
	for _, test := range []struct {
		file, platform, profile, role string
	}{
		{"foundation-linux-amd64.json", "linux-amd64", "debian", "foundation"},
		{"agent-linux-amd64.json", "linux-amd64", "debian", "agent"},
		{"foundation-darwin-arm64.json", "darwin-arm64", "archive", ""},
	} {
		t.Run(test.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, "software", "recipes", test.file))
			if err != nil {
				t.Fatal(err)
			}
			var recipe domain.BuildRecipe
			if err := json.Unmarshal(data, &recipe); err != nil {
				t.Fatal(err)
			}
			if err := software.ValidateRecipe(recipe); err != nil || recipe.Version != version ||
				recipe.Platform != test.platform || recipe.Profile != test.profile || recipe.TimeoutSeconds != 300 {
				t.Fatal("recipe lost its exact source/version/native profile", recipe, err)
			}
			if len(recipe.Steps) != 2 || strings.Join(recipe.Steps[1].Args, " ") != "go run ./tools/package --internal-platform "+test.platform {
				t.Fatal("recipe no longer uses the separately guarded internal build path", recipe.Steps)
			}
			if test.role != "" {
				want := "dist/linux-amd64/fdn-" + test.role + "_" + version + "_amd64.deb"
				if len(recipe.Outputs) != 1 || recipe.Outputs[0].Path != want || recipe.Outputs[0].Mode != 0644 {
					t.Fatal("Debian receiver was given another role or more than one package", recipe.Outputs)
				}
				return
			}
			if len(recipe.Outputs) != len(nativeBinaries)+1 {
				t.Fatal("archive omits native binaries or required license notices", recipe.Outputs)
			}
			outputs := map[string]uint32{}
			for _, output := range recipe.Outputs {
				outputs[output.Path] = output.Mode
			}
			for _, name := range nativeBinaries {
				if outputs["bin/"+name] != 0755 {
					t.Fatal("internal archive does not match the paired updater's exact binary layout", name)
				}
			}
			if outputs["THIRD_PARTY_NOTICES"] != 0644 {
				t.Fatal("internal archive omitted notices")
			}
		})
	}
}

func TestInternalPackagingDoesNotWeakenOfficialMergedReleaseAdmission(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	if err := run(false); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary packaging no longer requires the exact official merged release event", err)
	}
	if err := runInternal("windows-amd64"); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("internal payload mode invented an unsupported target", err)
	}
}
