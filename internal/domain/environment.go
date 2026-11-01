package domain

import (
	"slices"
	"strings"
)

const EnvironmentVersion = 1
const EnvironmentFreshSeconds int64 = 90

type SystemEnvironment struct {
	OS           string `json:"os"`
	OSVersion    string `json:"os_version,omitempty"`
	Kernel       string `json:"kernel,omitempty"`
	Architecture string `json:"architecture"`
	GoVersion    string `json:"go_version"`
}

type CPUEnvironment struct {
	Model         string  `json:"model,omitempty"`
	LogicalCores  int     `json:"logical_cores"`
	PhysicalCores *uint64 `json:"physical_cores,omitempty"`
}

type MemoryEnvironment struct {
	TotalBytes     *uint64 `json:"total_bytes,omitempty"`
	AvailableBytes *uint64 `json:"available_bytes,omitempty"`
	FreeBytes      *uint64 `json:"free_bytes,omitempty"`
}

type DiskEnvironment struct {
	TotalBytes     *uint64 `json:"total_bytes,omitempty"`
	AvailableBytes *uint64 `json:"available_bytes,omitempty"`
}

type FoundationExecutable struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	Version        string `json:"version,omitempty"`
	Commit         string `json:"commit,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Running        bool   `json:"running"`
	RunningVersion string `json:"running_version,omitempty"`
	RunningCommit  string `json:"running_commit,omitempty"`
}

type EnvironmentProblem struct {
	Component string `json:"component"`
	Code      string `json:"code"`
}

type EnvironmentObservation struct {
	Version     int                    `json:"version"`
	ObservedAt  int64                  `json:"observed_at"`
	System      SystemEnvironment      `json:"system"`
	CPU         CPUEnvironment         `json:"cpu"`
	Memory      MemoryEnvironment      `json:"memory"`
	Disk        DiskEnvironment        `json:"disk"`
	Executables []FoundationExecutable `json:"executables"`
	Problems    []EnvironmentProblem   `json:"problems"`
}

type EnvironmentRequest struct {
	Command
	Observation EnvironmentObservation `json:"observation"`
}

type ComputerEnvironment struct {
	BindingID   string                 `json:"binding_id"`
	DeviceName  string                 `json:"device_name"`
	Generation  int64                  `json:"generation"`
	ReceivedAt  int64                  `json:"received_at"`
	Stale       bool                   `json:"stale"`
	Observation EnvironmentObservation `json:"observation"`
}

type AgentEnvironment struct {
	AgentID          string                `json:"agent_id"`
	ServerObservedAt int64                 `json:"server_observed_at"`
	Computers        []ComputerEnvironment `json:"computers"`
}

func ValidateEnvironment(o EnvironmentObservation) error {
	if o.Version != EnvironmentVersion || o.ObservedAt <= 0 || o.CPU.LogicalCores <= 0 || o.CPU.LogicalCores > 131072 ||
		!slices.Contains([]string{"linux", "darwin", "ios"}, o.System.OS) || len(o.Executables) > 4 || len(o.Problems) > 16 {
		return Fail(Invalid, "unsupported or unbounded computer environment observation")
	}
	for _, text := range []string{o.System.OSVersion, o.System.Kernel, o.System.Architecture, o.System.GoVersion, o.CPU.Model} {
		if err := CheckText(text, "public environment label", 256, false); err != nil {
			return err
		}
		if strings.ContainsAny(text, "\r\n") {
			return Fail(Invalid, "environment labels must be single-line public facts")
		}
	}
	if o.System.Architecture == "" || o.System.GoVersion == "" {
		return Fail(Invalid, "environment architecture and runtime version are required")
	}
	for _, value := range []*uint64{o.CPU.PhysicalCores, o.Memory.TotalBytes, o.Memory.AvailableBytes, o.Memory.FreeBytes, o.Disk.TotalBytes, o.Disk.AvailableBytes} {
		if value != nil && *value > 1<<60 {
			return Fail(Invalid, "environment resource count is unbounded")
		}
	}
	if o.Memory.TotalBytes != nil && o.Memory.AvailableBytes != nil && *o.Memory.AvailableBytes > *o.Memory.TotalBytes ||
		o.Memory.TotalBytes != nil && o.Memory.FreeBytes != nil && *o.Memory.FreeBytes > *o.Memory.TotalBytes ||
		o.Disk.TotalBytes != nil && o.Disk.AvailableBytes != nil && *o.Disk.AvailableBytes > *o.Disk.TotalBytes {
		return Fail(Invalid, "available resources exceed the observed total")
	}
	seen := map[string]bool{}
	for _, executable := range o.Executables {
		if !slices.Contains([]string{"agent-cli", "agent-service", "agent-identity", "fdn-service"}, executable.Name) || seen[executable.Name] ||
			!slices.Contains([]string{"observed", "running", "missing", "unavailable", "embedded"}, executable.State) ||
			(executable.SHA256 != "" && !ValidHash(executable.SHA256)) {
			return Fail(Invalid, "invalid or duplicate Foundation executable observation")
		}
		seen[executable.Name] = true
		for _, label := range []string{executable.Version, executable.Commit, executable.RunningVersion, executable.RunningCommit} {
			if err := CheckText(label, "public executable version", 128, false); err != nil {
				return err
			}
			if strings.ContainsAny(label, "/\\\r\n") {
				return Fail(Invalid, "executable observations cannot contain paths or transcripts")
			}
		}
	}
	for _, problem := range o.Problems {
		if err := CheckText(problem.Component, "environment component", 64, true); err != nil {
			return err
		}
		if err := CheckText(problem.Code, "environment problem code", 64, true); err != nil {
			return err
		}
		if strings.ContainsAny(problem.Component+problem.Code, "/\\\r\n") {
			return Fail(Invalid, "environment problems must be safe codes, never raw errors or paths")
		}
	}
	return nil
}
