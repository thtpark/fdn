package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type LocalModelRecord struct {
	Family        string `json:"family"`
	Model         string `json:"model"`
	Quantization  string `json:"quantization"`
	Digest        string `json:"digest,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	InitializedAt int64  `json:"initialized_at"`
}

type localModelStore struct {
	DefaultModel string             `json:"default_model,omitempty"`
	Models       []LocalModelRecord `json:"models"`
}

type localRuntimeState struct {
	Address        string `json:"address"`
	PID            int    `json:"pid,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	StartedAt      int64  `json:"started_at"`
}

type LocalInitResult struct {
	Provider           string                      `json:"provider"`
	RuntimeVersion     string                      `json:"runtime_version,omitempty"`
	Family             string                      `json:"family"`
	Model              string                      `json:"model,omitempty"`
	Quantization       string                      `json:"quantization,omitempty"`
	Memory             LocalMemory                 `json:"memory"`
	RequiredBytes      int64                       `json:"required_bytes,omitempty"`
	Models             []string                    `json:"models,omitempty"`
	MemoryInsufficient bool                        `json:"memory_insufficient,omitempty"`
	CatalogPublished   bool                        `json:"catalog_published,omitempty"`
	PreferenceApplied  bool                        `json:"preference_applied,omitempty"`
	Preferences        *domain.ProviderPreferences `json:"preferences,omitempty"`
}

type LocalStatusInfo struct {
	Provider       string             `json:"provider"`
	Installed      bool               `json:"installed"`
	RuntimeVersion string             `json:"runtime_version,omitempty"`
	ServerRunning  bool               `json:"server_running"`
	DefaultModel   string             `json:"default_model,omitempty"`
	Memory         LocalMemory        `json:"memory"`
	Models         []LocalModelRecord `json:"models"`
}

type LocalModelInitializer interface {
	InitializeLocalModel(context.Context, domain.LocalModelSpec) (LocalInitResult, error)
}

func (m *Manager) localRoot() string {
	return filepath.Join(m.Config.StateDir, "providers", "local")
}

func (m *Manager) localModelsDir() string {
	return filepath.Join(m.localRoot(), "models")
}

func ollamaManifestPath(modelsDir, model string) (string, bool) {
	name, tag, ok := strings.Cut(model, ":")
	if !ok || name == "" || tag == "" || strings.ContainsAny(name, "\\\x00") || strings.ContainsAny(tag, "/\\\x00") {
		return "", false
	}
	parts := strings.Split(name, "/")
	if len(parts) == 1 {
		parts = []string{"library", parts[0]}
	}
	elems := append([]string{modelsDir, "manifests", "registry.ollama.ai"}, parts...)
	elems = append(elems, tag)
	return filepath.Join(elems...), true
}

func (m *Manager) readVerifiedLocalModels() (localModelStore, []string, error) {
	var store localModelStore
	err := config.Read(filepath.Join(m.localRoot(), "models.json"), &store)
	if os.IsNotExist(err) {
		return localModelStore{Models: []LocalModelRecord{}}, []string{}, nil
	}
	if err != nil {
		return store, nil, err
	}
	modelsDir := m.localModelsDir()
	_, manifestsDirErr := os.Stat(filepath.Join(modelsDir, "manifests"))
	checkManifest := manifestsDirErr == nil
	seen := map[string]bool{}
	var ready []string
	var kept []LocalModelRecord
	for _, rec := range store.Models {
		if !domain.ValidModelID(rec.Model) || seen[rec.Model] {
			continue
		}
		if checkManifest {
			manifest, ok := ollamaManifestPath(modelsDir, rec.Model)
			if !ok {
				continue
			}
			info, statErr := os.Stat(manifest)
			if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				continue
			}
		}
		seen[rec.Model] = true
		ready = append(ready, rec.Model)
		kept = append(kept, rec)
	}
	store.Models = kept
	if store.DefaultModel != "" && !seen[store.DefaultModel] {
		store.DefaultModel = ""
	}
	if store.DefaultModel == "" && len(ready) > 0 {
		store.DefaultModel = ready[0]
	}
	return store, ready, nil
}

