package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

func main() {
	args := os.Args[1:]
	if slices.Contains(args, "--help") {
		fmt.Println("--available-tools --allow-all --disable-builtin-mcps --no-custom-instructions --no-ask-user --no-auto-update --silent --dangerously-bypass-approvals-and-sandbox --ask-for-approval --output-last-message --ephemeral --sandbox --skip-git-repo-check")
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
	if slices.Contains(args, "login") {
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
	reply := provider.Reply{Outcome: "clear", Summary: "fixture reviewed the exact supplied context", Evidence: []string{}, Questions: []provider.Question{}, AnswerIDs: []string{}}
	switch launch.Phase {
	case "onboarding":
		reply.Outcome = "reviewed"
	case "message", "attention":
		reply.Outcome = "responded"
		reply.Summary = "fixture scoped reply"
	case "research":
		reply.Outcome = "researched"
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
		name := "copilot"
		if slices.Contains(args, "exec") {
			name = "codex"
			if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
				panic("coding permission policy missing")
			}
		} else if !slices.Contains(args, "--allow-all") {
			panic("coding approvals missing")
		}
		count, err := os.OpenFile("invocations.count", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		check(err)
		_, err = fmt.Fprintln(count, name)
		check(err)
		check(count.Close())
		check(os.WriteFile("started", []byte(strconv.Itoa(os.Getpid())), 0600))
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
		if launch.Task.Title == "pause" {
			reply.Questions = []provider.Question{{Body: "Confirm the remaining scope before continuing.", RespondentIDs: []string{launch.Project.OwnerID}}}
			reply.Checkpoint = "fixture-partial"
		}
	default:
		panic("unknown launch phase")
	}
	response, err := json.Marshal(reply)
	check(err)
	if index := slices.Index(args, "--output-last-message"); index >= 0 {
		check(os.WriteFile(args[index+1], response, 0600))
	}
	_, err = os.Stdout.Write(response)
	check(err)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
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
			result = map[string]any{"isAuthenticated": true}
		case "models.list":
			result = map[string]any{"models": []map[string]string{{"id": "fixture-model"}}}
		default:
			panic("unapproved RPC")
		}
		response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		check(err)
		_, err = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(response), response)
		check(err)
	}
}
