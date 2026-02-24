package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/container"
	"github.com/zhubert/erg/internal/daemon"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/logger"
	"github.com/zhubert/erg/internal/session"
)

var (
	agentOnce       bool
	agentRepo       string
	agentForeground bool
	agentDaemonMode bool // hidden --_daemon flag for re-exec child
)

// osExecutable is the function used to resolve the current binary path.
// Overridden in tests.
var osExecutable = os.Executable

func init() {
	rootCmd.RunE = runAgent
	rootCmd.Flags().BoolVar(&agentOnce, "once", false, "Run one tick and exit (vs continuous daemon)")
	rootCmd.Flags().StringVar(&agentRepo, "repo", "", "Repo to poll (owner/repo or filesystem path)")
	rootCmd.Flags().BoolVar(&agentDaemonMode, "_daemon", false, "Internal: run as detached daemon child")
	rootCmd.Flags().MarkHidden("_daemon") //nolint:errcheck
	rootCmd.Flags().MarkHidden("once")    //nolint:errcheck
	rootCmd.Flags().MarkHidden("repo")    //nolint:errcheck
}

func runAgent(cmd *cobra.Command, args []string) error {
	if agentDaemonMode {
		return runDaemonChild(cmd, args)
	}
	// When called as root command without --_daemon, show help
	return cmd.Help()
}

// daemonize performs all setup visible to the user (prereqs, image build),
// then re-execs itself with --_daemon to detach from the terminal.
func daemonize(cmd *cobra.Command, args []string) error {
	// Validate prerequisites
	prereqs := cli.DefaultPrerequisites()
	if err := cli.ValidateRequired(prereqs); err != nil {
		return fmt.Errorf("%v\n\nInstall required tools and try again", err)
	}

	if !hasContainerRuntime() {
		return fmt.Errorf("a container runtime is required for agent mode.\nInstall Docker: https://docs.docker.com/get-docker/\nInstall Colima: https://github.com/abiosoft/colima")
	}

	if err := checkDockerDaemon(); err != nil {
		return err
	}

	// Create services
	sessSvc := session.NewSessionService()

	// Resolve repo
	resolved, err := resolveAgentRepo(context.Background(), agentRepo, sessSvc)
	if err != nil {
		return err
	}
	agentRepo = resolved

	// Check no daemon already running for this repo
	if pid, running := daemonstate.ReadLockStatus(agentRepo); running {
		return fmt.Errorf("daemon already running for this repo (PID %d)", pid)
	}

	// Set up cancellable context for image build phase
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize file logger for the build phase — all output goes to log file, not stdout
	logger.SetDebug(true)
	defer logger.Close()
	buildLogger := logger.Get()

	// Load workflow config + build image
	wfCfg, err := workflow.LoadAndMerge(agentRepo)
	if err != nil {
		return fmt.Errorf("error loading workflow config: %w", err)
	}

	if wfCfg.Settings == nil || wfCfg.Settings.ContainerImage == "" {
		detected := container.Detect(ctx, agentRepo)
		buildLogger.Info("auto-detected languages", "languages", detected, "repo", agentRepo)

		image, err := container.EnsureImage(ctx, detected, version, buildLogger)
		if err != nil {
			return fmt.Errorf("failed to auto-build container image: %w\n\n"+
				"You can skip auto-detection by setting container_image in .erg/workflow.yaml", err)
		}
		_ = image // image cached; child will find it
	}

	// Build args for re-exec
	childArgs := buildDaemonArgs(agentRepo, agentOnce)

	// Re-exec self with --_daemon
	self, err := osExecutable()
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	child := exec.Command(self, childArgs...)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdin = nil
	child.Stdout = nil
	child.Stderr = nil

	if err := child.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}
	childPID := child.Process.Pid

	// Detach — we don't wait for the child
	if err := child.Process.Release(); err != nil {
		return fmt.Errorf("failed to detach daemon process: %w", err)
	}

	// Poll lock file to confirm child started (timeout 30s)
	if err := waitForDaemonLock(agentRepo, childPID, 30*time.Second); err != nil {
		return err
	}

	logPath, _ := logger.DefaultLogPath()
	fmt.Printf("erg daemon started (PID %d)\nLogs: %s\n", childPID, logPath)
	return nil
}

