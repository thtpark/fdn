package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"golang.org/x/sys/unix"
)

func appendEnvironmentProblem(problems *[]domain.EnvironmentProblem, component, code string) {
	if len(*problems) < 16 {
		*problems = append(*problems, domain.EnvironmentProblem{Component: component, Code: code})
	}
}

func environmentProblem(o *domain.EnvironmentObservation, component, code string) {
	appendEnvironmentProblem(&o.Problems, component, code)
}

func (s *State) environmentFacts(ctx context.Context, embedded bool, overrides ...map[string]string) (domain.EnvironmentObservation, error) {
	inventory, err := s.systemInventory(ctx, embedded, overrides...)
	if err != nil {
		return domain.EnvironmentObservation{}, err
	}
	o := inventory.SystemCapabilities.environment()
	o.System.GoVersion = runtime.Version()
	collectSystemUsage(&o)
	var disk unix.Statfs_t
	if err := unix.Statfs(s.Root, &disk); err != nil {
		environmentProblem(&o, "state-filesystem-available", "os-observation-unavailable")
	} else {
		available := uint64(disk.Bavail) * uint64(disk.Bsize)
		o.Disk.AvailableBytes = &available
	}
	if o.Memory.TotalBytes != nil && (o.Memory.FreeBytes != nil && *o.Memory.FreeBytes > *o.Memory.TotalBytes ||
		o.Memory.AvailableBytes != nil && *o.Memory.AvailableBytes > *o.Memory.TotalBytes) ||
		o.Disk.TotalBytes != nil && o.Disk.AvailableBytes != nil && *o.Disk.AvailableBytes > *o.Disk.TotalBytes {
		return o, domain.Fail(domain.Prerequisite, "live usage exceeds the retained system capacity; refresh the private capacity inventory before reporting")
	}
	return o, nil
}

func (s *State) CollectEmbeddedEnvironment(ctx context.Context) (domain.EnvironmentObservation, error) {
	o, err := s.environmentFacts(ctx, true)
	if err != nil {
		return o, err
	}
	o.Executables = append(o.Executables, domain.FoundationExecutable{Name: "agent-cli", State: "embedded",
		Version: domain.Version, Commit: domain.Commit, Running: true, RunningVersion: domain.Version, RunningCommit: domain.Commit})
	return o, domain.ValidateEnvironment(o)
}

func (s *State) CollectEnvironment(ctx context.Context, runner process.Runner, overrides ...map[string]string) (domain.EnvironmentObservation, error) {
	o, err := s.environmentFacts(ctx, false, overrides...)
	if err != nil {
		return o, err
	}
	current, err := os.Executable()
	if err != nil {
		return o, err
	}
	for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
		path := filepath.Join(filepath.Dir(current), name)
		if filepath.Base(current) == name {
			path = current
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			path, err = exec.LookPath(name)
			if err != nil {
				o.Executables = append(o.Executables, domain.FoundationExecutable{Name: name, State: "missing"})
				continue
			}
		}
		executable := observeFoundationExecutable(ctx, runner, name, path)
		if filepath.Base(current) == name {
			executable.Running = true
			executable.RunningVersion, executable.RunningCommit = domain.Version, domain.Commit
		}
		if executable.State == "unavailable" {
			environmentProblem(&o, name, "binary-observation-unavailable")
		}
		o.Executables = append(o.Executables, executable)
	}
	return o, domain.ValidateEnvironment(o)
}

func observeFoundationExecutable(ctx context.Context, runner process.Runner, name, path string) domain.FoundationExecutable {
	result := domain.FoundationExecutable{Name: name, State: "unavailable"}
	file, err := os.Open(path)
	if err != nil {
		return result
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		file.Close()
		return result
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return result
	}
	result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	output := process.NewBuffer(16384)
	diagnostic := process.NewBuffer(16384)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := runner.Run(ctx, process.Spec{Path: path, Args: []string{"--version"}, Env: process.Environment(),
		Stdout: output, Stderr: diagnostic}); err != nil {
		return result
	}
	data, err := output.Bytes()
	if err != nil {
		return result
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
			API     int    `json:"api_version"`
			Schema  int    `json:"schema_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || !envelope.OK || envelope.Data.Version == "" ||
		!slices.Contains([]int{domain.APIVersion}, envelope.Data.API) || envelope.Data.Schema != domain.SchemaVersion {
		return result
	}
	result.State, result.Version, result.Commit = "observed", envelope.Data.Version, envelope.Data.Commit
	return result
}
