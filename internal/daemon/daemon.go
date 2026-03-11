package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/dashboard"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/manager"
	"github.com/zhubert/erg/internal/session"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
)

const (
	defaultPollInterval       = 30 * time.Second
	defaultReviewPollInterval = 60 * time.Second
	defaultReconcileInterval  = 2 * time.Minute
	autonomousFilterLabel     = "queued"
)

// Daemon is the persistent orchestrator that manages the full lifecycle of work items.
type Daemon struct {
	config          agentconfig.Config
	gitService      *git.GitService
	sessionService  *session.SessionService
	sessionMgr      *manager.SessionManager
	issueRegistry   *issues.ProviderRegistry
	state           *daemonstate.DaemonState
	lock            *daemonstate.DaemonLock
	workers         map[string]*worker.SessionWorker
	workflowConfigs map[string]*workflow.Config // keyed by repo path
	engines         map[string]*workflow.Engine // keyed by repo path
	mu              sync.Mutex
	workerDone      chan struct{} // buffered(1); workers signal when done to wake the main loop
	logger          *slog.Logger

	// Config save tracking
	configSaveFailures int
	configSavePaused   bool // true after 5+ consecutive failures; blocks new work

	// Options
	once                  bool
	repoFilter            string
	maxConcurrent         int
	maxTurns              int
	maxDuration           int
	autoAddressPRComments bool
	autoBroadcastPR       bool
	autoMerge             bool
	mergeMethod           string
	pollInterval          time.Duration
	reviewPollInterval    time.Duration
	lastReviewPollAt      time.Time
	lastReconcileAt       time.Time

	// preseededIssue is an issue to inject on the first poll tick (for erg run).
	preseededIssue *issues.Issue

	// dashboardAddr, when set, causes the daemon to start an embedded dashboard
	// server with itself as the SessionController so that control buttons work.
	dashboardAddr string

	// Docker health tracking
	dockerDown        bool
	dockerDownLogged  bool
	dockerHealthCheck func(context.Context) error // injectable for testing; nil means use default

	// Workflow
	workflowFile        string            // optional explicit workflow config file path
	repoWorkflowFiles   map[string]string // per-repo workflow file overrides (repo path → file path)
	repoContainerImages map[string]string // per-repo auto-built container images (repo path → image tag)
	daemonID            string            // stable ID for lock/state keying in multi-repo mode
}

// Option configures the daemon.
type Option func(*Daemon)

// WithOnce configures the daemon to run one tick and exit.
func WithOnce(once bool) Option {
	return func(d *Daemon) { d.once = once }
}

// WithRepoFilter limits polling to a specific repo.
func WithRepoFilter(repo string) Option {
	return func(d *Daemon) { d.repoFilter = repo }
}

// WithMaxConcurrent overrides the config's max concurrent setting.
func WithMaxConcurrent(max int) Option {
	return func(d *Daemon) { d.maxConcurrent = max }
}

// WithMaxTurns overrides the config's max autonomous turns setting.
func WithMaxTurns(max int) Option {
	return func(d *Daemon) { d.maxTurns = max }
}

// WithMaxDuration overrides the config's max autonomous duration (minutes) setting.
func WithMaxDuration(max int) Option {
	return func(d *Daemon) { d.maxDuration = max }
}

// WithAutoAddressPRComments enables auto-addressing PR review comments.
func WithAutoAddressPRComments(v bool) Option {
	return func(d *Daemon) { d.autoAddressPRComments = v }
}

// WithAutoBroadcastPR enables auto-creating PRs when broadcast group completes.
func WithAutoBroadcastPR(v bool) Option {
	return func(d *Daemon) { d.autoBroadcastPR = v }
}

// WithAutoMerge enables auto-merging PRs after review approval and CI pass.
func WithAutoMerge(v bool) Option {
	return func(d *Daemon) { d.autoMerge = v }
}

// WithMergeMethod sets the merge method (rebase, squash, or merge).
func WithMergeMethod(method string) Option {
	return func(d *Daemon) { d.mergeMethod = method }
}

