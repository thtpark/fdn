package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
	"github.com/thtpark/fdn/tests/scenario"
)

func main() {
	args := os.Args[1:]
	if slices.Contains(args, "--fixture-interactive") {
		fmt.Println("FIXTURE_TERMINAL_READY")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			panic("foreground terminal input was unavailable")
		}
		fmt.Println("FIXTURE_TERMINAL_INPUT=" + scanner.Text())
		return
	}
	if slices.Contains(args, "--help") {
		fmt.Println("--available-tools --allow-all --disable-builtin-mcps --no-custom-instructions --no-ask-user --no-auto-update --silent --dangerously-bypass-approvals-and-sandbox --ask-for-approval --json --output-last-message --ephemeral --sandbox --skip-git-repo-check")
		return
	}
	if slices.Contains(args, "--version") {
		fmt.Println("fixture-cli 1.2.3")
		return
	}
	if slices.Contains(args, "--headless") {
		serveRPC()
		return
	}
	if len(args) != 0 && args[0] == "app-server" {
		serveCodexCatalog()
		return
	}
	if slices.Contains(args, "login") {
		if slices.Contains(args, "status") {
			if fixtureNeedsLogin() {
				os.Exit(1)
			}
		} else if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".fixture-login-required")); err != nil && !os.IsNotExist(err) {
			check(err)
		}
		return
	}
	prompt, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	check(err)
	_, raw, ok := strings.Cut(string(prompt), "\nFDN_CONTEXT_JSON\n")
	if !ok {
		panic("missing shared prompt context")
	}
	var launch work.Launch
	check(json.Unmarshal([]byte(raw), &launch))
	if response, scripted, err := scenario.RunScript(os.Getenv("HOME"), launch, args); scripted {
		check(err)
		writeResponse(args, response)
		return
	}
	reply := provider.Reply{Outcome: "clear", Summary: "fixture reviewed the exact supplied context", Evidence: []string{}, Questions: []provider.Question{}, AnswerIDs: []string{}}
	switch launch.Phase {
	case "onboarding":
		reply.Outcome = "reviewed"
	case "message", "attention":
		reply.Outcome = "responded"
		reply.Summary = "fixture scoped reply"
		if launch.Message != nil && strings.HasPrefix(launch.Message.Body, "fixture-conversation-action ") {
			reply.Action = &provider.ConversationAction{
				Title:              "Write and verify the requested fixture result",
				Description:        "Read the applicable skill and relevant context, write the requested result, and check the saved contents.",
				AcceptanceCriteria: "Verify the actual output and deliver checked results to the source conversation.",
				Instructions:       "Read the matching fixture skill and relevant thread context, perform the local action, and report actual checks.",
				WorkspaceRef:       strings.TrimPrefix(launch.Message.Body, "fixture-conversation-action "),
			}
		}
	case "research":
		reply.Outcome = "researched"
	case "recovery":
		if launch.Task == nil || launch.Recovery == nil || launch.Recovery.SourceRun.ID != launch.Task.LastRunID {
			panic("recovery omitted its exact interrupted execution")
		}
		name := codingProvider(args)
		count, err := os.OpenFile("recovery.count", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		check(err)
		_, err = fmt.Fprintln(count, name)
		check(err)
		check(count.Close())
		previous, err := os.ReadFile("invocations.count")
		check(err)
		if strings.Count(string(previous), "\n") != 1 {
			panic("recovery repeated the original execution")
		}
		reply.Outcome = "paused"
		reply.Summary = "The failed provider has no final effect result; retained evidence needs review before another execution."
		reply.Evidence = []string{"Original invocation count inspected; no execution repeated."}
		request := work.RecoveryInspectionRequest{
			TaskID: launch.Task.ID, SourceRunID: launch.Recovery.SourceRun.ID, Paths: []string{"invocations.count"},
		}
		if launch.Task.Title == "recoverable" {
			content, err := os.ReadFile("partial.txt")
			check(err)
			if string(content) != "verified fixture partial result\n" {
				panic("recovery did not inspect actual retained progress")
			}
			reply.Outcome, reply.SafeToRetry = "reconciled", true
			reply.Summary = "Verified retained local output; original fixture work can continue without repeating an external effect."
			reply.Evidence = []string{"partial.txt bytes matched the retained fixture", "Original invocation count inspected"}
			request.Paths = append(request.Paths, "partial.txt")
		}
		executable, err := os.Executable()
		check(err)
		input, err := json.Marshal(request)
		check(err)
		command := exec.CommandContext(context.Background(), filepath.Join(filepath.Dir(executable), "agent-cli"),
			"work", "recovery-inspect", "--input", "-")
		command.Stdin = bytes.NewReader(input)
		output, err := command.CombinedOutput()
		if err != nil {
			panic("native fixture recovery inspection failed: " + string(output))
		}
		var result struct {
			OK   bool                    `json:"ok"`
			Data work.RecoveryInspection `json:"data"`
		}
		check(json.Unmarshal(output, &result))
		if !result.OK || result.Data.InspectionID == "" {
			panic("native fixture recovery inspection returned no receipt")
		}
		reply.Recovery = &provider.RecoveryResult{InspectionID: result.Data.InspectionID}
		if reply.Outcome == "paused" {
			reply.Recovery.Blocker = &provider.RecoveryBlocker{
				Kind: "unresolved-effect", Reference: "fixture-provider-operation",
				Observation: "The started provider has no authoritative final effect result.",
				NextAction:  "Inspect the original external operation result before continuing.",
			}
		}
	case "scope", "acceptance", "preflight":
		if launch.Task != nil && launch.Task.Waiting != nil && launch.Task.Waiting.Kind == "clarification" {
			for _, entry := range launch.Discussion {
				if slices.Contains(launch.Task.Waiting.QuestionIDs, entry.ParentID) && slices.Contains(launch.Task.Waiting.RequestedIDs, entry.AuthorID) {
					reply.AnswerIDs = append(reply.AnswerIDs, entry.ID)
				}
			}
			if len(reply.AnswerIDs) > 0 {
				description := launch.Task.Description + "\nDecision incorporated from actual requested replies."
				reply.Description = &description
			}
		} else if launch.Task != nil && launch.Task.Title == "clarify" && launch.Phase == "scope" && !strings.Contains(launch.Task.Description, "Decision incorporated from actual requested replies.") {
			reply.Outcome = "blocked"
			reply.Questions = []provider.Question{{Body: "Which fixture result is required?", RespondentIDs: []string{launch.Project.OwnerID}}}
		}
	case "execution":
		name := codingProvider(args)
		count, err := os.OpenFile("invocations.count", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		check(err)
		_, err = fmt.Fprintln(count, name)
		check(err)
		check(count.Close())
		check(os.WriteFile("started", []byte(strconv.Itoa(os.Getpid())), 0600))
		if launch.Task.Title == "recoverable-stopped" {
			archive, err := zip.OpenReader("source.zip")
			check(err)
			found := false
			for _, entry := range archive.File {
				if entry.Name != "nested.zip" {
					continue
				}
				reader, err := entry.Open()
				check(err)
				content, err := io.ReadAll(io.LimitReader(reader, 1<<20))
				check(err)
				check(reader.Close())
				check(os.WriteFile("nested.zip", content, 0600))
				found = true
			}
			check(archive.Close())
			if !found {
				panic("stopped fixture omitted its actual nested archive")
			}
			time.Sleep(60 * time.Second)
		}
		if launch.Task.Title == "recoverable" {
			invocations, err := os.ReadFile("invocations.count")
			check(err)
			if strings.Count(string(invocations), "\n") == 1 {
				check(os.WriteFile("partial.txt", []byte("verified fixture partial result\n"), 0600))
				fmt.Fprintln(os.Stderr, "fixture interrupted after verified local progress")
				os.Exit(42)
			}
		}
		if launch.Task.Title == "slow" || launch.Task.Title == "crash" {
			time.Sleep(60 * time.Second)
		}
		if launch.Task.Title == "fail" {
			fmt.Fprintln(os.Stderr, "SECRET_PROVIDER_DIAGNOSTIC_MUST_STAY_PRIVATE")
			os.Exit(42)
		}
		check(os.WriteFile("result.txt", []byte("verified fixture result\n"), 0600))
		reply.Outcome = "completed"
		reply.Summary = "fixture file written and locally checked"
		reply.Evidence = []string{"result.txt content matched the fixture"}
		if launch.ConversationID != "" {
			if launch.Task.ProjectID != "project_adhoc" || launch.ActionInstructions == "" || len(launch.Thread) == 0 {
				panic("conversation execution omitted its real record or context")
			}
			skill, err := os.ReadFile(filepath.Join(".agents", "skills", "fixture-action", "SKILL.md"))
			check(err)
			if !strings.Contains(string(skill), "Verify result.txt") {
				panic("applicable conversation skill was not retrieved")
			}
			result, err := os.ReadFile("result.txt")
			check(err)
			if string(result) != "verified fixture result\n" {
				panic("conversation action did not verify its actual effect")
			}
			reply.Evidence = append(reply.Evidence, "fixture-action skill read", "source message: "+launch.Message.ID)
			reply.PublicReport = &provider.PublicReport{Summary: "Wrote the requested fixture result and checked its contents.",
				Evidence: []string{"Applicable skill reviewed", "Saved result matched the expected content"}}
		}
		if launch.Task.Title == "pause" {
			reply.Questions = []provider.Question{{Body: "Confirm the remaining scope before continuing.", RespondentIDs: []string{launch.Project.OwnerID}}}
			reply.Checkpoint = "fixture-partial"
		}
	default:
		panic("unknown launch phase")
	}
	response, err := json.Marshal(reply)
	check(err)
	writeResponse(args, response)
}

func codingProvider(args []string) string {
	if slices.Contains(args, "exec") {
		if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
			panic("coding permission policy missing")
		}
		return "codex"
	}
	if !slices.Contains(args, "--allow-all") {
		panic("coding approvals missing")
	}
	return "copilot"
}