func (m *Manager) localReadiness(_ context.Context) (bool, []string, error) {
	_, models, err := m.readVerifiedLocalModels()
	if err != nil {
		return false, nil, err
	}
	if len(models) == 0 {
		return false, nil, domain.Fail(domain.Prerequisite, "no local model weights are initialized; run agent-cli local-model init")
	}
	return true, models, nil
}

func (m *Manager) LocalStatus(ctx context.Context) (LocalStatusInfo, error) {
	mem, _ := m.ProbeLocalMemory(ctx)
	info := LocalStatusInfo{Provider: "local", Memory: mem, Models: []LocalModelRecord{}}
	store, _, err := m.readVerifiedLocalModels()
	if err != nil {
		return info, err
	}
	info.Models = store.Models
	info.DefaultModel = store.DefaultModel
	path, err := m.Resolve("local")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return info, nil
		}
		return info, err
	}
	info.Installed = true
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, verErr := m.capture(probeCtx, path, "", "--version"); verErr == nil {
		if match := versionPattern.FindSubmatch(out); len(match) == 2 {
			info.RuntimeVersion = string(match[1])
		}
	}
	var rt localRuntimeState
	if config.Read(filepath.Join(m.localRoot(), "runtime.json"), &rt) == nil && config.Loopback(rt.Address) == nil {
		if _, pingErr := probeOllamaServer(probeCtx, rt.Address); pingErr == nil {
			info.ServerRunning = true
		}
	}
	return info, nil
}

func probeOllamaServer(ctx context.Context, address string) (string, error) {
	if err := config.Loopback(address); err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "0" || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return "", domain.Fail(domain.Invalid, "local model runtime requires a non-zero loopback address")
	}
	if host == "localhost" {
		address = net.JoinHostPort("127.0.0.1", port)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+address+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", domain.Fail(domain.Unavailable, "local model server rejected version check")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	return payload.Version, nil
}

