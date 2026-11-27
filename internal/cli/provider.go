package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
	"golang.org/x/term"
)

func (a *app) humanTerminal() (*os.File, *os.File, *os.File, error) {
	input, inOK := a.in.(*os.File)
	output, outOK := a.out.(*os.File)
	diagnostic, errOK := a.diagnostic.(*os.File)
	if a.json || a.nonInteractive || !inOK || !outOK || !errOK || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) || !term.IsTerminal(int(diagnostic.Fd())) {
		return nil, nil, nil, domain.Fail(domain.Prerequisite, "protected authentication/remote UI requires an explicitly invoked human terminal, without --json or --non-interactive; never send login secrets to chat")
	}
	return input, output, diagnostic, nil
}

func activeAdmittedSessionOnTarget(ctx context.Context, r *resources) bool {
	if os.Getenv("FDN_SESSION_ADMITTED") == "1" {
		return true
	}
	if r == nil || r.client == nil || r.client.Credential.AgentID == "" {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	sessions, err := api.Call[[]domain.Session](checkCtx, r.client, http.MethodGet, "/v1/sessions", nil)
	if err != nil {
		return false
	}
	targetID := r.client.Credential.AgentID
	for _, s := range sessions {
		if s.TargetID != targetID || s.State != "active" {
			continue
		}
		if s.Purpose == "authentication" || (s.Sharing() && (s.InputGranted || s.InputRequestedBy != "")) {
			return true
		}
	}
	return false
}

func publishCatalogBestEffort(ctx context.Context, r *resources) {
	if r == nil || r.client == nil || r.client.Credential.AgentID == "" {
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	observation, err := provider.CollectCatalog(pubCtx, provider.New(r.config), time.Now)
	if err != nil {
		return
	}
	req := domain.ProviderCatalogRequest{
		Command:     domain.Command{OperationID: domain.NewID("op")},
		Observation: observation,
	}
	_, _ = api.Call[domain.ComputerModelOptions](pubCtx, r.client, http.MethodPost, api.Path("agents", r.client.Credential.AgentID, "model-options"), req)
}

func readMaskedSecretOrLine(input *os.File, reader *bufio.Reader) (string, error) {
	fd := int(input.Fd())
	if term.IsTerminal(fd) {
		raw, err := term.ReadPassword(fd)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (a *app) runAntigravityLogin(ctx context.Context, r *resources, manager *provider.Manager, input, output, diagnostic *os.File, useAPIKey, useOAuth, clearKey bool) (any, error) {
	reader := bufio.NewReader(input)
	choice := ""
	switch {
	case clearKey:
		choice = "3"
	case useAPIKey:
		choice = "1"
	case useOAuth:
		choice = "2"
	default:
		_, _ = fmt.Fprintln(output, "Antigravity authentication (protected human terminal)")
		_, _ = fmt.Fprintln(output, "  1) Configure GEMINI_API_KEY (recommended — unlocks Gemini 4 Carbon, Neon, Argon, Hydrogen)")
		_, _ = fmt.Fprintln(output, "  2) Sign in with Google OAuth (agy acp --login)")
		_, _ = fmt.Fprintln(output, "  3) Clear saved GEMINI_API_KEY")
		_, _ = fmt.Fprint(output, "Select [1/2/3, default 1]: ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		choice = strings.TrimSpace(line)
		if choice == "" {
			choice = "1"
		}
	}

	switch choice {
	case "1":
		_, _ = fmt.Fprint(output, "Enter GEMINI_API_KEY (input hidden): ")
		secret, err := readMaskedSecretOrLine(input, reader)
		_, _ = fmt.Fprintln(output)
		if err != nil {
			return nil, err
		}
		key, err := provider.ValidateGeminiAPIKey(secret)
		if err != nil {
			return nil, err
		}
		verifyCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		verifyErr := provider.VerifyGeminiAPIKey(verifyCtx, nil, key)
		cancel()
		if verifyErr != nil && domain.AsFault(verifyErr).Code == domain.Denied {
			return nil, verifyErr
		}
		if err := provider.SaveAntigravityAPIKey(r.config.StateDir, key); err != nil {
			return nil, err
		}
		checkCtx, cancelCheck := context.WithTimeout(ctx, 15*time.Second)
		health, checkErr := manager.Check(checkCtx, "antigravity")
		cancelCheck()
		if checkErr != nil {
			return map[string]bool{"login_command_finished": true, "authentication_validated": false}, checkErr
		}
		validated := health.Authentication == "authenticated"
		publishCatalogBestEffort(ctx, r)
		_, _ = fmt.Fprintf(output, "Saved GEMINI_API_KEY privately. Antigravity status: %s (%d models discovered).\n", health.Authentication, len(health.Models))
		return map[string]bool{"login_command_finished": true, "authentication_validated": validated}, nil

	case "2":
		path, arguments, err := manager.LoginCommand(ctx, "antigravity")
		if err != nil {
			return nil, err
		}
		err = (process.Native{}).Run(ctx, process.Spec{Path: path, Args: arguments, Dir: r.config.StateDir, Env: process.Environment(), Stdin: input, Stdout: output, Stderr: diagnostic, Terminal: input})
		if err == nil {
			publishCatalogBestEffort(ctx, r)
		}
		return map[string]bool{"login_command_finished": err == nil, "authentication_validated": false}, err

	case "3":
		if err := provider.ClearAntigravityAPIKey(r.config.StateDir); err != nil {
			return nil, err
		}
		publishCatalogBestEffort(ctx, r)
		_, _ = fmt.Fprintln(output, "Cleared saved GEMINI_API_KEY.")
		return map[string]bool{"login_command_finished": true, "authentication_validated": false}, nil

	default:
		return nil, domain.Fail(domain.Invalid, "choose 1 (GEMINI_API_KEY), 2 (Google OAuth), or 3 (clear saved key)")
	}
}

func (a *app) providerCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "provider command required")
	}
	if args[0] == "exec" {
		return a.executeProvider(ctx, r, args[1:])
	}
	if args[0] == "link" {
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return a.linkProviders()
	}
	fs := flag.NewFlagSet("provider", flag.ContinueOnError)
	name := fs.String("provider", r.config.Provider, "codex, copilot, antigravity or local")
	version := fs.String("version", "", "exact version for installation")
	geminiAPIKey := fs.Bool("gemini-api-key", false, "prompt for GEMINI_API_KEY in the human terminal (antigravity only)")
	oauthLogin := fs.Bool("oauth", false, "use browser/OAuth sign-in flow (antigravity only)")
	clearAPIKey := fs.Bool("clear-api-key", false, "remove saved GEMINI_API_KEY (antigravity only)")
	sessionAdmitted := fs.Bool("session-admitted", false, "internal flag when launched inside an admitted authentication session")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	manager := provider.New(r.config)
	switch args[0] {
	case "status":
		return manager.Check(ctx, *name)
	case "install", "update":
		selected := *version
		if args[0] == "update" {
			if selected != "" {
				return nil, domain.Fail(domain.Invalid, "update resolves the latest version; use install to pin one")
			}
			var err error
			selected, err = provider.Latest(ctx, *name, &http.Client{Timeout: 15 * time.Second})
			if err != nil {
				return nil, err
			}
		}
		if selected == "" {
			return nil, domain.Fail(domain.Invalid, "install requires --version; update explicitly selects latest")
		}
		installed, err := manager.Install(ctx, *name, selected)
		if err != nil {
			return installed, err
		}
		_, err = a.linkProviders()
		return installed, err
	case "login":
		input, output, diagnostic, err := a.humanTerminal()
		if err != nil {
			return nil, err
		}
		var slot *secure.Lock
		if !*sessionAdmitted && !activeAdmittedSessionOnTarget(ctx, r) {
			slot, err = terminalAdmission(ctx, r)
			if err != nil {
				return nil, err
			}
			defer slot.Close()
		}
		if *name == "antigravity" {
			return a.runAntigravityLogin(ctx, r, manager, input, output, diagnostic, *geminiAPIKey, *oauthLogin, *clearAPIKey)
		}
		if *geminiAPIKey || *oauthLogin || *clearAPIKey {
			return nil, domain.Fail(domain.Invalid, "--gemini-api-key, --oauth, and --clear-api-key apply to --provider antigravity only")
		}
		path, arguments, err := manager.LoginCommand(ctx, *name)
		if err != nil {
			return nil, err
		}
		err = (process.Native{}).Run(ctx, process.Spec{Path: path, Args: arguments, Dir: r.config.StateDir, Env: process.Environment(), Stdin: input, Stdout: output, Stderr: diagnostic, Terminal: input})
		return map[string]bool{"login_command_finished": err == nil, "authentication_validated": false}, err
	default:
		return nil, domain.Fail(domain.Invalid, "unknown provider command")
	}
}