// WithPreacquiredLock tells the daemon that the lock was already acquired
// by the parent process. The daemon will adopt it instead of acquiring a new one.
func WithPreacquiredLock(lock *daemonstate.DaemonLock) Option {
	return func(d *Daemon) { d.lock = lock }
}

// WithPreseededIssue injects a specific issue to be processed on the first poll
// tick, bypassing the normal issue-fetch from the provider. Intended for erg run.
func WithPreseededIssue(issue issues.Issue) Option {
	return func(d *Daemon) { d.preseededIssue = &issue }
}

// WithWorkflowFile sets an explicit workflow config file path, overriding the
// default <repo>/.erg/workflow.yaml discovery.
func WithWorkflowFile(file string) Option {
	return func(d *Daemon) { d.workflowFile = file }
}

// WithRepoWorkflowFiles sets per-repo workflow file overrides.
// Each key is a repo path (or owner/repo), and the value is the path to
// its workflow config file. This takes precedence over WithWorkflowFile.
func WithRepoWorkflowFiles(files map[string]string) Option {
	return func(d *Daemon) { d.repoWorkflowFiles = files }
}

// WithDaemonID sets a stable identifier for lock and state files.
// This is used in multi-repo mode where repoFilter may be empty.
// WithRepoContainerImages sets per-repo container image overrides.
// Used in multi-repo mode where each repo may auto-build a different image.
func WithRepoContainerImages(images map[string]string) Option {
	return func(d *Daemon) { d.repoContainerImages = images }
}

func WithDaemonID(id string) Option {
	return func(d *Daemon) { d.daemonID = id }
}

// WithDashboard starts an embedded dashboard server at addr alongside the daemon.
// The dashboard will have full control access (stop, retry, send-message).
// When addr is empty the embedded dashboard is disabled.
func WithDashboard(addr string) Option {
	return func(d *Daemon) { d.dashboardAddr = addr }
}

