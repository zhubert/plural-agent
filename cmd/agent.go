package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/container"
	"github.com/zhubert/erg/internal/daemon"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/plural-core/cli"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/issues"
	"github.com/zhubert/plural-core/logger"
	"github.com/zhubert/plural-core/session"
)

var (
	agentOnce bool
	agentRepo string
)

func init() {
	rootCmd.RunE = runAgent
	rootCmd.Flags().BoolVar(&agentOnce, "once", false, "Run one tick and exit (vs continuous daemon)")
	rootCmd.Flags().StringVar(&agentRepo, "repo", "", "Repo to poll (owner/repo or filesystem path)")
}

func runAgent(cmd *cobra.Command, args []string) error {
	// Validate prerequisites
	prereqs := cli.DefaultPrerequisites()
	if err := cli.ValidateRequired(prereqs); err != nil {
		return fmt.Errorf("%v\n\nInstall required tools and try again", err)
	}

	// Check that a container runtime is available (required for agent mode).
	// Either docker or colima on PATH satisfies this requirement.
	if !hasContainerRuntime() {
		return fmt.Errorf("a container runtime is required for agent mode.\nInstall Docker: https://docs.docker.com/get-docker/\nInstall Colima: https://github.com/abiosoft/colima")
	}

	// Verify a container runtime daemon is actually running (not just binary present)
	if err := checkDockerDaemon(); err != nil {
		return err
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

	// Set up signal handling (needed early for auto-detect/build cancellation)
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

	// Load workflow config for settings
	wfCfg, err := workflow.LoadAndMerge(agentRepo)
	if err != nil {
		return fmt.Errorf("error loading workflow config: %w", err)
	}

	// Auto-detect container image if not explicitly configured
	if wfCfg.Settings == nil || wfCfg.Settings.ContainerImage == "" {
		detected := container.Detect(ctx, agentRepo)
		agentLogger.Info("auto-detected languages", "languages", detected, "repo", agentRepo)

		image, err := container.EnsureImage(ctx, detected, version, agentLogger)
		if err != nil {
			return fmt.Errorf("failed to auto-build container image: %w\n\n"+
				"You can skip auto-detection by setting container_image in .erg/workflow.yaml", err)
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
	// Auto-merge is on by default; workflow settings can disable it
	if wfCfg.Settings != nil && wfCfg.Settings.AutoMerge != nil {
		opts = append(opts, daemon.WithAutoMerge(*wfCfg.Settings.AutoMerge))
	}

	// Create daemon
	d := daemon.New(cfg, gitSvc, sessSvc, issueRegistry, agentLogger, opts...)

	// Run daemon
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
