package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
)

// Sentinel errors for recoverable situations that should not cause infinite re-queue.
var (
	errExistingPR = errors.New("existing open PR")
	errMergedPR   = errors.New("existing merged PR")
	errNoChanges  = errors.New("no changes")
)

// DefaultCodingSystemPrompt is the system prompt used for daemon-managed coding sessions
// when no custom system_prompt is configured in the workflow. It tells Claude to focus on
// coding and explicitly NOT attempt remote git operations (push, PR creation, etc.)
// since those are handled by the daemon workflow.
const DefaultCodingSystemPrompt = `You are an autonomous coding agent working on a task.

FOCUS: Write code, tests, and commit your changes locally.

DO NOT:
- Push branches or create pull requests — the system handles this automatically after you finish
- Run "git push", "gh pr create", or any remote git operations
- Look for or use push_branch, create_pr, or similar tools
- Attempt to find git credentials or authenticate with GitHub

WORKFLOW:
1. Read and understand the task
2. Implement the changes with clean, well-tested code
3. Run relevant tests locally to verify your changes work (quick tests only — the full CI suite runs after push)
4. Commit your changes locally with a clear commit message
5. Stop when the implementation is complete — the system will handle pushing and PR creation

TESTING — TWO-PHASE APPROACH:
- Run relevant unit tests locally to catch obvious issues before committing
- Do NOT try to run the entire CI pipeline locally — CI handles the full test suite after push
- If CI fails later, you may be resumed with failure logs to fix specific issues

CONTAINER ENVIRONMENT:
You are running inside a Docker container with the project's toolchain pre-installed.
- If a build or test command fails with a signal (segfault, SIGBUS, signal: killed),
  retry the command up to 2 times — the failure is likely transient due to container resource constraints.`

// codingAction implements the ai.code action.
type codingAction struct {
	daemon *Daemon
}

// Execute creates a session and starts a Claude worker for the work item.
func (a *codingAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.startCoding(ctx, item); err != nil {
		if errors.Is(err, errExistingPR) {
			// Branch has an open PR from a previous attempt — skip coding,
			// advance to open_pr which will detect the existing PR.
			return workflow.ActionResult{Success: true}
		}
		if errors.Is(err, errMergedPR) {
			// Branch already merged — close the issue and skip to done.
			d.closeIssueGracefully(ctx, item)
			return workflow.ActionResult{Success: true, OverrideNext: "done"}
		}
		return workflow.ActionResult{Error: err}
	}

	return workflow.ActionResult{Success: true, Async: true}
}

// createPRAction implements the github.create_pr action.
type createPRAction struct {
	daemon *Daemon
}

// Execute creates a PR. This is a synchronous action.
func (a *createPRAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	prURL, err := d.createPR(ctx, item)
	if err != nil {
		if errors.Is(err, errNoChanges) {
			// Coding session made no changes — unqueue the issue (remove label +
			// comment) but leave it open for humans to investigate.
			repoPath := d.resolveRepoPath(ctx, item)
			label := d.resolveQueueLabel(repoPath)
			d.unqueueIssue(ctx, item, fmt.Sprintf("The coding session made no changes. Removing from the queue — re-add the '%s' label if this still needs work.", label))
			return workflow.ActionResult{Success: true, OverrideNext: "done"}
		}
		return workflow.ActionResult{Error: fmt.Errorf("PR creation failed: %w", err)}
	}

	return workflow.ActionResult{
		Success: true,
		Data:    map[string]any{"pr_url": prURL},
	}
}

// pushAction implements the github.push action.
type pushAction struct {
	daemon *Daemon
}