// New creates a new daemon.
func New(cfg agentconfig.Config, gitSvc *git.GitService, sessSvc *session.SessionService, registry *issues.ProviderRegistry, logger *slog.Logger, opts ...Option) *Daemon {
	d := &Daemon{
		config:             cfg,
		gitService:         gitSvc,
		sessionService:     sessSvc,
		sessionMgr:         manager.NewSessionManager(cfg, gitSvc),
		issueRegistry:      registry,
		workers:            make(map[string]*worker.SessionWorker),
		workerDone:         make(chan struct{}, 1),
		logger:             logger,
		autoMerge:          true, // Auto-merge is default for daemon
		pollInterval:       defaultPollInterval,
		reviewPollInterval: defaultReviewPollInterval,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Run starts the daemon's main loop. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Info("daemon starting",
		"once", d.once,
		"repoFilter", d.repoFilter,
		"daemonID", d.daemonID,
		"repos", d.config.GetRepos(),
		"maxConcurrent", d.getMaxConcurrent(),
		"maxTurns", d.getMaxTurns(),
		"maxDuration", d.getMaxDuration(),
		"autoMerge", d.autoMerge,
	)

	key := d.stateKey()

	// Acquire lock (unless pre-acquired by parent process)
	if d.lock == nil {
		lock, err := daemonstate.AcquireLock(key)
		if err != nil {
			return fmt.Errorf("failed to acquire daemon lock: %w", err)
		}
		d.lock = lock
	}
	defer d.releaseLock()

	// Clean up stale files from previous runs that were killed ungracefully
	if n, err := claude.ClearAuthFiles(); err != nil {
		d.logger.Warn("failed to clean stale auth files", "error", err)
	} else if n > 0 {
		d.logger.Info("cleaned stale auth files from previous run", "count", n)
	}
	if n, err := claude.ClearMCPConfigFiles(); err != nil {
		d.logger.Warn("failed to clean stale MCP config files", "error", err)
	} else if n > 0 {
		d.logger.Info("cleaned stale MCP config files from previous run", "count", n)
	}

	// Load or create state
	state, err := daemonstate.LoadDaemonState(key)
	if err != nil {
		// If state is for a different repo, create fresh
		d.logger.Warn("failed to load daemon state, creating new", "error", err)
		state = daemonstate.NewDaemonState(key)
	}
	d.state = state

	// Reset spend tracking so it reflects only the current daemon run.
	d.state.ResetSpend()

	// Resolve human-readable owner/repo labels from git remote URLs and persist them
	// so the dashboard can display "zhubert/erg" instead of raw filesystem paths or
	// opaque daemon IDs.
	d.resolveAndSaveRepoLabels(ctx)

	if err := d.state.Save(); err != nil {
		d.logger.Warn("failed to save state after resetting spend", "error", err)
	}

	// Start embedded dashboard server if configured.
	if d.dashboardAddr != "" {
		dashSrv := dashboard.New(d.dashboardAddr, dashboard.WithController(d))
		dashErrCh := make(chan error, 1)
		go func() {
			if err := dashSrv.Run(ctx); err != nil {
				select {
				case dashErrCh <- err:
				default:
				}
				d.logger.Warn("embedded dashboard stopped", "addr", d.dashboardAddr, "error", err)
				return
			}
			d.logger.Info("embedded dashboard stopped", "addr", d.dashboardAddr)
		}()
		// Wait briefly so that an immediate bind failure is captured before
		// declaring the dashboard started.
		select {
		case err := <-dashErrCh:
			d.logger.Warn("failed to start embedded dashboard", "addr", d.dashboardAddr, "error", err)
		case <-time.After(500 * time.Millisecond):
			d.logger.Info("embedded dashboard started", "addr", d.dashboardAddr)
		}
	}

	// Load workflow configs for all repos
	d.loadWorkflowConfigs()

	// Rebuild state from the issue tracker. This scans for active issues,
	// queries the tracker for their actual progress (PR state, CI, review),
	// and places each work item at the correct workflow step.
	d.rebuildStateFromTracker(ctx)

	// Immediate first tick
	d.tick(ctx)

	if d.once {
		d.waitForActiveWorkers(ctx)
		d.collectCompletedWorkers(ctx)
		d.saveState()
		d.logger.Info("daemon exiting (--once mode)")
		return nil
	}

	// Continuous polling loop
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("context cancelled, shutting down daemon")
			d.shutdown()
			return ctx.Err()
		case <-ticker.C:
			d.tick(ctx)
		case <-d.workerDone:
			d.tick(ctx)
		}
	}
}

// notifyWorkerDone signals the main loop that a worker has completed.
// Uses a non-blocking send so it never blocks if the channel is already full.
func (d *Daemon) notifyWorkerDone() {
	select {
	case d.workerDone <- struct{}{}:
	default:
	}
}

// tick performs one iteration of the daemon event loop.
func (d *Daemon) tick(ctx context.Context) {
	d.collectCompletedWorkers(ctx) // Always: detect finished sessions
	dockerOK := d.checkDockerHealth(ctx)
	if dockerOK {
		d.processRetryItems(ctx)     // Re-execute items whose retry delay has elapsed
		d.processIdleSyncItems(ctx)  // Execute items idle on sync task steps (e.g. after recovery)
		d.processWorkItems(ctx)      // Process active items via engine
		d.reconcileClosedIssues(ctx) // Cancel work items whose issues were closed externally
		d.pollForNewIssues(ctx)      // Find new issues (if slots available)
		d.startQueuedItems(ctx)      // Start coding on queued items
	}
	d.saveState() // Always: persist
}

// getMaxConcurrent returns the effective max concurrent limit.
func (d *Daemon) getMaxConcurrent() int {
	if d.maxConcurrent > 0 {
		return d.maxConcurrent
	}
	return d.config.GetIssueMaxConcurrent()
}

// getMaxTurns returns the effective max autonomous turns limit.
func (d *Daemon) getMaxTurns() int {
	if d.maxTurns > 0 {
		return d.maxTurns
	}
	return d.config.GetAutoMaxTurns()
}

// getMaxDuration returns the effective max autonomous duration (minutes).
func (d *Daemon) getMaxDuration() int {
	if d.maxDuration > 0 {
		return d.maxDuration
	}
	return d.config.GetAutoMaxDurationMin()
}

