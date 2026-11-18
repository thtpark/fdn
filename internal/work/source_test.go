package work

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/distribution"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func TestSourceWorkspaceStrictInputsAndLegacyOwnership(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"fresh", "legacy", "mismatch", "conflict", "symlink", "incomplete", "foreign-writable", "git-symlink", "replaced", "wrong-commit", "wrong-ref"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			source := domain.TaskSource{RepositoryID: "repository_fixture", Ref: "refs/heads/work/fixture"}
			path, err := SourceWorkspace(ctx, nil, source, root, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(path, ".git", "foundation", "source")
			legacy := filepath.Join(path, ".foundation-source")
			original, err := secure.Read(marker, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GitCommand(ctx, path, "", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "Fixture"); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "legacy", "mismatch", "symlink", "incomplete":
				if err := os.Rename(marker, legacy); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "mismatch":
				source.RepositoryID = "repository_other"
			case "conflict":
				if err := secure.Create(legacy, original); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(legacy, legacy+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(legacy+".saved", legacy); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				if err := os.Remove(filepath.Join(path, ".git", "HEAD")); err != nil {
					t.Fatal(err)
				}
			case "foreign-writable":
				if err := os.Chmod(filepath.Join(path, ".git"), 0777); err != nil {
					t.Fatal(err)
				}
			case "git-symlink":
				if err := os.Rename(filepath.Join(path, ".git"), filepath.Join(path, ".git-saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(".git-saved", filepath.Join(path, ".git")); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.WriteFile(filepath.Join(path, ".git", "foundation", "git-directory"), []byte("0:0\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-commit", "wrong-ref":
				if mode == "wrong-commit" {
					source.Commit = strings.Repeat("a", 40)
				} else {
					source.Ref = "refs/heads/work/other"
				}
				if err := os.WriteFile(marker, []byte(source.RepositoryID+"\n"+source.Commit+"\n"+source.Ref+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = SourceWorkspace(ctx, nil, source, root, "fixture")
			if mode != "fresh" && mode != "legacy" {
				if err == nil {
					t.Fatal("unsafe retained ownership was accepted")
				}
				if _, oldErr := os.Lstat(legacy); oldErr != nil {
					if _, err := os.Lstat(marker); err != nil {
						t.Fatal("original evidence lost", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := distribution.CheckTrackedBuildInputs(ctx, path); err != nil {
				t.Fatal("owned source metadata polluted strict inputs", err)
			}
			if mode == "legacy" {
				data, err := secure.Read(filepath.Join(path, ".git", "foundation", "legacy-source"), 4096)
				if err != nil || string(data) != string(original) {
					t.Fatal("legacy binding evidence changed", err)
				}
			}
			if err := os.WriteFile(filepath.Join(path, ".git", "info", "exclude"), []byte("unrelated\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "unrelated"), []byte("ignored input"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := distribution.CheckTrackedBuildInputs(ctx, path); err == nil || !strings.Contains(err.Error(), "unrelated") {
				t.Fatal("unrelated ignored input was accepted", err)
			}
		})
	}
}