func writeResponse(args []string, response []byte) {
	if index := slices.Index(args, "--output-last-message"); index >= 0 {
		check(os.WriteFile(args[index+1], response, 0600))
	}
	_, err := os.Stdout.Write(response)
	check(err)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func fixtureNeedsLogin() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".fixture-login-required"))
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		panic(err)
	}
	return false
}

func serveRPC() {
	reader := bufio.NewReader(os.Stdin)
	for {
		length := -1
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				return
			}
			check(err)
			if line == "\r\n" {
				break
			}
			if value, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				n, err := strconv.Atoi(strings.TrimSpace(value))
				check(err)
				length = n
			}
		}
		if length < 1 || length > 1<<20 {
			panic("invalid frame")
		}
		data := make([]byte, length)
		_, err := io.ReadFull(reader, data)
		check(err)
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		check(json.Unmarshal(data, &request))
		var result any
		switch request.Method {
		case "connect":
			result = map[string]any{"protocolVersion": 3}
		case "auth.getStatus":
			result = map[string]any{"isAuthenticated": !fixtureNeedsLogin()}
		case "models.list":
			models := fixtureModels()
			catalog := make([]map[string]string, 0, len(models))
			for _, model := range models {
				catalog = append(catalog, map[string]string{"id": model})
			}
			result = map[string]any{"models": catalog}
		default:
			panic("unapproved RPC")
		}
		response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		check(err)
		_, err = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(response), response)
		check(err)
	}
}

func fixtureModels() []string {
	if plan, present, err := scenario.ReadPlan(os.Getenv("HOME")); present {
		check(err)
		if len(plan.Models) != 0 {
			return plan.Models
		}
	} else {
		check(err)
	}
	return []string{"fixture-model"}
}

func serveCodexCatalog() {
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		check(json.Unmarshal(reader.Bytes(), &request))
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"userAgent": "fdn-fixture"}
		case "initialized":
			continue
		case "model/list":
			catalog := []map[string]string{}
			for _, model := range fixtureModels() {
				catalog = append(catalog, map[string]string{"id": model, "model": model})
			}
			result = map[string]any{"data": catalog, "nextCursor": nil}
		default:
			panic("unapproved Codex RPC")
		}
		check(json.NewEncoder(os.Stdout).Encode(map[string]any{"id": request.ID, "result": result}))
	}
	check(reader.Err())
}
