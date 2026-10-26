package distribution

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	On map[string]struct {
		Branches []string `yaml:"branches"`
		Types    []string `yaml:"types"`
	} `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			Uses string         `yaml:"uses"`
			Run  string         `yaml:"run"`
			With map[string]any `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
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
	if !reflect.DeepEqual(pr.Branches, []string{"release"}) || !reflect.DeepEqual(pr.Types, []string{"closed"}) || len(release.Jobs) != 1 {
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
}