// Execute pushes changes. This is a synchronous action.
func (a *pushAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.pushChanges(ctx, item); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("push failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// mergeAction implements the github.merge action.
type mergeAction struct {
	daemon *Daemon
}

// Execute merges the PR. This is a synchronous action.
func (a *mergeAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.mergePR(ctx, item); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("merge failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// commentIssueAction implements the github.comment_issue action.
type commentIssueAction struct {
	daemon *Daemon
}

// Execute posts a comment on the GitHub issue for the work item.
func (a *commentIssueAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.commentOnIssue(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("issue comment failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// commentPRAction implements the github.comment_pr action.
type commentPRAction struct {
	daemon *Daemon
}

// Execute posts a comment on the PR for the work item.
func (a *commentPRAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.commentOnPR(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("PR comment failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// asanaCommentAction implements the asana.comment action.
type asanaCommentAction struct {
	daemon *Daemon
}

// Execute posts a comment on the Asana task for the work item.
func (a *asanaCommentAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.commentViaProvider(ctx, item, ac.Params, issues.SourceAsana); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("asana comment failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// linearCommentAction implements the linear.comment action.
type linearCommentAction struct {
	daemon *Daemon
}

// Execute posts a comment on the Linear issue for the work item.
func (a *linearCommentAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.commentViaProvider(ctx, item, ac.Params, issues.SourceLinear); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("linear comment failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// addLabelAction implements the github.add_label action.
type addLabelAction struct {
	daemon *Daemon
}

// Execute adds a label to the issue for the work item.
func (a *addLabelAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.addLabel(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("add label failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// removeLabelAction implements the github.remove_label action.
type removeLabelAction struct {
	daemon *Daemon
}

// Execute removes a label from the issue for the work item.
func (a *removeLabelAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.removeLabel(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("remove label failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// closeIssueAction implements the github.close_issue action.
type closeIssueAction struct {
	daemon *Daemon
}

// Execute closes the GitHub issue for the work item.
func (a *closeIssueAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.closeIssue(ctx, item); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("close issue failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// requestReviewAction implements the github.request_review action.
type requestReviewAction struct {
	daemon *Daemon
}

// Execute requests review on the PR for the work item.
func (a *requestReviewAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.requestReview(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("request review failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// fixCIAction implements the ai.fix_ci action.
type fixCIAction struct {
	daemon *Daemon
}

// Execute fetches CI failure logs and resumes the coding session to fix CI.
func (a *fixCIAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	// Check max rounds
	maxRounds := ac.Params.Int("max_ci_fix_rounds", 3)
	rounds := getCIFixRounds(item.StepData)
	if rounds >= maxRounds {
		return workflow.ActionResult{Error: fmt.Errorf("max CI fix rounds exceeded (%d/%d)", rounds, maxRounds)}
	}

	// Fetch CI failure logs
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return workflow.ActionResult{Error: fmt.Errorf("session not found")}
	}

	logs, err := fetchCIFailureLogs(ctx, sess.RepoPath, item.Branch)
	if err != nil {
		d.logger.Warn("failed to fetch CI logs, proceeding with generic message", "error", err)
		logs = "(CI failure logs unavailable)"
	}

	// Increment rounds
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.StepData["ci_fix_rounds"] = rounds + 1
		it.UpdatedAt = time.Now()
	})

	// Resume session
	if err := d.startFixCI(ctx, item, sess, rounds+1, logs); err != nil {
		return workflow.ActionResult{Error: err}
	}

	return workflow.ActionResult{Success: true, Async: true}
}

// rebaseAction implements the git.rebase action.
type rebaseAction struct {
	daemon *Daemon
}

// Execute rebases the work item's branch onto the base branch.
func (a *rebaseAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return workflow.ActionResult{Error: fmt.Errorf("session not found")}
	}

	// Check max rounds
	maxRounds := ac.Params.Int("max_rebase_rounds", 3)
	rounds := getRebaseRounds(item.StepData)
	if rounds >= maxRounds {
		return workflow.ActionResult{Error: fmt.Errorf("max rebase rounds exceeded (%d/%d)", rounds, maxRounds)}
	}

	// Refresh stale session to ensure worktree exists
	sess = d.refreshStaleSession(ctx, item, sess)

	// Determine base branch
	baseBranch := sess.BaseBranch
	if baseBranch == "" {
		baseBranch = d.gitService.GetDefaultBranch(ctx, sess.RepoPath)
	}

	// Increment rounds
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.StepData["rebase_rounds"] = rounds + 1
		it.UpdatedAt = time.Now()
	})

	// Perform the rebase
	workDir := sess.WorkTree
	if workDir == "" {
		workDir = sess.RepoPath
	}

	rebaseCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := d.gitService.RebaseBranch(rebaseCtx, workDir, item.Branch, baseBranch); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("rebase failed: %w", err)}
	}

	d.logger.Info("rebased branch successfully", "workItem", item.ID, "branch", item.Branch, "baseBranch", baseBranch, "round", rounds+1)
	return workflow.ActionResult{Success: true}
}

// resolveConflictsAction implements the ai.resolve_conflicts action.
// It merges origin/main into the feature branch (leaving conflict markers),
// then starts a Claude session to resolve the conflicts.
type resolveConflictsAction struct {
	daemon *Daemon
}

// Execute merges the base branch and starts Claude to resolve any conflicts.
func (a *resolveConflictsAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return workflow.ActionResult{Error: fmt.Errorf("session not found")}
	}

	// Check max rounds
	maxRounds := ac.Params.Int("max_conflict_rounds", 3)
	rounds := getConflictRounds(item.StepData)
	if rounds >= maxRounds {
		return workflow.ActionResult{Error: fmt.Errorf("max conflict resolution rounds exceeded (%d/%d)", rounds, maxRounds)}
	}

	// Refresh stale session to ensure worktree exists
	sess = d.refreshStaleSession(ctx, item, sess)

	// Determine base branch
	baseBranch := sess.BaseBranch
	if baseBranch == "" {
		baseBranch = d.gitService.GetDefaultBranch(ctx, sess.RepoPath)
	}

	// Abort any stale merge that may be in progress from a previous attempt
	workDir := sess.WorkTree
	if workDir == "" {
		workDir = sess.RepoPath
	}

	mergeInProgress, _ := d.gitService.IsMergeInProgress(ctx, workDir)
	if mergeInProgress {
		d.gitService.AbortMerge(ctx, workDir)
	}

	// Increment rounds
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.StepData["conflict_rounds"] = rounds + 1
		it.UpdatedAt = time.Now()
	})

	// Merge base branch into feature branch
	mergeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	conflictedFiles, err := d.gitService.MergeBaseIntoBranch(mergeCtx, workDir, baseBranch)
	if err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("merge failed: %w", err)}
	}

	// Clean merge — no conflicts to resolve
	if len(conflictedFiles) == 0 {
		d.logger.Info("merge was clean, no conflicts to resolve", "workItem", item.ID, "branch", item.Branch, "baseBranch", baseBranch)
		return workflow.ActionResult{Success: true}
	}

	// Conflicts exist — start Claude to resolve them
	if err := d.startResolveConflicts(ctx, &item, sess, rounds+1, conflictedFiles); err != nil {
		return workflow.ActionResult{Error: err}
	}

	return workflow.ActionResult{Success: true, Async: true}
}

// formatAction implements the git.format action.
type formatAction struct {
	daemon *Daemon
}

// Execute runs a formatter command in the session's worktree and commits any resulting changes.
func (a *formatAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.runFormatter(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("format failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// addressReviewAction implements the ai.address_review action.
type addressReviewAction struct {
	daemon *Daemon
}

// Execute fetches PR review comments and resumes the coding session to address them.
func (a *addressReviewAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	// Check max rounds
	maxRounds := ac.Params.Int("max_review_rounds", 3)
	rounds := getReviewRounds(item.StepData)
	if rounds >= maxRounds {
		return workflow.ActionResult{Error: fmt.Errorf("max review rounds exceeded (%d/%d)", rounds, maxRounds)}
	}

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return workflow.ActionResult{Error: fmt.Errorf("session not found")}
	}

	// Fetch review comments
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	comments, err := d.gitService.FetchPRReviewComments(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		d.logger.Warn("failed to fetch review comments, proceeding with generic message", "error", err)
	}

	// Filter out daemon transcript comments
	reviewComments := worker.FilterTranscriptComments(comments)

	// Increment rounds
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.StepData["review_rounds"] = rounds + 1
		it.UpdatedAt = time.Now()
	})

	// Resume session
	if err := d.startAddressReview(ctx, item, sess, rounds+1, reviewComments); err != nil {
		return workflow.ActionResult{Error: err}
	}

	return workflow.ActionResult{Success: true, Async: true}
}

// slackNotifyAction implements the slack.notify action.
type slackNotifyAction struct {
	daemon *Daemon
}

// Execute posts a notification message to Slack via an incoming webhook.
func (a *slackNotifyAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.sendSlackNotification(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("slack.notify failed: %w", err)}
	}

	return workflow.ActionResult{Success: true}
}

// slackTemplateData holds the variables available for slack.notify message templates.
type slackTemplateData struct {
	Title    string
	IssueID  string
	IssueURL string
	PRURL    string
	Branch   string
	Status   string
}

// slackWebhookPayload is the JSON body sent to a Slack incoming webhook.
type slackWebhookPayload struct {
	Text      string `json:"text"`
	Username  string `json:"username,omitempty"`
	IconEmoji string `json:"icon_emoji,omitempty"`
	Channel   string `json:"channel,omitempty"`
}

// sendSlackNotification posts a message to a Slack incoming webhook for a work item.
// Required params:
//   - webhook_url: Slack incoming webhook URL. Supports $ENV_VAR syntax for secret injection.
//   - message: Message text. Supports Go text/template syntax with .Title, .IssueID,
//     .IssueURL, .PRURL, .Branch, and .Status variables.
//
// Optional params:
//   - username: display name shown in Slack (defaults to "erg")
//   - icon_emoji: emoji icon, e.g. ":robot_face:" (defaults to ":robot_face:")
//   - channel: channel override, e.g. "#alerts" (defaults to webhook's configured channel)
func (d *Daemon) sendSlackNotification(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	// Resolve webhook URL — support $ENV_VAR expansion so secrets stay out of config files.
	rawURL := params.String("webhook_url", "")
	if rawURL == "" {
		return fmt.Errorf("webhook_url parameter is required")
	}
	webhookURL := os.ExpandEnv(rawURL)
	if webhookURL == "" {
		return fmt.Errorf("webhook_url resolved to empty string (env var not set?)")
	}

	// Resolve and render the message template.
	messageTemplate := params.String("message", "")
	if messageTemplate == "" {
		return fmt.Errorf("message parameter is required")
	}

	tmpl, err := template.New("slack").Parse(messageTemplate)
	if err != nil {
		return fmt.Errorf("invalid message template: %w", err)
	}

	data := slackTemplateData{
		Title:    item.IssueRef.Title,
		IssueID:  item.IssueRef.ID,
		IssueURL: item.IssueRef.URL,
		PRURL:    item.PRURL,
		Branch:   item.Branch,
		Status:   string(item.State),
	}

	var msgBuf bytes.Buffer
	if err := tmpl.Execute(&msgBuf, data); err != nil {
		return fmt.Errorf("failed to render message template: %w", err)
	}
	message := strings.TrimSpace(msgBuf.String())
	if message == "" {
		return fmt.Errorf("rendered message is empty")
	}

	payload := slackWebhookPayload{
		Text:      message,
		Username:  params.String("username", "erg"),
		IconEmoji: params.String("icon_emoji", ":robot_face:"),
		Channel:   params.String("channel", ""),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal Slack payload: %w", err)
	}

	notifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(notifyCtx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to Slack failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Slack webhook returned non-200 status: %d", resp.StatusCode)
	}

	d.logger.Info("slack notification sent", "workItem", item.ID, "channel", payload.Channel)
	return nil
}

// waitAction implements the workflow.wait action.
type waitAction struct {
	daemon *Daemon
}

// Execute pauses for the configured duration, cancelling cleanly on shutdown.
// Required params:
//   - duration: pause length as a Go duration string, e.g. "30s", "5m" (default: 0)
func (a *waitAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	duration := ac.Params.Duration("duration", 0)
	if duration <= 0 {
		return workflow.ActionResult{Success: true}
	}

	a.daemon.logger.Info("workflow.wait: pausing", "workItem", ac.WorkItemID, "duration", duration)

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		a.daemon.logger.Info("workflow.wait: pause complete", "workItem", ac.WorkItemID, "duration", duration)
		return workflow.ActionResult{Success: true}
	case <-ctx.Done():
		a.daemon.logger.Info("workflow.wait: cancelled", "workItem", ac.WorkItemID, "duration", duration)
		return workflow.ActionResult{Error: ctx.Err()}
	}
}

// webhookHTTPClient is the shared HTTP client used by webhook.post actions.
// A custom transport with explicit connection pooling is used for production robustness.
var webhookHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// webhookPostAction implements the webhook.post action.
type webhookPostAction struct {
	daemon *Daemon
}

// Execute POSTs to a configurable URL with a templated JSON body.
func (a *webhookPostAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item, ok := d.state.GetWorkItem(ac.WorkItemID)
	if !ok {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	statusCode, err := d.postWebhook(ctx, item, ac.Params)
	if err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("webhook.post failed: %w", err)}
	}

	return workflow.ActionResult{
		Success: true,
		Data:    map[string]any{"response_status": statusCode},
	}
}

// webhookTemplateData holds work item fields available for webhook body templates.
type webhookTemplateData struct {
	IssueID     string
	IssueTitle  string
	IssueURL    string
	IssueSource string
	PRURL       string
	Branch      string
	State       string
	WorkItemID  string
}

// interpolateWebhookBody renders a body template string with work item data.
// Templates use Go text/template syntax, e.g. {{.IssueID}}, {{.PRURL}}.
func interpolateWebhookBody(bodyTemplate string, data webhookTemplateData) (string, error) {
	t, err := template.New("webhook").Parse(bodyTemplate)
	if err != nil {
		return "", fmt.Errorf("invalid body template: %w", err)
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("body template execution failed: %w", err)
	}
	return buf.String(), nil
}

// postWebhook POSTs to a webhook URL with an interpolated JSON body.
// Params:
//   - url (required): destination URL
//   - body (required): JSON body template (Go text/template syntax)
//   - headers (optional): map[string]string of extra request headers
//   - timeout (optional, default 30s): request timeout duration
//   - expected_status (optional, default 200): HTTP status code considered success
func (d *Daemon) postWebhook(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) (int, error) {
	urlStr := params.String("url", "")
	if urlStr == "" {
		return 0, fmt.Errorf("url parameter is required")
	}

	bodyTemplate := params.String("body", "")
	if bodyTemplate == "" {
		return 0, fmt.Errorf("body parameter is required")
	}

	timeout := params.Duration("timeout", 30*time.Second)
	expectedStatus := params.Int("expected_status", 200)

	data := webhookTemplateData{
		IssueID:     item.IssueRef.ID,
		IssueTitle:  item.IssueRef.Title,
		IssueURL:    item.IssueRef.URL,
		IssueSource: item.IssueRef.Source,
		PRURL:       item.PRURL,
		Branch:      item.Branch,
		State:       string(item.State),
		WorkItemID:  item.ID,
	}

	body, err := interpolateWebhookBody(bodyTemplate, data)
	if err != nil {
		return 0, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, urlStr, bytes.NewBufferString(body))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if raw := params.Raw("headers"); raw != nil {
		if hdrs, ok := raw.(map[string]any); ok {
			for k, v := range hdrs {
				if vs, ok := v.(string); ok {
					req.Header.Set(k, vs)
				}
			}
		}
	}

	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("webhook POST failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != expectedStatus {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("unexpected status %d (expected %d): %s", resp.StatusCode, expectedStatus, strings.TrimSpace(string(respBody)))
	}

	return resp.StatusCode, nil
}
