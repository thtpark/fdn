package distribution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	RunName string `yaml:"run-name"`
	On      map[string]struct {
		Branches []string `yaml:"branches"`
		Types    []string `yaml:"types"`
	} `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		Name        string            `yaml:"name"`
		If          string            `yaml:"if"`
		Needs       []string          `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]any    `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestIndependentPRChecksDoNotShareNamesOrPublicationAcceptance(t *testing.T) {
	tests := loadWorkflow(t, "tests.yml")
	if tests.RunName != "Test PR #${{ github.event.pull_request.number }} into ${{ github.base_ref }} at ${{ github.event.pull_request.head.sha }}" {
		t.Fatal("acceptance run is not bound to its initiating pull request, base and exact head")
	}
	for id, job := range tests.Jobs {
		if !strings.Contains(job.Name, "${{ github.base_ref }}") {
			t.Fatal("main/release checks can overwrite each other's rollup", id)
		}
	}
}

func TestPublicationAcceptanceRejectsSuccessfulSiblingPR(t *testing.T) {
	release := loadWorkflow(t, "package.yml")
	var command, expected string
	for _, step := range release.Jobs["publish"].Steps {
		if step.Env["ACCEPTANCE_RUN"] != "" {
			command, expected = step.Run, step.Env["ACCEPTANCE_RUN"]
		}
	}
	if command == "" || expected != "Test PR #${{ github.event.pull_request.number }} into release at ${{ github.event.pull_request.head.sha }}" {
		t.Fatal("publication does not require the initiating release PR's own acceptance")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "gh"), []byte("#!/bin/sh\ncat \"$ACCEPTANCE_FIXTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	title := "Test PR #84 into release at " + head
	for _, tc := range []struct {
		name, title, source, event, status, conclusion, created, updated string
		want                                                             bool
	}{
		{"own-completed", title, head, "pull_request", "completed", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", true},
		{"sibling-main", "Test PR #83 into main at " + head, head, "pull_request", "completed", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", false},
		{"another-release-pr", "Test PR #85 into release at " + head, head, "pull_request", "completed", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", false},
		{"different-head", title, strings.Repeat("b", 40), "pull_request", "completed", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", false},
		{"other-event", title, head, "push", "completed", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", false},
		{"running", title, head, "pull_request", "in_progress", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", false},
		{"failed", title, head, "pull_request", "completed", "failure", "2026-11-03T10:01:00Z", "2026-11-03T10:05:00Z", false},
		{"before-pr", title, head, "pull_request", "completed", "success", "2026-11-03T09:59:00Z", "2026-11-03T10:05:00Z", false},
		{"after-merge", title, head, "pull_request", "completed", "success", "2026-11-03T10:01:00Z", "2026-11-03T10:11:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "runs.json")
			data, err := json.Marshal(map[string]any{"workflow_runs": []map[string]string{{
				"display_title": tc.title, "head_sha": tc.source, "event": tc.event, "status": tc.status,
				"conclusion": tc.conclusion, "created_at": tc.created, "updated_at": tc.updated,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", command)
			cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
				"ACCEPTANCE_FIXTURE="+fixture, "GITHUB_REPOSITORY=fixture/fdn", "RELEASE_HEAD="+head,
				"ACCEPTANCE_RUN="+title, "PR_CREATED=2026-11-03T10:00:00Z", "PR_MERGED=2026-11-03T10:10:00Z")
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("acceptance=%v want=%v: %s (%v)", err == nil, tc.want, output, err)
			}
		})
	}
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var value workflow
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.On) != 1 {
		t.Fatal("extra workflow trigger")
	}
	if value.Permissions["contents"] != "read" {
		t.Fatal("excessive checkout permissions")
	}
	pin := regexp.MustCompile(`^[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+@[0-9a-f]{40}$`)
	for _, job := range value.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !pin.MatchString(step.Uses) {
				t.Fatal("mutable action reference", step.Uses)
			}
		}
	}
	return value
}

func TestWorkflowTriggersAndIndependentSuites(t *testing.T) {
	tests := loadWorkflow(t, "tests.yml")
	pr := tests.On["pull_request"]
	if !reflect.DeepEqual(pr.Branches, []string{"main", "release"}) || !reflect.DeepEqual(pr.Types, []string{"opened", "synchronize", "reopened", "ready_for_review"}) {
		t.Fatal("main/release PR tests have incorrect trigger")
	}
	for job, suite := range map[string]string{"static": "static", "unit": "unit", "integration": "integration", "end-to-end": "e2e", "stress": "stress"} {
		value, exists := tests.Jobs[job]
		if !exists {
			t.Fatal("missing independent suite", job)
		}
		lockedCompiler := false
		for _, step := range tests.Jobs["static"].Steps {
			if strings.Contains(step.Run, "protoc-36.2-linux-x86_64.zip") &&
				strings.Contains(step.Run, "121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278") &&
				strings.Contains(step.Run, "sha256sum --check --strict") {
				lockedCompiler = true
			}
		}
		if !lockedCompiler {
			t.Fatal("static acceptance does not install its verified locked protobuf compiler")
		}
		found := false
		for _, step := range value.Steps {
			if step.Run == "bash scripts/test.sh "+suite {
				found = true
			}
			if strings.Contains(step.Run, "tools/package") {
				t.Fatal("test workflow packages artifacts")
			}
		}
		if !found {
			t.Fatal("suite is not independently invoked", suite)
		}
	}
	release := loadWorkflow(t, "package.yml")
	pr = release.On["pull_request"]
	if !reflect.DeepEqual(pr.Branches, []string{"release"}) || !reflect.DeepEqual(pr.Types, []string{"closed"}) || len(release.Jobs) != 2 {
		t.Fatal("release workflow has extra paths")
	}
	job := release.Jobs["package"]
	if job.If != "github.event.pull_request.merged == true && github.event.pull_request.base.ref == 'release'" {
		t.Fatal("package job not merged-only")
	}
	exactCheckout, guardedBuild := false, false
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["ref"] == "${{ github.event.pull_request.merge_commit_sha }}" {
			exactCheckout = true
		}
		if step.Run == "go run ./tools/package" {
			guardedBuild = true
		}
	}
	if !exactCheckout || !guardedBuild {
		t.Fatal("packaging does not bind the merged source and runtime event guard")
	}
	publication := release.Jobs["publish"]
	if publication.If != job.If || !reflect.DeepEqual(publication.Needs, []string{"package"}) ||
		publication.Permissions["contents"] != "write" || publication.Permissions["actions"] != "read" {
		t.Fatal("durable release publication is not bound to all successful native packages and its narrow permission scope")
	}
	var commands string
	for _, step := range publication.Steps {
		commands += step.Run + "\n"
	}
	for _, required := range []string{"go run ./tools/publish", "--target \"$RELEASE_COMMIT\"", "--draft", "--draft=false --latest", "Release already exists"} {
		if !strings.Contains(commands, required) {
			t.Fatal("release publication lost source/immutability/draft protection", required)
		}
	}
}