func pickLoopbackAddress() (string, error) {
	for _, candidate := range []string{"127.0.0.1:11435", "127.0.0.1:0"} {
		ln, err := net.Listen("tcp", candidate)
		if err != nil {
			continue
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return addr, nil
	}
	return "", domain.Fail(domain.Unavailable, "cannot allocate a loopback port for the local model runtime")
}

func (m *Manager) ensureLocalServer(ctx context.Context, path string) (string, error) {
	root := m.localRoot()
	if err := secure.Dir(root); err != nil {
		return "", err
	}
	modelsDir := m.localModelsDir()
	if err := secure.Dir(modelsDir); err != nil {
		return "", err
	}
	runtimeFile := filepath.Join(root, "runtime.json")
	var existing localRuntimeState
	if config.Read(runtimeFile, &existing) == nil && config.Loopback(existing.Address) == nil {
		if _, err := probeOllamaServer(ctx, existing.Address); err == nil {
			return existing.Address, nil
		}
	}
	address, err := pickLoopbackAddress()
	if err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(filepath.Join(root, "serve.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(path, "serve")
	cmd.Dir = root
	cmd.Env = process.Environment(
		"OLLAMA_HOST="+address,
		"OLLAMA_MODELS="+modelsDir,
		"OLLAMA_KEEP_ALIVE=30m",
		"OLLAMA_LOAD_TIMEOUT=15m",
		"OLLAMA_NOPRUNE=1",
		"OLLAMA_NOHISTORY=1",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return "", domain.Fail(domain.Unavailable, "failed to launch local model server")
	}
	_ = logFile.Close()
	pid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			return "", err
		}
		if _, err := probeOllamaServer(ctx, address); err == nil {
			state := localRuntimeState{
				Address:        address,
				PID:            pid,
				ExecutablePath: path,
				StartedAt:      time.Now().UTC().Unix(),
			}
			if err := config.Write(runtimeFile, state); err != nil {
				return "", err
			}
			return address, nil
		}
		select {
		case <-exited:
			return "", domain.Fail(domain.Unavailable, "local model server exited before becoming ready")
		case <-time.After(200 * time.Millisecond):
		}
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	return "", domain.Fail(domain.Unavailable, "local model server did not become ready on loopback within 15s")
}

func (m *Manager) InitializeLocalModel(ctx context.Context, spec domain.LocalModelSpec) (result LocalInitResult, err error) {
	if err := domain.ValidateLocalModelSpec(spec); err != nil {
		return result, err
	}
	result.Provider = "local"
	result.Family = spec.Family
	slot, err := secure.Acquire(filepath.Join(m.Config.StateDir, "work.lock"))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()

	mem, err := m.ProbeLocalMemory(ctx)
	if err != nil {
		return result, err
	}
	result.Memory = mem
	modelTag, quant, requiredBytes, err := domain.SelectLocalModelForMemory(spec, mem.TotalBytes)
	result.Model = modelTag
	result.Quantization = quant
	result.RequiredBytes = requiredBytes
	if err != nil {
		result.MemoryInsufficient = true
		return result, err
	}

	path, err := m.Resolve("local")
	if errors.Is(err, os.ErrNotExist) {
		client := localDownloadClient()
		_, latestVer, latestErr := ollamaReleaseMetadata(ctx, m.LocalReleaseBaseURL, client, "latest")
		if latestErr != nil {
			return result, latestErr
		}
		installed, installErr := m.installLocalRuntimeLocked(ctx, latestVer, client)
		if installErr != nil {
			return result, installErr
		}
		path = installed.Path
	} else if err != nil {
		return result, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	versionOut, verErr := m.capture(probeCtx, path, "", "--version")
	compatErr := m.localCompatibility(probeCtx, path)
	cancel()
	if verErr != nil || compatErr != nil {
		return result, errors.Join(verErr, compatErr, domain.Fail(domain.Prerequisite, "local model runtime failed version/capability verification"))
	}
	match := versionPattern.FindSubmatch(versionOut)
	if len(match) != 2 {
		return result, domain.Fail(domain.Prerequisite, "local model runtime did not report a semantic version")
	}
	result.RuntimeVersion = string(match[1])

	address, err := m.ensureLocalServer(ctx, path)
	if err != nil {
		return result, err
	}
	if err := pullOllamaModel(ctx, address, modelTag); err != nil {
		return result, err
	}
	digest, sizeBytes, err := verifyPulledOllamaModel(ctx, address, modelTag)
	if err != nil {
		return result, err
	}
	if err := preloadOllamaModel(ctx, address, modelTag); err != nil {
		return result, err
	}

	store, _, err := m.readVerifiedLocalModels()
	if err != nil {
		return result, err
	}
	now := time.Now().UTC().Unix()
	record := LocalModelRecord{
		Family:        spec.Family,
		Model:         modelTag,
		Quantization:  quant,
		Digest:        digest,
		SizeBytes:     sizeBytes,
		InitializedAt: now,
	}
	updated := false
	for i := range store.Models {
		if store.Models[i].Model == modelTag {
			store.Models[i] = record
			updated = true
			break
		}
	}
	if !updated {
		store.Models = append(store.Models, record)
	}
	store.DefaultModel = modelTag
	if err := config.Write(filepath.Join(m.localRoot(), "models.json"), store); err != nil {
		return result, err
	}
	_, models, err := m.readVerifiedLocalModels()
	if err != nil {
		return result, err
	}
	if !slices.Contains(models, modelTag) {
		return result, domain.Fail(domain.Provider, "initialized local model weights could not be verified in local storage")
	}
	result.Models = models
	return result, nil
}

func pullOllamaModel(ctx context.Context, address, model string) error {
	body, err := json.Marshal(map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return domain.Fail(domain.Unavailable, "local model weight download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return domain.Fail(domain.Unavailable, "local model runtime rejected weight pull request")
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	succeeded := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return domain.Fail(domain.Provider, "local model pull stream returned malformed JSON")
		}
		if event.Error != "" {
			return domain.Fail(domain.Unavailable, "local model pull failed: "+event.Error)
		}
		if event.Status == "success" {
			succeeded = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !succeeded {
		return domain.Fail(domain.Unavailable, "local model pull ended before reporting success")
	}
	return nil
}

func verifyPulledOllamaModel(ctx context.Context, address, model string) (string, int64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+address+"/api/tags", nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", 0, domain.Fail(domain.Unavailable, "local model catalog check failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, domain.Fail(domain.Unavailable, "local model catalog endpoint rejected check")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxBody+1))
	if err != nil {
		return "", 0, err
	}
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"models"`
	}
	if len(data) > domain.MaxBody || json.Unmarshal(data, &tags) != nil {
		return "", 0, domain.Fail(domain.Provider, "local model catalog response is malformed")
	}
	for _, entry := range tags.Models {
		if (entry.Name == model || entry.Model == model) && entry.Size > 0 {
			return entry.Digest, entry.Size, nil
		}
	}
	return "", 0, domain.Fail(domain.Provider, "pulled local model is absent from the verified runtime catalog")
}

func preloadOllamaModel(ctx context.Context, address, model string) error {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"prompt":     "",
		"keep_alive": "30m",
		"stream":     false,
	})
	if err != nil {
		return err
	}
	preloadCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(preloadCtx, http.MethodPost, "http://"+address+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return domain.Fail(domain.Unavailable, "local model GPU/memory preload failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		if payload.Error != "" {
			return domain.Fail(domain.Prerequisite, "local model failed to load into memory: "+payload.Error)
		}
		return domain.Fail(domain.Unavailable, "local model runtime rejected memory preload")
	}
	return nil
}

type ollamaChatMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func promptPhaseMeta(prompt string) (phase string, isConversation bool) {
	_, raw, ok := strings.Cut(prompt, "\nFDN_CONTEXT_JSON\n")
	if !ok {
		return "", false
	}
	var meta struct {
		Phase          string `json:"phase"`
		ConversationID string `json:"conversation_id"`
	}
	if json.Unmarshal([]byte(raw), &meta) != nil {
		return "", false
	}
	return meta.Phase, meta.ConversationID != ""
}

func localReplySchema(phase string, coding, isConversation bool) map[string]any {
	outcomes := []string{"reviewed", "clear", "blocked", "completed", "paused", "failed", "responded", "researched", "reconciled"}
	switch phase {
	case "onboarding":
		outcomes = []string{"reviewed", "blocked", "paused"}
	case "scope", "acceptance", "preflight":
		outcomes = []string{"clear", "blocked", "paused"}
	case "message", "attention":
		outcomes = []string{"responded"}
	case "request":
		outcomes = []string{"responded"}
	case "research":
		outcomes = []string{"researched", "blocked", "paused"}
	case "improvement-triage":
		outcomes = []string{"reviewed"}
	case "execution":
		outcomes = []string{"completed", "blocked", "paused", "failed"}
	case "verification":
		outcomes = []string{"reviewed", "paused", "failed"}
	case "recovery":
		outcomes = []string{"reconciled", "paused", "failed"}
	}
	props := map[string]any{
		"outcome":       map[string]any{"type": "string", "enum": outcomes},
		"summary":       map[string]any{"type": "string"},
		"evidence":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"questions":     map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"body", "respondent_ids"}, "properties": map[string]any{"body": map[string]any{"type": "string"}, "respondent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}},
		"answer_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"safe_to_retry": map[string]any{"type": "boolean"},
	}
	required := []string{"outcome", "summary", "evidence", "questions", "answer_ids", "safe_to_retry"}

	if !coding {
		props["description"] = map[string]any{"type": []string{"string", "null"}}
		props["acceptance_criteria"] = map[string]any{"type": []string{"string", "null"}}
	}
	if phase == "verification" {
		props["decision"] = map[string]any{"type": "string", "enum": []string{"accept", "rework"}}
		required = append(required, "decision")
	}
	if phase == "request" {
		props["request_decision"] = map[string]any{"type": "string", "enum": []string{"accept", "defer", "decline", "answer"}}
		required = append(required, "request_decision")
	}
	if phase == "message" || phase == "attention" {
		props["action"] = map[string]any{
			"type":                 []string{"object", "null"},
			"additionalProperties": false,
			"required":             []string{"title", "description", "acceptance_criteria", "instructions"},
			"properties": map[string]any{
				"title":               map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"acceptance_criteria": map[string]any{"type": "string"},
				"instructions":        map[string]any{"type": "string"},
				"workspace_ref":       map[string]any{"type": "string"},
				"task_id":             map[string]any{"type": "string"},
			},
		}
	}
	if phase == "improvement-triage" {
		props["improvement"] = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"decision", "reason"},
			"properties": map[string]any{
				"decision":     map[string]any{"type": "string", "enum": []string{"accept", "defer", "decline", "duplicate"}},
				"assignee_id":  map[string]any{"type": "string"},
				"duplicate_of": map[string]any{"type": "string"},
				"reason":       map[string]any{"type": "string"},
			},
		}
		required = append(required, "improvement")
	}
	if isConversation && coding {
		props["public_report"] = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"summary", "evidence"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"evidence": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		}
		required = append(required, "public_report")
	}
	if phase == "recovery" {
		props["recovery"] = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"inspection_id"},
			"properties": map[string]any{
				"inspection_id": map[string]any{"type": "string"},
				"blocker": map[string]any{
					"type":                 []string{"object", "null"},
					"additionalProperties": false,
					"required":             []string{"kind", "reference", "observation", "next_action"},
					"properties": map[string]any{
						"kind":        map[string]any{"type": "string"},
						"reference":   map[string]any{"type": "string"},
						"observation": map[string]any{"type": "string"},
						"next_action": map[string]any{"type": "string"},
					},
				},
			},
		}
		required = append(required, "recovery")
	}
	props["preferred_model"] = map[string]any{
		"type":                 []string{"object", "null"},
		"additionalProperties": false,
		"required":             []string{"provider", "model", "reason"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string", "enum": domain.ProviderNames()},
			"model":    map[string]any{"type": "string"},
			"reason":   map[string]any{"type": "string"},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           props,
	}
}

func localCodingTools() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "run_command",
				"description": "Execute a non-interactive shell command inside the authorized workspace.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"command"},
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read UTF-8 text from a workspace-relative path.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write_file",
				"description": "Write UTF-8 text to a workspace-relative path.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"path", "content"},
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_files",
				"description": "List entries in a workspace-relative directory.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

func resolveContainedWorkspacePath(workspace, rel string, allowMissingLeaf bool) (string, error) {
	if strings.TrimSpace(rel) == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return "", domain.Fail(domain.Denied, "local tool path must be relative to the authorized workspace")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(rel))
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", domain.Fail(domain.Denied, "local tool path escaped the authorized workspace")
	}
	checkPath := candidate
	if allowMissingLeaf {
		parent := candidate
		for {
			if _, statErr := os.Lstat(parent); statErr == nil {
				checkPath = parent
				break
			}
			next := filepath.Dir(parent)
			if next == parent {
				break
			}
			parent = next
		}
	}
	resolved, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", domain.Fail(domain.Denied, "local tool symlink escaped the authorized workspace")
	}
	return candidate, nil
}

func (m *Manager) runLocalCodingTool(ctx context.Context, inv Invocation, call ollamaToolCall) (string, error) {
	switch call.Function.Name {
	case "run_command":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(call.Function.Arguments, &args); err != nil || strings.TrimSpace(args.Command) == "" {
			return "error: command string is required", nil
		}
		toolCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		stdout := process.NewBuffer(64 << 10)
		stderr := process.NewBuffer(32 << 10)
		runErr := m.Runner.Run(toolCtx, process.Spec{
			Path:   "/bin/sh",
			Args:   []string{"-c", args.Command},
			Dir:    inv.Workspace,
			Env:    invocationEnvironment(m.Config, inv),
			Stdout: stdout,
			Stderr: stderr,
		})
		var cleanup *process.CleanupError
		if errors.As(runErr, &cleanup) || ctx.Err() != nil {
			return "", errors.Join(runErr, ctx.Err())
		}
		outBytes, _ := stdout.Bytes()
		errBytes, _ := stderr.Bytes()
		if runErr != nil {
			return fmt.Sprintf("exit_error: %v\nstdout:\n%s\nstderr:\n%s", runErr, string(outBytes), string(errBytes)), nil
		}
		return fmt.Sprintf("exit_code: 0\nstdout:\n%s\nstderr:\n%s", string(outBytes), string(errBytes)), nil

	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Function.Arguments, &args); err != nil {
			return "error: path string is required", nil
		}
		target, err := resolveContainedWorkspacePath(inv.Workspace, args.Path, false)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		file, err := os.Open(target)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 128<<10))
		if err != nil {
			return "error: " + err.Error(), nil
		}
		return string(data), nil

	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.Function.Arguments, &args); err != nil || strings.TrimSpace(args.Path) == "" {
			return "error: path and content are required", nil
		}
		target, err := resolveContainedWorkspacePath(inv.Workspace, args.Path, true)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "error: " + err.Error(), nil
		}
		if err := os.WriteFile(target, []byte(args.Content), 0600); err != nil {
			return "error: " + err.Error(), nil
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil

	case "list_files":
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(call.Function.Arguments, &args)
		target, err := resolveContainedWorkspacePath(inv.Workspace, args.Path, false)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		var names []string
		for i, entry := range entries {
			if i >= 256 {
				break
			}
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		return strings.Join(names, "\n"), nil

	default:
		return "error: unsupported tool " + call.Function.Name, nil
	}
}

func callOllamaChat(ctx context.Context, address string, reqBody map[string]any) (ollamaChatMessage, []byte, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return ollamaChatMessage{}, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return ollamaChatMessage{}, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ollamaChatMessage{}, nil, fmt.Errorf("provider stopped: %w", ctx.Err())
		}
		return ollamaChatMessage{}, nil, domain.Fail(domain.Provider, "local model chat request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return ollamaChatMessage{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return ollamaChatMessage{}, raw, domain.Fail(domain.Provider, "local model runtime returned an HTTP error")
	}
	var parsed struct {
		Message ollamaChatMessage `json:"message"`
		Error   string            `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Error != "" {
		return ollamaChatMessage{}, raw, domain.Fail(domain.Provider, "local model chat response is invalid")
	}
	return parsed.Message, raw, nil
}

func (m *Manager) resolveLocalContextLength(ctx context.Context, address, model string) int {
	maxTokens := 32768
	if mem, err := m.ProbeLocalMemory(ctx); err == nil {
		switch {
		case mem.TotalBytes >= 128<<30:
			maxTokens = 262144
		case mem.TotalBytes >= 64<<30:
			maxTokens = 131072
		case mem.TotalBytes >= 36<<30:
			maxTokens = 65536
		}
	}
	payload, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return maxTokens
	}
	showCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(showCtx, http.MethodPost, "http://"+address+"/api/show", bytes.NewReader(payload))
	if err != nil {
		return maxTokens
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return maxTokens
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return maxTokens
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return maxTokens
	}
	var info struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if json.Unmarshal(raw, &info) != nil || info.ModelInfo == nil {
		return maxTokens
	}
	advertised := 0
	for key, val := range info.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			if n, ok := val.(float64); ok && int(n) > advertised {
				advertised = int(n)
			}
		}
	}
	if advertised > 0 && advertised < maxTokens {
		return advertised
	}
	return maxTokens
}