// waitForDaemonLock polls the lock file until the expected PID appears or timeout.
func waitForDaemonLock(repo string, expectedPID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pid, running := daemonstate.ReadLockStatus(repo)
		if pid == expectedPID && running {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start within %s (expected PID %d)", timeout, expectedPID)
}

// buildDaemonArgs constructs the args slice for the re-exec'd child process.
func buildDaemonArgs(repo string, once bool) []string {
	args := []string{"--_daemon", "--repo", repo}
	if once {
		args = append(args, "--once")
	}
	return args
}

// runDaemonChild is the entry point for the detached daemon child.
// All logging goes to file — no stdout.
func runDaemonChild(_ *cobra.Command, _ []string) error {
	// Enable debug logging for daemon mode
	logger.SetDebug(true)
	defer logger.Close()

	fileLogger := logger.Get()

	sessSvc := session.NewSessionService()

	// Resolve repo (should already be set via --repo)
	resolved, err := resolveAgentRepo(context.Background(), agentRepo, sessSvc)
	if err != nil {
		return err
	}
	agentRepo = resolved

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		fileLogger.Info("received signal, shutting down gracefully", "signal", sig)
		cancel()
		sig = <-sigCh
		fileLogger.Warn("received second signal, force exiting", "signal", sig)
		os.Exit(1)
	}()

	return runDaemonWithLogger(ctx, fileLogger)
}

// runForeground runs the daemon in the current process with a live status display.
func runForeground(_ *cobra.Command, _ []string) error {
	// Validate prerequisites
	prereqs := cli.DefaultPrerequisites()
	if err := cli.ValidateRequired(prereqs); err != nil {
		return fmt.Errorf("%v\n\nInstall required tools and try again", err)
	}

	if !hasContainerRuntime() {
		return fmt.Errorf("a container runtime is required for agent mode.\nInstall Docker: https://docs.docker.com/get-docker/\nInstall Colima: https://github.com/abiosoft/colima")
	}

	if err := checkDockerDaemon(); err != nil {
		return err
	}

	// Enable debug logging
	logger.SetDebug(true)
	defer logger.Close()

	fileLogger := logger.Get()

	// Temporary stdout logger for build output
	buildLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	sessSvc := session.NewSessionService()

	resolved, err := resolveAgentRepo(context.Background(), agentRepo, sessSvc)
	if err != nil {
		return err
	}
	if resolved != agentRepo && agentRepo != "" {
		buildLogger.Info("using repo", "repo", resolved)
	}
	agentRepo = resolved

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		fileLogger.Info("received signal, shutting down gracefully", "signal", sig)
		cancel()
		sig = <-sigCh
		fileLogger.Warn("received second signal, force exiting", "signal", sig)
		os.Exit(1)
	}()

	// Load workflow config + build image
	wfCfg, err := workflow.LoadAndMerge(agentRepo)
	if err != nil {
		return fmt.Errorf("error loading workflow config: %w", err)
	}

	if wfCfg.Settings == nil || wfCfg.Settings.ContainerImage == "" {
		detected := container.Detect(ctx, agentRepo)
		buildLogger.Info("auto-detected languages", "languages", detected, "repo", agentRepo)

		image, err := container.EnsureImage(ctx, detected, version, buildLogger)
		if err != nil {
			return fmt.Errorf("failed to auto-build container image: %w\n\n"+
				"You can skip auto-detection by setting container_image in .erg/workflow.yaml", err)
		}

		if wfCfg.Settings == nil {
			wfCfg.Settings = &workflow.SettingsConfig{}
		}
		wfCfg.Settings.ContainerImage = image
	}

	// Run daemon in a goroutine
	daemonErr := make(chan error, 1)
	go func() {
		daemonErr <- runDaemonWithLogger(ctx, fileLogger)
	}()

	if agentOnce {
		// For --once mode, just wait for the daemon to finish
		return <-daemonErr
	}

	// Auto-refreshing status display
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Initial display
	clearScreen()
	_ = displayDashboard(agentRepo)

	for {
		select {
		case err := <-daemonErr:
			return err
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			clearScreen()
			_ = displayDashboard(agentRepo)
		}
	}
}

