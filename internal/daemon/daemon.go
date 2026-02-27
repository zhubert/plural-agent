package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/agentconfig"
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
	autonomousFilterLabel     = "queued"
)

// Daemon is the persistent orchestrator that manages the full lifecycle of work items.
type Daemon struct {
	config         agentconfig.Config
	gitService     *git.GitService
	sessionService *session.SessionService
	sessionMgr     *manager.SessionManager
	issueRegistry  *issues.ProviderRegistry
	state          *daemonstate.DaemonState
	lock           *daemonstate.DaemonLock
	workers        map[string]*worker.SessionWorker
	workflowConfigs map[string]*workflow.Config // keyed by repo path
	engines        map[string]*workflow.Engine  // keyed by repo path
	mu             sync.Mutex
	workerDone     chan struct{} // buffered(1); workers signal when done to wake the main loop
	logger         *slog.Logger

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

	// Docker health tracking
	dockerDown        bool
	dockerDownLogged  bool
	dockerHealthCheck func() error // injectable for testing; nil means use default
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
		"maxConcurrent", d.getMaxConcurrent(),
		"maxTurns", d.getMaxTurns(),
		"maxDuration", d.getMaxDuration(),
		"autoMerge", d.autoMerge,
	)

	// Acquire lock (unless pre-acquired by parent process)
	if d.lock == nil {
		lock, err := daemonstate.AcquireLock(d.repoFilter)
		if err != nil {
			return fmt.Errorf("failed to acquire daemon lock: %w", err)
		}
		d.lock = lock
	}
	defer d.releaseLock()

	// Load or create state
	state, err := daemonstate.LoadDaemonState(d.repoFilter)
	if err != nil {
		// If state is for a different repo, create fresh
		d.logger.Warn("failed to load daemon state, creating new", "error", err)
		state = daemonstate.NewDaemonState(d.repoFilter)
	}
	d.state = state

	// Reset spend tracking so it reflects only the current daemon run.
	d.state.ResetSpend()
	if err := d.state.Save(); err != nil {
		d.logger.Warn("failed to save state after resetting spend", "error", err)
	}

	// Load workflow configs for all repos
	d.loadWorkflowConfigs()

	// Recover from any interrupted state
	d.recoverFromState(ctx)

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
	dockerOK := d.checkDockerHealth()
	if dockerOK {
		d.processRetryItems(ctx)   // Re-execute items whose retry delay has elapsed
		d.processIdleSyncItems(ctx) // Execute items idle on sync task steps (e.g. after recovery)
		d.processWorkItems(ctx)    // Process active items via engine
		d.pollForNewIssues(ctx)    // Find new issues (if slots available)
		d.startQueuedItems(ctx)    // Start coding on queued items
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

// loadWorkflowConfigs loads workflow configs and creates engines for all registered repos.
func (d *Daemon) loadWorkflowConfigs() {
	d.workflowConfigs = make(map[string]*workflow.Config)
	d.engines = make(map[string]*workflow.Engine)

	for _, repoPath := range d.config.GetRepos() {
		cfg, err := workflow.LoadAndMerge(repoPath)
		if err != nil {
			d.logger.Warn("failed to load workflow config", "repo", repoPath, "error", err)
			continue
		}
		d.workflowConfigs[repoPath] = cfg

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
	registry.Register("github.create_pr", &createPRAction{daemon: d})
	registry.Register("github.create_draft_pr", &createDraftPRAction{daemon: d})
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
	registry.Register("linear.comment", &linearCommentAction{daemon: d})
	registry.Register("slack.notify", &slackNotifyAction{daemon: d})
	registry.Register("webhook.post", &webhookPostAction{daemon: d})
	registry.Register("workflow.wait", &waitAction{daemon: d})
	return registry
}

// getWorkflowConfig returns the workflow config for a repo, or defaults.
func (d *Daemon) getWorkflowConfig(repoPath string) *workflow.Config {
	if cfg, ok := d.workflowConfigs[repoPath]; ok {
		return cfg
	}
	return workflow.DefaultWorkflowConfig()
}

// getEngine returns the workflow engine for a repo, or creates one with defaults.
func (d *Daemon) getEngine(repoPath string) *workflow.Engine {
	if engine, ok := d.engines[repoPath]; ok {
		return engine
	}
	// Create a default engine on the fly
	cfg := workflow.DefaultWorkflowConfig()
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