func (m *Manager) invokeLocal(ctx context.Context, inv Invocation, directory string) (reply Reply, err error) {
	if inv.Coding && inv.Workspace == "" {
		return reply, domain.Fail(domain.Prerequisite, "local model coding requires an authorized workspace")
	}
	store, models, err := m.readVerifiedLocalModels()
	if err != nil {
		return reply, err
	}
	if len(models) == 0 {
		return reply, domain.Fail(domain.Prerequisite, "no local model weights are initialized")
	}
	model := inv.Model
	if model == "" {
		model = store.DefaultModel
	}
	if !slices.Contains(models, model) {
		return reply, domain.Fail(domain.Prerequisite, "selected local model is not initialized on this computer")
	}

	ctx, cancel := context.WithTimeout(ctx, inv.Timeout)
	defer cancel()

	address, err := m.ensureLocalServer(ctx, inv.Path)
	if err != nil {
		return reply, err
	}
	if inv.Started != nil {
		if err := inv.Started(os.Getpid()); err != nil {
			return reply, err
		}
	}

	stdout, err := newInvocationOutput(filepath.Join(directory, "stdout.log"), 16<<20)
	if err != nil {
		return reply, err
	}
	stderr, err := newInvocationOutput(filepath.Join(directory, "stderr.log"), 256<<10)
	if err != nil {
		_, closeErr := stdout.finish()
		return reply, errors.Join(err, closeErr)
	}
	defer func() {
		_, outErr := stdout.finish()
		_, diagErr := stderr.finish()
		err = errors.Join(err, outErr, diagErr)
	}()

	phase, isConversation := promptPhaseMeta(inv.Prompt)
	schema := localReplySchema(phase, inv.Coding, isConversation)
	numCtx := m.resolveLocalContextLength(ctx, address, model)
	options := map[string]any{"num_ctx": numCtx, "temperature": 0.1}

	messages := []ollamaChatMessage{
		{
			Role:    "system",
			Content: "You are a Foundation autonomous worker agent. Obey all operating boundaries and return the exact required Foundation JSON reply.",
		},
		{
			Role:    "user",
			Content: inv.Prompt,
		},
	}

	var finalText string
	if !inv.Coding {
		msg, raw, chatErr := callOllamaChat(ctx, address, map[string]any{
			"model":    model,
			"messages": messages,
			"stream":   false,
			"think":    false,
			"format":   schema,
			"options":  options,
		})
		if len(raw) > 0 {
			_, _ = stdout.Write(append(raw, '\n'))
		}
		if chatErr != nil {
			return reply, chatErr
		}
		if len(msg.ToolCalls) > 0 {
			return reply, domain.Fail(domain.Provider, "local text-only invocation attempted a disabled tool call")
		}
		finalText = msg.Content
	} else {
		tools := localCodingTools()
		for step := 0; step < 16; step++ {
			msg, raw, chatErr := callOllamaChat(ctx, address, map[string]any{
				"model":    model,
				"messages": messages,
				"tools":    tools,
				"stream":   false,
				"think":    false,
				"options":  options,
			})
			if len(raw) > 0 {
				_, _ = stdout.Write(append(raw, '\n'))
			}
			if chatErr != nil {
				return reply, chatErr
			}
			messages = append(messages, msg)
			if len(msg.ToolCalls) == 0 {
				break
			}
			for _, tc := range msg.ToolCalls {
				toolOut, toolErr := m.runLocalCodingTool(ctx, inv, tc)
				if toolErr != nil {
					return reply, toolErr
				}
				messages = append(messages, ollamaChatMessage{
					Role:     "tool",
					ToolName: tc.Function.Name,
					Content:  toolOut,
				})
			}
		}
		messages = append(messages, ollamaChatMessage{
			Role:    "user",
			Content: "Now emit ONLY the final structured Foundation Reply JSON object summarizing your verified work and evidence.",
		})
		msg, raw, chatErr := callOllamaChat(ctx, address, map[string]any{
			"model":    model,
			"messages": messages,
			"stream":   false,
			"think":    false,
			"format":   schema,
			"options":  options,
		})
		if len(raw) > 0 {
			_, _ = stdout.Write(append(raw, '\n'))
		}
		if chatErr != nil {
			return reply, chatErr
		}
		finalText = msg.Content
	}

	data := bytes.TrimSpace([]byte(finalText))
	if len(data) > 256<<10 {
		return reply, domain.Fail(domain.Provider, "local model structured reply exceeds its size limit")
	}
	if err := config.Decode(data, &reply); err != nil {
		return reply, domain.Fail(domain.Provider, "local model did not return the required structured reply")
	}
	if err := reply.Validate(); err != nil {
		return reply, err
	}
	if err := secure.Create(filepath.Join(directory, "reply.json"), data); err != nil {
		return reply, err
	}
	return reply, nil
}
