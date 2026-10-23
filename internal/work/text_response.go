package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

const textResponseFormat = 1

type textResponseFenced struct{ error }

func (failure textResponseFenced) Unwrap() error { return failure.error }

type TextResponseBlock struct {
	Version    int    `json:"version"`
	ActionHash string `json:"action_hash"`
	JournalID  string `json:"journal_id"`
	Phase      string `json:"phase"`
	TaskID     string `json:"task_id,omitempty"`
	State      string `json:"state"`
	CreatedAt  int64  `json:"created_at"`
	ResolvedAt int64  `json:"resolved_at,omitempty"`
}

func phaseOutcomes(phase string) []string {
	switch phase {
	case "onboarding":
		return []string{"reviewed"}
	case "scope", "acceptance", "preflight":
		return []string{"clear", "blocked", "paused"}
	case "message", "attention":
		return []string{"responded"}
	case "research":
		return []string{"researched"}
	case domain.ExecutionRun:
		return []string{"completed", "paused", "failed"}
	case domain.VerificationRun:
		return []string{"reviewed", "paused", "failed"}
	default:
		return nil
	}
}

func phasePrompt(phase string) (string, error) {
	outcomes := phaseOutcomes(phase)
	if len(outcomes) == 0 {
		return "", domain.Fail(domain.Internal, "controller supplied an unsupported work phase")
	}
	instruction := fmt.Sprintf("FDN_PHASE_CONTRACT\nCurrent phase: %s. The JSON outcome MUST be exactly one of: %s. Other outcomes in the general response format belong to different phases and are invalid here.\n", phase, strings.Join(outcomes, ", "))
	if phase == "scope" || phase == "acceptance" || phase == "preflight" {
		instruction += "This is tool-disabled preparation, NOT execution or verification. Use clear only after reviewing the supplied scope/criteria and finding no unresolved blocker; otherwise use blocked or paused with useful questions. Do NOT return reviewed or completed, do not claim execution, and do not invent task approval.\n"
	}
	return instruction, nil
}

func validateTextPhase(launch Launch, reply provider.Reply) error {
	allowed := phaseOutcomes(launch.Phase)
	if len(allowed) == 0 || launch.Phase == domain.ExecutionRun || launch.Phase == domain.VerificationRun {
		return domain.Fail(domain.Internal, "text invocation has no supported tool-disabled phase")
	}
	if reply.Decision != "" || !slices.Contains(allowed, reply.Outcome) {
		return domain.Fail(domain.Provider, "text response does not match phase "+launch.Phase+"; required outcome: "+strings.Join(allowed, "/"))
	}
	if slices.Contains([]string{"onboarding", "message", "attention"}, launch.Phase) && len(reply.Questions) != 0 {
		return domain.Fail(domain.Provider, "text response questions are not supported in phase "+launch.Phase)
	}
	return nil
}

func textActionHash(launch Launch) string {
	semantic := launch
	semantic.ActionKey = ""
	action := requestedAction(context.Background(), semantic)
	if action == "" {
		action = "inventory:" + launch.InventoryHash
	}
	if task := launch.Task; task != nil {
		action += fmt.Sprintf(":%s:%d:%d:%d:%s", task.ID, task.SpecGeneration, task.DiscussionGeneration, task.ResultGeneration, task.LastRunID)
	}
	epoch := int64(0)
	if launch.Project != nil {
		epoch = launch.Project.Epoch
	}
	return domain.Hash(fmt.Sprintf("text-v1:%s:%s:%d", launch.Phase, action, epoch))
}

func (e *Engine) textResponseBlocks(ctx context.Context) ([]TextResponseBlock, error) {
	values, err := e.State.TextResponseValues(ctx)
	if err != nil {
		return nil, err
	}
	blocks := []TextResponseBlock{}
	for _, value := range values {
		var block TextResponseBlock
		if err := json.Unmarshal(value, &block); err != nil {
			return nil, err
		}
		if block.Version != textResponseFormat || !domain.ValidHash(block.ActionHash) || !domain.ValidOperation(block.JournalID) {
			return nil, domain.Fail(domain.Prerequisite, "unsupported retained text-response fence; preserve state for an authorized operator")
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (e *Engine) textResponseBlocked(ctx context.Context, c config.Worker, launch Launch) error {
	hash := textActionHash(launch)
	var block TextResponseBlock
	found, err := e.State.Get(ctx, "text-response:"+hash, &block)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if block.Version != textResponseFormat || block.ActionHash != hash || !domain.ValidOperation(block.JournalID) {
		return domain.Fail(domain.Prerequisite, "unsupported retained text-response fence; preserve state for an authorized operator")
	}
	if block.State == "retry-authorized" {
		return nil
	}
	if block.State != "blocked" {
		return domain.Fail(domain.Prerequisite, "unknown retained text-response fence state")
	}
	_, helpErr := e.requestPermission(ctx, c, launch.Task, "text-response:"+block.JournalID, "provider-response")
	return textResponseFenced{errors.Join(domain.Fail(domain.Prerequisite, "invalid phase response retained in "+block.JournalID+"; exact local work recover is required before repeating this review"), helpErr, e.blockHealth(ctx, "work-prerequisite"))}
}

func (e *Engine) rejectTextResponse(ctx context.Context, journal *local.Journal, launch Launch, cause error) error {
	if journal.State != "reply-prepared" {
		return cause
	}
	block := TextResponseBlock{Version: textResponseFormat, ActionHash: textActionHash(launch), JournalID: journal.ID, Phase: launch.Phase,
		State: "blocked", CreatedAt: e.Now().UTC().Unix()}
	if launch.Task != nil {
		block.TaskID = launch.Task.ID
	}
	journal.State, journal.TextActionHash = "reply-rejected", block.ActionHash
	journal.Problem = "provider response violated the tool-disabled phase contract; original reply retained and this action requires exact local retry recovery"
	if err := e.State.SaveTextResponse(ctx, *journal, block.ActionHash, block); err != nil {
		return errors.Join(cause, err)
	}
	c, err := e.currentConfig()
	if err != nil {
		return errors.Join(cause, err)
	}
	_, helpErr := e.requestPermission(ctx, c, launch.Task, "text-response:"+journal.ID, "provider-response")
	return errors.Join(cause, helpErr)
}

func (e *Engine) recoverTextResponse(ctx context.Context, journal *local.Journal) error {
	if !domain.ValidHash(journal.TextActionHash) {
		return domain.Fail(domain.Prerequisite, "rejected text response has no exact retry fence")
	}
	var block TextResponseBlock
	found, err := e.State.Get(ctx, "text-response:"+journal.TextActionHash, &block)
	if err != nil {
		return err
	}
	if !found || block.Version != textResponseFormat || block.ActionHash != journal.TextActionHash || block.JournalID != journal.ID || block.State != "blocked" {
		return domain.Fail(domain.Conflict, "exact rejected text action changed; retained evidence was not overwritten")
	}
	block.State, block.ResolvedAt = "retry-authorized", e.Now().UTC().Unix()
	journal.State, journal.Problem = "abandoned", "operator confirmed stopped processes and explicitly authorized a fresh tool-disabled review; rejected reply retained"
	if err := e.State.SaveTextResponse(ctx, *journal, block.ActionHash, block); err != nil {
		return err
	}
	return e.resolvePermission(ctx, "text-response:"+journal.ID, "provider-response")
}