// getMergeMethod returns the effective merge method.
func (d *Daemon) getMergeMethod() string {
	if d.mergeMethod != "" {
		return d.mergeMethod
	}
	return d.config.GetAutoMergeMethod()
}

// getAutoAddressPRComments returns whether auto-address PR comments is enabled.
func (d *Daemon) getAutoAddressPRComments() bool {
	return d.autoAddressPRComments || d.config.GetAutoAddressPRComments()
}

// stateKey returns the key used for lock and state file paths.
// In multi-repo mode this is the daemonID; otherwise it's the repoFilter.
func (d *Daemon) stateKey() string {
	if d.daemonID != "" {
		return d.daemonID
	}
	return d.repoFilter
}

// resolveAndSaveRepoLabels resolves owner/repo display labels for all repos this daemon
// manages by reading the git remote origin URL for each local path. Failures are logged and
// fall back to the raw path so the dashboard always has something to display.
func (d *Daemon) resolveAndSaveRepoLabels(ctx context.Context) {
	repos := d.config.GetRepos()
	labels := make([]string, 0, len(repos))
	pathLabels := make(map[string]string, len(repos))

	for _, repoPath := range repos {
		label := repoPath // fallback
		remoteURL, err := d.gitService.GetRemoteOriginURL(ctx, repoPath)
		if err != nil {
			d.logger.Debug("could not get remote URL for repo label", "repo", repoPath, "error", err)
		} else if ownerRepo := git.ExtractOwnerRepo(remoteURL); ownerRepo != "" {
			label = ownerRepo
		}
		labels = append(labels, label)
		pathLabels[repoPath] = label
	}

	d.state.SetRepoLabels(labels, pathLabels)
	d.logger.Debug("resolved repo labels", "labels", labels)
}

// loadWorkflowConfigs loads workflow configs and creates engines for all registered repos.
func (d *Daemon) loadWorkflowConfigs() {
	d.workflowConfigs = make(map[string]*workflow.Config)
	d.engines = make(map[string]*workflow.Engine)

	for _, repoPath := range d.config.GetRepos() {
		wfFile := d.getWorkflowFileForRepo(repoPath)
		cfg, err := workflow.LoadAndMergeWithFile(repoPath, wfFile)
		if err != nil {
			d.logger.Warn("failed to load workflow config", "repo", repoPath, "error", err)
			continue
		}
		if cfg == nil {
			d.logger.Warn("no .erg/workflow.yaml found — skipping repo (run `erg workflow init` to create one)", "repo", repoPath)
			continue
		}
		d.workflowConfigs[repoPath] = cfg

		// Sync Asana project GID from workflow config into the config store so
		// MoveToSection and IsInSection (which read from config.GetAsanaProject)
		// work without requiring a separate manual configuration step.
		if cfg.Source.Provider == "asana" && cfg.Source.Filter.Project != "" {
			d.config.SetAsanaProject(repoPath, cfg.Source.Filter.Project)
		}

		// Create engine with action registry and event checker
		registry := d.buildActionRegistry()
		checker := newEventChecker(d)
		engine := workflow.NewEngine(cfg, registry, checker, d.logger)
		d.engines[repoPath] = engine

		d.logger.Debug("loaded workflow config", "repo", repoPath, "provider", cfg.Source.Provider)
	}
}

