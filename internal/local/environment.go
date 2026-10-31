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

func environmentProblem(o *domain.EnvironmentObservation, component, code string) {
	if len(o.Problems) < 16 {
		o.Problems = append(o.Problems, domain.EnvironmentProblem{Component: component, Code: code})
	}
}

func collectEnvironmentFacts(stateRoot string) domain.EnvironmentObservation {
	o := domain.EnvironmentObservation{Version: domain.EnvironmentVersion, ObservedAt: time.Now().UTC().Unix(),
		System:      domain.SystemEnvironment{OS: runtime.GOOS, Architecture: runtime.GOARCH, GoVersion: runtime.Version()},
		CPU:         domain.CPUEnvironment{LogicalCores: runtime.NumCPU()},
		Executables: []domain.FoundationExecutable{}, Problems: []domain.EnvironmentProblem{}}
	memory, err := physicalMemory()
	if err != nil {
		environmentProblem(&o, "memory-total", "os-observation-unavailable")
	} else {
		o.Memory.TotalBytes = &memory
	}
	collectEnvironmentSystem(&o)
	var disk unix.Statfs_t
	if err := unix.Statfs(stateRoot, &disk); err != nil {
		environmentProblem(&o, "state-filesystem", "os-observation-unavailable")
	} else {
		total, available := uint64(disk.Blocks)*uint64(disk.Bsize), uint64(disk.Bavail)*uint64(disk.Bsize)
		o.Disk.TotalBytes, o.Disk.AvailableBytes = &total, &available
	}
	return o
}

func CollectEmbeddedEnvironment(stateRoot string) (domain.EnvironmentObservation, error) {
	o := collectEnvironmentFacts(stateRoot)
	o.Executables = append(o.Executables, domain.FoundationExecutable{Name: "agent-cli", State: "embedded",
		Version: domain.Version, Commit: domain.Commit, Running: true, RunningVersion: domain.Version, RunningCommit: domain.Commit})
	return o, domain.ValidateEnvironment(o)
}

func CollectEnvironment(ctx context.Context, stateRoot string, runner process.Runner) (domain.EnvironmentObservation, error) {
	o := collectEnvironmentFacts(stateRoot)
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
