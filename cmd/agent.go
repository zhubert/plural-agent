package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhubert/plural-agent/internal/agentconfig"
	"github.com/zhubert/plural-agent/internal/daemon"
	"github.com/zhubert/plural-agent/internal/workflow"
	"github.com/zhubert/plural-core/cli"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/issues"
	"github.com/zhubert/plural-core/logger"
	"github.com/zhubert/plural-core/session"
)

var (
	agentOnce                  bool
	agentRepo                  string
	agentMaxConcurrent         int
	agentMaxTurns              int
	agentMaxDuration           int
	agentAutoAddressPRComments bool
	agentAutoBroadcastPR       bool
	agentAutoMerge             bool
	agentNoAutoMerge           bool
	agentMergeMethod           string
)


var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run headless autonomous agent daemon",
	Long: `Persistent orchestrator daemon that manages the full lifecycle of work items:
picking up issues, coding, PR creation, review feedback cycles, and final merge.

The daemon is stoppable and restartable without losing track of in-flight work.
State is persisted to ~/.plural/daemon-state.json.

If --repo is not specified and the current directory is inside a git repository,
that repository is used as the default. Auto-merge is enabled by default;
use --no-auto-merge to disable.

All sessions are containerized (container = sandbox).

Examples:
  plural agent                                # Use current git repo as default
  plural agent --repo owner/repo              # Run daemon (long-running, auto-merge on)
  plural agent --repo owner/repo --once       # Process one tick and exit
  plural agent --repo /path/to/repo           # Use filesystem path instead
  plural agent --repo owner/repo --no-auto-merge  # Disable auto-merge
  plural agent --repo owner/repo --max-turns 100`,
	RunE: runAgent,
}

func init() {
	agentCmd.Flags().BoolVar(&agentOnce, "once", false, "Run one tick and exit (vs continuous daemon)")
	agentCmd.Flags().StringVar(&agentRepo, "repo", "", "Repo to poll (owner/repo or filesystem path)")
	agentCmd.Flags().IntVar(&agentMaxConcurrent, "max-concurrent", 0, "Override max concurrent sessions (0 = use config)")
	agentCmd.Flags().IntVar(&agentMaxTurns, "max-turns", 0, "Override max autonomous turns per session (0 = use config default of 50)")
	agentCmd.Flags().IntVar(&agentMaxDuration, "max-duration", 0, "Override max autonomous duration in minutes (0 = use config default of 30)")
	agentCmd.Flags().BoolVar(&agentAutoAddressPRComments, "auto-address-pr-comments", false, "Auto-address PR review comments")
	agentCmd.Flags().BoolVar(&agentAutoBroadcastPR, "auto-broadcast-pr", false, "Auto-create PRs when broadcast group completes")
	agentCmd.Flags().BoolVar(&agentAutoMerge, "auto-merge", false, "Auto-merge PRs after review approval and CI pass (default: true)")
	agentCmd.Flags().BoolVar(&agentNoAutoMerge, "no-auto-merge", false, "Disable auto-merge")
	agentCmd.Flags().StringVar(&agentMergeMethod, "merge-method", "", "Merge method: rebase, squash, or merge (default: rebase)")
	rootCmd.AddCommand(agentCmd)
}

func runAgent(cmd *cobra.Command, args []string) error {
	// Validate prerequisites
	prereqs := cli.DefaultPrerequisites()
	if err := cli.ValidateRequired(prereqs); err != nil {
		return fmt.Errorf("%v\n\nInstall required tools and try again", err)
	}

	// Check that docker is available (required for agent mode)
	dockerCheck := cli.Check(cli.Prerequisite{
		Name:        "docker",
		Required:    true,
		Description: "Docker (required for agent mode)",
		InstallURL:  "https://docs.docker.com/get-docker/",
	})
	if !dockerCheck.Found {
		return fmt.Errorf("docker is required for agent mode.\nInstall: https://docs.docker.com/get-docker/")
	}

	// Enable debug logging for agent mode (always on for headless autonomous operation)
	logger.SetDebug(true)

	// Ensure logger is closed on exit
	defer logger.Close()

	// Create structured logger for agent output (always debug level)
	agentLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create services
	gitSvc := git.NewGitService()
	sessSvc := session.NewSessionService()

	// If --repo is not provided, try to detect from current working directory
	resolved, err := resolveAgentRepo(context.Background(), agentRepo, sessSvc)
	if err != nil {
		return err
	}
	if resolved != agentRepo {
		agentLogger.Info("no --repo specified, using current directory", "repo", resolved)
	}
	agentRepo = resolved

	// Load workflow config for settings
	wfCfg, err := workflow.LoadAndMerge(agentRepo)
	if err != nil {
		return fmt.Errorf("error loading workflow config: %w", err)
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
	}
	cfg := agentconfig.NewAgentConfig(cfgOpts...)

	// Initialize issue providers (nil configs — Asana/Linear are configured via workflow source, not config.json)
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
	if agentMaxConcurrent > 0 {
		opts = append(opts, daemon.WithMaxConcurrent(agentMaxConcurrent))
	}
	if agentMaxTurns > 0 {
		opts = append(opts, daemon.WithMaxTurns(agentMaxTurns))
	}
	if agentMaxDuration > 0 {
		opts = append(opts, daemon.WithMaxDuration(agentMaxDuration))
	}
	if agentAutoAddressPRComments {
		opts = append(opts, daemon.WithAutoAddressPRComments(true))
	}
	if agentAutoBroadcastPR {
		opts = append(opts, daemon.WithAutoBroadcastPR(true))
	}
	// Auto-merge is on by default for daemon; --no-auto-merge disables it
	if agentNoAutoMerge {
		opts = append(opts, daemon.WithAutoMerge(false))
	}
	if agentMergeMethod != "" {
		opts = append(opts, daemon.WithMergeMethod(agentMergeMethod))
	}

	// Create daemon
	d := daemon.New(cfg, gitSvc, sessSvc, issueRegistry, agentLogger, opts...)

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		agentLogger.Info("received signal, shutting down gracefully", "signal", sig)
		cancel()
		// On second signal, force exit
		sig = <-sigCh
		agentLogger.Warn("received second signal, force exiting", "signal", sig)
		os.Exit(1)
	}()

	// Run daemon
	return d.Run(ctx)
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
