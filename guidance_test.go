package guidance_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/cli"
	"github.com/thtpark/fdn/internal/distribution"
	"github.com/thtpark/fdn/internal/domain"
	"gopkg.in/yaml.v3"
)

func TestGuideCatalogSkillsLinksAndCurrentCommands(t *testing.T) {
	catalog, err := guidance.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	wantSkills := []string{"foundation-develop", "foundation-enroll", "foundation-install", "foundation-operations", "foundation-provider-auth", "foundation-work"}
	if !slices.Equal(catalog.Skills, wantSkills) || len(catalog.Files) != 16 || !domain.ValidHash(catalog.ContentSHA256) {
		t.Fatal("incomplete guide catalog", catalog)
	}
	link := regexp.MustCompile(`\]\(([^)]+)\)`)
	for _, file := range catalog.Files {
		data, err := guidance.Read(file.Path)
		if err != nil || domain.Hash(string(data)) != file.SHA256 {
			t.Fatal("catalog hash/content differs", file.Path, err)
		}
		if bytes.Contains(data, []byte("../foundation")) || bytes.Contains(data, []byte("/Users/thpark")) {
			t.Fatal("guide depends on a legacy/private operator checkout", file.Path)
		}
		if strings.HasSuffix(file.Path, "/SKILL.md") {
			parts := strings.SplitN(string(data), "---", 3)
			if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" || len(strings.Split(parts[2], "\n")) > 500 {
				t.Fatal("invalid skill frontmatter/body", file.Path)
			}
			var meta struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
			}
			if err := yaml.Unmarshal([]byte(parts[1]), &meta); err != nil {
				t.Fatal(file.Path, err)
			}
			if meta.Name != path.Base(path.Dir(file.Path)) || !strings.Contains(meta.Description, "Use when") || len(meta.Description) > 1024 || len(strings.Fields(meta.Description)) < 12 {
				t.Fatal("skill is not meaningfully discoverable", file.Path)
			}
		}
		for _, match := range link.FindAllStringSubmatch(string(data), -1) {
			target := strings.Trim(match[1], "<>")
			if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			resolved := path.Clean(path.Join(path.Dir(file.Path), target))
			if strings.HasPrefix(resolved, "../") || strings.HasPrefix(resolved, "/") {
				t.Fatal("documentation link escapes this repository", file.Path, target)
			}
			if _, err := os.Stat(filepath.FromSlash(resolved)); err != nil {
				t.Fatal("broken local documentation link", file.Path, target, err)
			}
		}
	}
	commands, err := guidance.Read("COMMANDS.md")
	if err != nil {
		t.Fatal(err)
	}
	for binary, entries := range map[string][]string{"agent-cli": append(append([]string{}, cli.AgentCommands...), cli.AutonomyCommands...), "fdn-service": cli.FoundationCommands, "agent-service": cli.ServiceCommands} {
		for _, entry := range append(append([]string{}, entries...), cli.GuideCommands...) {
			if !bytes.Contains(commands, []byte("`"+binary+" "+entry)) {
				t.Fatal("implemented CLI leaf is missing from standalone command documentation", binary, entry)
			}
		}
	}
	context, err := guidance.WorkContext()
	if err != nil || len(context) > 32<<10 || !strings.Contains(context, "name: foundation-work") || !strings.Contains(context, "tools remain disabled") {
		t.Fatal("bounded operational prompt context is incomplete", err)
	}
}

func TestGuideDocumentsExplicitLegacyResetCleanup(t *testing.T) {
	for file, fragments := range map[string][]string{
		"COMMANDS.md":    {"terminal legacy tmux", "kernel-confirmed pane parentage", "sessions/retired", "operator-required"},
		"SCHEMA.md":      {"*.legacy-reset.json", "pane-parent relationship", "atomic reset receipt/fence retirement"},
		"DEVELOPMENT.md": {"legacy tmux endpoints require actual bounded native cleanup", "original inode preserved"},
		"TEST_PLAN.md":   {"TestNativeFounderResetClearsLegacySession", "Ordinary automatic cleanup still refuses legacy"},
		".agents/skills/foundation-operations/SKILL.md": {"old journal alone must not strand reset", "retired evidence"},
		".agents/skills/foundation-work/SKILL.md":       {"missing old journal alone must not strand reset"},
		".agents/skills/foundation-develop/SKILL.md":    {"Explicit reset must clean recognized terminal legacy tmux endpoints"},
	} {
		data, err := guidance.Read(file)
		if err != nil {
			t.Fatal(file, err)
		}
		text := strings.Join(strings.Fields(string(data)), " ")
		for _, fragment := range fragments {
			if !strings.Contains(text, fragment) {
				t.Fatal("guide lost explicit legacy-reset cleanup contract", file, fragment)
			}
		}
	}
}