// buildActionRegistry creates the action registry with all daemon actions.
func (d *Daemon) buildActionRegistry() *workflow.ActionRegistry {
	registry := workflow.NewActionRegistry()
	registry.Register("ai.code", &codingAction{daemon: d})
	registry.Register("ai.review", &aiReviewAction{daemon: d})
	registry.Register("ai.plan", &planningAction{daemon: d})
	registry.Register("github.create_pr", &createPRAction{daemon: d})
	registry.Register("github.push", &pushAction{daemon: d})
	registry.Register("github.merge", &mergeAction{daemon: d})
	registry.Register("github.comment_issue", &commentIssueAction{daemon: d})
	registry.Register("github.comment_pr", &commentPRAction{daemon: d})
	registry.Register("github.add_label", &addLabelAction{daemon: d})
	registry.Register("github.remove_label", &removeLabelAction{daemon: d})
	registry.Register("github.close_issue", &closeIssueAction{daemon: d})
	registry.Register("github.request_review", &requestReviewAction{daemon: d})
	registry.Register("github.assign_pr", &assignPRAction{daemon: d})
	registry.Register("ai.fix_ci", &fixCIAction{daemon: d})
	registry.Register("ai.address_review", &addressReviewAction{daemon: d})
	registry.Register("ai.write_pr_description", &writePRDescriptionAction{daemon: d})
	registry.Register("git.format", &formatAction{daemon: d})
	registry.Register("git.rebase", &rebaseAction{daemon: d})
	registry.Register("git.validate_diff", &validateDiffAction{daemon: d})
	registry.Register("git.squash", &squashAction{daemon: d})
	registry.Register("git.cherry_pick", &cherryPickAction{daemon: d})
	registry.Register("ai.resolve_conflicts", &resolveConflictsAction{daemon: d})
	registry.Register("asana.comment", &asanaCommentAction{daemon: d})
	registry.Register("asana.move_to_section", &asanaMoveToSectionAction{daemon: d})
	registry.Register("linear.comment", &linearCommentAction{daemon: d})
	registry.Register("linear.move_to_state", &linearMoveToStateAction{daemon: d})
	registry.Register("github.create_release", &createReleaseAction{daemon: d})
	registry.Register("slack.notify", &slackNotifyAction{daemon: d})
	registry.Register("webhook.post", &webhookPostAction{daemon: d})
	registry.Register("workflow.retry", workflow.NewRetryAction(registry))
	registry.Register("workflow.wait", &waitAction{daemon: d})
	return registry
}

// getWorkflowFileForRepo returns the workflow file path for a specific repo.
// It checks repoWorkflowFiles first, then falls back to the global workflowFile.
func (d *Daemon) getWorkflowFileForRepo(repoPath string) string {
	if d.repoWorkflowFiles != nil {
		if f, ok := d.repoWorkflowFiles[repoPath]; ok {
			return f
		}
	}
	return d.workflowFile
}

// getWorkflowConfig returns the workflow config for a repo.
// The repo must have a loaded config — if missing, this logs an error and
// returns a minimal config to avoid panics, but the repo will not function.
func (d *Daemon) getWorkflowConfig(repoPath string) *workflow.Config {
	if cfg, ok := d.workflowConfigs[repoPath]; ok {
		return cfg
	}
	d.logger.Error("no workflow config loaded for repo — add .erg/workflow.yaml", "repo", repoPath)
	return &workflow.Config{
		Start: "failed",
		States: map[string]*workflow.State{
			"failed": {Type: workflow.StateTypeFail},
		},
	}
}

// getEngine returns the workflow engine for a repo.
// The repo must have a loaded engine — if missing, this logs an error and
// returns a minimal engine to avoid panics, but the repo will not function.
func (d *Daemon) getEngine(repoPath string) *workflow.Engine {
	if engine, ok := d.engines[repoPath]; ok {
		return engine
	}
	d.logger.Error("no workflow engine loaded for repo — add .erg/workflow.yaml", "repo", repoPath)
	cfg := &workflow.Config{
		Start: "failed",
		States: map[string]*workflow.State{
			"failed": {Type: workflow.StateTypeFail},
		},
	}
	registry := d.buildActionRegistry()
	checker := newEventChecker(d)
	return workflow.NewEngine(cfg, registry, checker, d.logger)
}

// getEffectiveMergeMethod returns the effective merge method.
func (d *Daemon) getEffectiveMergeMethod(repoPath string) string {
	if d.mergeMethod != "" {
		return d.mergeMethod
	}
	wfCfg := d.getWorkflowConfig(repoPath)
	mergeState := wfCfg.States["merge"]
	if mergeState != nil {
		p := workflow.NewParamHelper(mergeState.Params)
		if m := p.String("method", ""); m != "" {
			return m
		}
	}
	return d.config.GetAutoMergeMethod()
}
