package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func buildSyntheticOllamaArchive(t *testing.T, script string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	name := "ollama"
	if runtime.GOOS == "linux" {
		name = "bin/ollama"
	}
	hdr := &tar.Header{
		Name: name,
		Mode: 0755,
		Size: int64(len(script)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestLocalModelInitQuantizationCheckAndInvocation(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	pulled := map[string]bool{}
	var chatCalls []map[string]any

	ollamaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.40.16"})
		case "/api/pull":
			var req struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			pulled[req.Model] = true
			mu.Unlock()
			_, _ = w.Write([]byte("{\"status\":\"pulling manifest\"}\n{\"status\":\"success\"}\n"))
		case "/api/tags":
			mu.Lock()
			var list []map[string]any
			for name := range pulled {
				list = append(list, map[string]any{
					"name":   name,
					"model":  name,
					"digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"size":   int64(18 << 30),
				})
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
		case "/api/generate":
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "qwen3.8:27b-q8_0", "done": true})
		case "/api/chat":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			chatCalls = append(chatCalls, body)
			callIndex := len(chatCalls)
			mu.Unlock()

			if tools, hasTools := body["tools"]; hasTools && tools != nil {
				msgs, _ := body["messages"].([]any)
				hasToolResult := false
				for _, rawMsg := range msgs {
					m, _ := rawMsg.(map[string]any)
					if m["role"] == "tool" {
						hasToolResult = true
					}
				}
				if !hasToolResult {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"message": map[string]any{
							"role":    "assistant",
							"content": "",
							"tool_calls": []map[string]any{
								{
									"function": map[string]any{
										"name":      "write_file",
										"arguments": map[string]string{"path": "local-output.txt", "content": "qwen3.8 verified output\n"},
									},
								},
							},
						},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"message": map[string]any{"role": "assistant", "content": "File written."},
				})
				return
			}
			outcome := "reviewed"
			if callIndex > 1 {
				outcome = "completed"
			}
			replyJSON, _ := json.Marshal(Reply{
				Outcome:     outcome,
				Summary:     "Local Qwen 3.8 completed structured response",
				Evidence:    []string{"local model verified"},
				Questions:   []Question{},
				AnswerIDs:   []string{},
				SafeToRetry: false,
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"role": "assistant", "content": string(replyJSON)},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollamaAPI.Close()

	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"ollama version is 0.40.16\"; exit 0; fi\nif [ \"$1\" = \"--help\" ]; then echo \"Available Commands: serve pull list\"; exit 0; fi\nexit 0\n"
	archiveBytes, archiveSHA := buildSyntheticOllamaArchive(t, script)

	assetName := "ollama-darwin.tgz"
	if runtime.GOOS == "linux" {
		assetName = "ollama-linux-" + runtime.GOARCH + ".tgz"
	}
	var releaseServer *httptest.Server
	releaseServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest", "/releases/tags/v0.40.16":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":   "v0.40.16",
				"draft":      false,
				"prerelease": false,
				"assets": []map[string]any{
					{
						"name":                 assetName,
						"browser_download_url": releaseServer.URL + "/download/v0.40.16/" + assetName,
						"digest":               "sha256:" + archiveSHA,
						"size":                 len(archiveBytes),
					},
				},
			})
		case "/download/v0.40.16/" + assetName:
			_, _ = w.Write(archiveBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer releaseServer.Close()

	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	workerCfg := config.DefaultWorker(stateDir)
	mgr := New(workerCfg)
	mgr.LocalReleaseBaseURL = releaseServer.URL
	mgr.MemoryProbe = func(context.Context) (LocalMemory, error) {
		return LocalMemory{Source: "nvidia-vram", TotalBytes: 60 * (1 << 30), GPUCount: 4}, nil
	}

	// Pre-populate runtime.json pointing at our loopback test server.
	localRoot := filepath.Join(stateDir, "providers", "local")
	if err := os.MkdirAll(localRoot, 0700); err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(ollamaAPI.URL, "http://")
	if err := config.Write(filepath.Join(localRoot, "runtime.json"), localRuntimeState{Address: addr, StartedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}

	// 1. Unquantized bf16 must fail memory check on 60 GiB before any download!
	unquantSpec, err := domain.ParseLocalModelSpec("qwen3.8:27b", false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	unquantRes, err := mgr.InitializeLocalModel(ctx, unquantSpec)
	if domain.AsFault(err).Code != domain.Prerequisite || !unquantRes.MemoryInsufficient {
		t.Fatal("unquantized bf16 did not fail memory check on 60 GiB", unquantRes, err)
	}
	if _, statErr := os.Stat(filepath.Join(localRoot, "active.json")); !os.IsNotExist(statErr) {
		t.Fatal("failed memory check downloaded runtime unexpectedly")
	}

	// 2. Quantized initialization downloads verified runtime, pulls qwen3.8:27b-q8_0, and exposes authenticated catalog!
	quantSpec, err := domain.ParseLocalModelSpec("qwen3.8:27b", true, "", true)
	if err != nil {
		t.Fatal(err)
	}
	initRes, err := mgr.InitializeLocalModel(ctx, quantSpec)
	if err != nil || initRes.Model != "qwen3.8:27b-q8_0" || initRes.Quantization != "q8_0" || initRes.RuntimeVersion != "0.40.16" {
		t.Fatal("quantized local model init failed", initRes, err)
	}

	health, err := mgr.Check(ctx, "local")
	if err != nil || health.Authentication != "authenticated" || len(health.Models) != 1 || health.Models[0] != "qwen3.8:27b-q8_0" {
		t.Fatal("local provider Check did not report authenticated model", health, err)
	}
	if _, _, loginErr := mgr.LoginCommand(ctx, "local"); domain.AsFault(loginErr).Code != domain.Prerequisite {
		t.Fatal("local provider accepted interactive vendor login", loginErr)
	}

	// 3. Tool-disabled text call (onboarding) must pass format schema and zero tools.
	textReply, err := mgr.Invoke(ctx, Invocation{
		ID:       "inv_local_text",
		Provider: "local",
		Model:    "qwen3.8:27b-q8_0",
		Path:     health.Path,
		Prompt:   "Shared instructions\nFDN_CONTEXT_JSON\n{\"phase\":\"onboarding\"}",
		Coding:   false,
		Timeout:  30 * time.Second,
	})
	if err != nil || textReply.Outcome != "reviewed" {
		t.Fatal("local text invocation failed", textReply, err)
	}

	// 4. Tool-enabled coding call executes write_file inside workspace and returns structured completed Reply.
	workspace := t.TempDir()
	var recordedPID int
	codingReply, err := mgr.Invoke(ctx, Invocation{
		ID:        "inv_local_code",
		Provider:  "local",
		Model:     "qwen3.8:27b-q8_0",
		Path:      health.Path,
		Prompt:    "Shared instructions\nFDN_CONTEXT_JSON\n{\"phase\":\"execution\"}",
		Workspace: workspace,
		Coding:    true,
		Timeout:   30 * time.Second,
		Started:   func(pid int) error { recordedPID = pid; return nil },
	})
	if err != nil || codingReply.Outcome != "completed" || recordedPID <= 0 {
		t.Fatal("local coding invocation failed", codingReply, recordedPID, err)
	}
	written, err := os.ReadFile(filepath.Join(workspace, "local-output.txt"))
	if err != nil || string(written) != "qwen3.8 verified output\n" {
		t.Fatal("local coding tool did not write workspace file", string(written), err)
	}
}