func TestGuideDocumentsCurrentUserWorkerOwnership(t *testing.T) {
	for file, fragments := range map[string][]string{
		"AGENTS.md":     {"existing installing user", "Do not create another OS user"},
		"SCHEMA.md":     {"the same existing non-root user", "worker_uid", "worker_gid", "trust boundary"},
		"COMMANDS.md":   {"existing installing user", "without sudo or new users/groups"},
		"DEPLOYMENT.md": {"local user", "without sudo", "no hidden account"},
		"README.md":     {"same user", "Windows native adapters remain unimplemented"},
		".agents/skills/foundation-install/SKILL.md":       {"existing installing user", "systemctl --user", "Only current single-user service options"},
		".agents/skills/foundation-enroll/SKILL.md":        {"existing installing user", "local worker uses its existing OS user"},
		".agents/skills/foundation-work/SKILL.md":          {"existing installing user owns all worker state"},
		".agents/skills/foundation-provider-auth/SKILL.md": {"same existing installing user", "process separation does not isolate keys"},
		".agents/skills/foundation-operations/SKILL.md":    {"existing installing user", "only current single-user service options"},
		".agents/skills/foundation-develop/SKILL.md":       {"current-user OPTIONS", "no new worker account/group"},
	} {
		data, err := guidance.Read(file)
		if err != nil {
			t.Fatal(file, err)
		}
		text := strings.Join(strings.Fields(string(data)), " ")
		for _, fragment := range fragments {
			if !strings.Contains(text, fragment) {
				t.Fatal("embedded guide lost the current-user ownership contract", file, fragment)
			}
		}
	}
	context, err := guidance.WorkContext()
	if err != nil || !strings.Contains(context, "owns all worker state") {
		t.Fatal("controller prompts did not receive the current-user ownership contract", err)
	}
}

func TestGuideDocumentsScopedNotesAndStatePreservation(t *testing.T) {
	for file, fragments := range map[string][]string{
		"AGENTS.md":    {"note context --project PROJECT_ID", "retained/in-flight journals and retry fences stay unchanged"},
		"COMMANDS.md":  {"note set", "65,536 UTF-8 bytes", "revoked delegation", "References are not automatically downloaded", "verification uses the reviewer's notes"},
		"SCHEMA.md":    {"SQL6", "Notes changes do not mutate", "Launch.shared_notes", "private local knowledge"},
		"MIGRATION.md": {"SQL5 databases upgrade transactionally", "A SQL5 binary cannot open a SQL6 database"},
		"TEST_PLAN.md": {"TestNativeScopedNotesPersistAndReachBothProviderTaskPrompts", "not live model compliance"},
		".agents/skills/foundation-work/SKILL.md":    {"shared company/project/agent notes", "note context --project PROJECT", "private `knowledge` stays separate"},
		".agents/skills/foundation-install/SKILL.md": {"SQL6 shared company/project/agent notes", "SQL5 binaries cannot read SQL6"},
	} {
		data, err := guidance.Read(file)
		if err != nil {
			t.Fatal(file, err)
		}
		text := strings.Join(strings.Fields(string(data)), " ")
		for _, fragment := range fragments {
			if !strings.Contains(strings.ToLower(text), strings.ToLower(fragment)) {
				t.Fatal("embedded guide lost the scoped-notes contract", file, fragment)
			}
		}
	}
	context, err := guidance.WorkContext()
	if err != nil || !strings.Contains(context, "note context --project") || !strings.Contains(context, "in-flight/retained journals") {
		t.Fatal("shared task prompts omitted scoped-notes guidance", err)
	}
}

func TestGuideDocumentsAutonomousSessionRecoveryWithoutAuthorityBypass(t *testing.T) {
	for file, fragments := range map[string][]string{
		"AGENTS.md":              {"target-owned and automatic", "atomically retire"},
		"SCHEMA.md":              {"kernel process start identity", "session-cleanup-result:", "30-second server-timed"},
		"COMMANDS.md":            {"Routine session cleanup is autonomous", "explicit last-resort operator"},
		"DEPLOYMENT.md":          {"Automatic session recovery before operator escalation", "unknown/legacy ownership"},
		"DEVELOPMENT.md":         {"Interactive cleanup is target-owned", "replaying inference/login"},
		"IMPLEMENTATION_PLAN.md": {"Autonomous target-owned interactive recovery", "locked payload schema"},
		".agents/skills/foundation-operations/SKILL.md": {"Routine interactive cleanup is automatic first", "1-30-second backoff"},
		".agents/skills/foundation-work/SKILL.md":       {"Routine session cleanup is autonomous", "Eligible ai work then resumes"},
	} {
		data, err := guidance.Read(file)
		if err != nil {
			t.Fatal(file, err)
		}
		text := strings.Join(strings.Fields(string(data)), " ")
		for _, fragment := range fragments {
			if !strings.Contains(text, fragment) {
				t.Fatal("guide omitted autonomous session recovery contract", file, fragment)
			}
		}
	}
	context, err := guidance.WorkContext()
	if err != nil || !strings.Contains(context, "Routine session cleanup is autonomous") || !strings.Contains(context, "unknown resources") {
		t.Fatal("runtime work guidance omitted automatic recovery or ownership limits", err)
	}
}