// runDaemonWithLogger creates all services and runs the daemon with the given logger.
// This is the shared core between runDaemonChild and runForeground.
func runDaemonWithLogger(ctx context.Context, daemonLogger *slog.Logger) error {
	gitSvc := git.NewGitService()
	sessSvc := session.NewSessionService()

	wfCfg, err := workflow.LoadAndMerge(agentRepo)
	if err != nil {
		return fmt.Errorf("error loading workflow config: %w", err)
	}

	// Auto-detect container image if not set (should be cached from parent)
	if wfCfg.Settings == nil || wfCfg.Settings.ContainerImage == "" {
		detected := container.Detect(ctx, agentRepo)
		image, err := container.EnsureImage(ctx, detected, version, daemonLogger)
		if err != nil {
			return fmt.Errorf("failed to auto-build container image: %w", err)
		}
		if wfCfg.Settings == nil {
			wfCfg.Settings = &workflow.SettingsConfig{}
		}
		wfCfg.Settings.ContainerImage = image
	}

	// Build AgentConfig from workflow settings + defaults
	var cfgOpts []agentconfig.AgentConfigOption
	cfgOpts = append(cfgOpts, agentconfig.WithRepos([]string{agentRepo}))
	if wfCfg.Settings != nil {
		if wfCfg.Settings.ContainerImage != "" {
			cfgOpts = append(cfgOpts, agentconfig.WithContainerImage(wfCfg.Settings.ContainerImage))
		}
		if wfCfg.Settings.BranchPrefix != "" {
			cfgOpts = append(cfgOpts, agentconfig.WithBranchPrefix(wfCfg.Settings.BranchPrefix))
		}
		if wfCfg.Settings.MaxConcurrent > 0 {
			cfgOpts = append(cfgOpts, agentconfig.WithMaxConcurrent(wfCfg.Settings.MaxConcurrent))
		}
		if wfCfg.Settings.CleanupMerged != nil {
			cfgOpts = append(cfgOpts, agentconfig.WithCleanupMerged(*wfCfg.Settings.CleanupMerged))
		}
		if wfCfg.Settings.MaxTurns > 0 {
			cfgOpts = append(cfgOpts, agentconfig.WithMaxTurns(wfCfg.Settings.MaxTurns))
		}
		if wfCfg.Settings.MaxDuration > 0 {
			cfgOpts = append(cfgOpts, agentconfig.WithMaxDuration(wfCfg.Settings.MaxDuration))
		}
		if wfCfg.Settings.MergeMethod != "" {
			cfgOpts = append(cfgOpts, agentconfig.WithMergeMethod(wfCfg.Settings.MergeMethod))
		}
	}
	cfg := agentconfig.NewAgentConfig(cfgOpts...)

	// Initialize issue providers
	githubProvider := issues.NewGitHubProvider(gitSvc)
	asanaProvider := issues.NewAsanaProvider(cfg)
	linearProvider := issues.NewLinearProvider(cfg)
	issueRegistry := issues.NewProviderRegistry(githubProvider, asanaProvider, linearProvider)

	// Build daemon options
	var opts []daemon.Option
	if agentOnce {
		opts = append(opts, daemon.WithOnce(true))
	}
	opts = append(opts, daemon.WithRepoFilter(agentRepo))
	if wfCfg.Settings != nil && wfCfg.Settings.AutoMerge != nil {
		opts = append(opts, daemon.WithAutoMerge(*wfCfg.Settings.AutoMerge))
	}

	d := daemon.New(cfg, gitSvc, sessSvc, issueRegistry, daemonLogger, opts...)

	if err := d.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// cwdGitRootGetter abstracts the GetCurrentDirGitRoot call for testability.
type cwdGitRootGetter interface {
	GetCurrentDirGitRoot(ctx context.Context) string
}

// resolveAgentRepo returns the effective repo to use for the agent.
// If repo is non-empty it is returned unchanged.
// If repo is empty, the current working directory's git root is used.
// An error is returned when no repo can be determined.
func resolveAgentRepo(ctx context.Context, repo string, getter cwdGitRootGetter) (string, error) {
	if repo != "" {
		return repo, nil
	}
	cwdRoot := getter.GetCurrentDirGitRoot(ctx)
	if cwdRoot == "" {
		return "", fmt.Errorf("--repo is required: specify which repo to poll (owner/repo or filesystem path)\n\nAlternatively, run from within a git repository to use it as the default")
	}
	return cwdRoot, nil
}

// formatUptime returns a human-friendly duration string (e.g. "2h 15m").
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// uptimeFromLockFile returns uptime based on the lock file's modification time.
func uptimeFromLockFile(repo string) time.Duration {
	fp := daemonstate.LockFilePath(repo)
	info, err := os.Stat(fp)
	if err != nil {
		return 0
	}
	return time.Since(info.ModTime())
}

// displaySummary prints a one-shot daemon status summary.
func displaySummary(repo string) error {
	pid, running := daemonstate.ReadLockStatus(repo)
	if !running && pid == 0 {
		fmt.Println("Daemon: not running")
		return nil
	}

	status := "running"
	if !running {
		status = "dead"
	}

	uptime := uptimeFromLockFile(repo)
	uptimeStr := ""
	if running && uptime > 0 {
		uptimeStr = fmt.Sprintf(", uptime %s", formatUptime(uptime))
	}

	fmt.Printf("Daemon: %s (PID %d%s)\n", status, pid, uptimeStr)
	fmt.Printf("Repo:   %s\n", repo)

	// Load state for counts
	state, err := daemonstate.LoadDaemonState(repo)
	if err == nil {
		activeCount := 0
		queuedCount := 0
		completedCount := 0
		failedCount := 0
		for _, item := range state.WorkItems {
			switch item.State {
			case daemonstate.WorkItemActive:
				activeCount++
			case daemonstate.WorkItemQueued:
				queuedCount++
			case daemonstate.WorkItemCompleted:
				completedCount++
			case daemonstate.WorkItemFailed:
				failedCount++
			}
		}

		// Load max_concurrent from workflow config
		wfCfg, _ := workflow.LoadAndMerge(repo)
		maxConcurrent := 0
		if wfCfg != nil && wfCfg.Settings != nil {
			maxConcurrent = wfCfg.Settings.MaxConcurrent
		}

		if maxConcurrent > 0 {
			fmt.Printf("Slots:  %d/%d active\n", activeCount, maxConcurrent)
		} else {
			fmt.Printf("Slots:  %d active\n", activeCount)
		}
		fmt.Printf("Active: %d  |  Queued: %d  |  Completed: %d  |  Failed: %d\n",
			activeCount, queuedCount, completedCount, failedCount)

		costUSD, outputTokens, inputTokens := state.GetSpend()
		totalTokens := outputTokens + inputTokens
		if costUSD > 0 || totalTokens > 0 {
			fmt.Printf("Spend:  $%.4f  |  Tokens: %s (in: %s, out: %s)\n",
				costUSD,
				formatTokenCount(totalTokens),
				formatTokenCount(inputTokens),
				formatTokenCount(outputTokens),
			)
		}
	}

	logPath, _ := logger.DefaultLogPath()
	fmt.Printf("Logs:   %s\n", logPath)
	return nil
}