func TestInstallPreservesInstructionsUnrelatedSkillsAndIdempotency(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0755); err != nil {
		t.Fatal(err)
	}
	instructions := filepath.Join(home, "AGENTS.md")
	original := []byte("# Existing operator instructions\nKeep my private conventions intact.\n")
	if err := os.WriteFile(instructions, original, 0644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(home, ".agents", "skills", "unrelated")
	if err := os.MkdirAll(unrelated, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(unrelated, "SKILL.md")
	if err := os.WriteFile(marker, []byte("unrelated skill"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := guidance.Install(home)
	if err != nil || !first.Changed {
		t.Fatal(first, err)
	}
	current, err := os.ReadFile(instructions)
	if err != nil || !bytes.HasPrefix(current, original) || bytes.Count(current, []byte("<!-- foundation-guide:start -->")) != 1 {
		t.Fatal("home instructions were not preserved", err)
	}
	if info, err := os.Stat(instructions); err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("existing instruction permissions were changed", err)
	}
	catalog, err := guidance.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range first.SkillRoots {
		for _, skill := range catalog.Skills {
			file := filepath.Join(root, skill, "SKILL.md")
			data, err := os.ReadFile(file)
			if err != nil || !bytes.Contains(data, []byte("name: "+skill+"\n")) {
				t.Fatal("provider discovery link is broken", file, err)
			}
		}
	}
	stored, err := os.ReadFile(filepath.Join(first.Directory, "MANIFEST.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest guidance.Manifest
	if err := json.Unmarshal(stored, &manifest); err != nil || manifest.ContentSHA256 != first.ContentSHA256 || manifest.Version != first.Version {
		t.Fatal("installed guide provenance differs", err)
	}
	second, err := guidance.Install(home)
	if err != nil || second.Changed {
		t.Fatal("identical guide installation is not idempotent", second, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unrelated skill" {
		t.Fatal("unrelated skill changed", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".foundation", "config", "agent.json")); !os.IsNotExist(err) {
		t.Fatal("guide installation changed/created worker configuration")
	}
}

func TestInstallRefusesUnmanagedCollisionsTamperingAndSymlinks(t *testing.T) {
	for _, kind := range []string{"skill-directory", "skill-link", "instructions-link", "ambiguous-block", "guide-tamper", "guide-current"} {
		t.Run(kind, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			collision := filepath.Join(home, ".agents", "skills", "foundation-work")
			switch kind {
			case "skill-directory", "skill-link":
				if err := os.MkdirAll(filepath.Dir(collision), 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "skill-directory" {
					if err := os.Mkdir(collision, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(t.TempDir(), collision); err != nil {
					t.Fatal(err)
				}
			case "instructions-link":
				if err := os.Symlink(filepath.Join(t.TempDir(), "private"), filepath.Join(home, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			case "ambiguous-block":
				if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("<!-- foundation-guide:start -->\nunmatched existing content"), 0600); err != nil {
					t.Fatal(err)
				}
			case "guide-tamper", "guide-current":
				installed, err := guidance.Install(home)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "guide-tamper" {
					if err := os.WriteFile(filepath.Join(installed.Directory, "AGENTS.md"), []byte("user's local edits"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Remove(installed.Directory); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(t.TempDir(), installed.Directory); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := guidance.Install(home); err == nil {
				t.Fatal("unsafe guidance installation was accepted")
			}
			if kind == "guide-tamper" {
				data, err := os.ReadFile(filepath.Join(home, ".foundation", "guidance", "current", "AGENTS.md"))
				if err != nil || string(data) != "user's local edits" {
					t.Fatal("local guide edits were destroyed")
				}
			}
		})
	}
	if _, err := guidance.Install("/"); err == nil {
		t.Fatal("filesystem root accepted as HOME")
	}
	if _, err := guidance.Read("../private"); err == nil {
		t.Fatal("guide read escaped embedded content")
	}
}

func TestNativeAndBothDebianRoleGuidePayloadsMatchEmbeddedCatalog(t *testing.T) {
	catalog, err := guidance.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"guide", "usr/share/doc/fdn-foundation/guide", "usr/share/doc/fdn-agent/guide"} {
		entries, err := distribution.GuideEntries(prefix)
		if err != nil || len(entries) != len(catalog.Files)+1 {
			t.Fatal("incomplete release guide", prefix, err)
		}
		for index, file := range catalog.Files {
			if entries[index].Name != prefix+"/"+file.Path || entries[index].Mode != 0644 || domain.Hash(string(entries[index].Data)) != file.SHA256 {
				t.Fatal("package/source guide drift", entries[index].Name)
			}
		}
		var manifest guidance.Manifest
		if err := json.Unmarshal(entries[len(entries)-1].Data, &manifest); err != nil || manifest.ContentSHA256 != catalog.ContentSHA256 {
			t.Fatal("release guide manifest drift", err)
		}
	}
	if _, err := distribution.GuideEntries("../escape"); err == nil {
		t.Fatal("unsafe release guide prefix")
	}
}
