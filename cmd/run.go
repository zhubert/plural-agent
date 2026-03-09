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
	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/container"
	"github.com/zhubert/erg/internal/daemon"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/logger"
	"github.com/zhubert/erg/internal/session"
	"github.com/zhubert/erg/internal/workflow"
)

var (
	runIssueID      string
	runRepo         string
	runWorkflowFile string
)

var runCmd = &cobra.Command{
	Use:     "run",
	Short:   "Run the workflow for a single issue",
	GroupID: "daemon",
	Long: `Run the erg workflow for a specific issue, synchronously in the foreground.

Fetches the issue from the configured provider, executes the full workflow,
and exits when complete. Useful for testing workflows, one-off tasks, or
CI/CD integration.

The --issue flag accepts the native ID format for the configured provider:
  GitHub:  integer issue number (e.g. --issue 42)
  Asana:   task GID (e.g. --issue 1234567890123)
  Linear:  issue identifier (e.g. --issue ENG-123)`,
	Example: `  erg run --issue 42
  erg run --issue 42 --repo /path/to/repo
  erg run --issue ENG-123 --workflow .erg/linear-workflow.yaml`,
	RunE: runIssue,
}

func init() {
	runCmd.Flags().StringVar(&runIssueID, "issue", "", "Issue ID to process (required)")
	runCmd.Flags().StringVar(&runRepo, "repo", "", "Repo path (default: current git root)")
	runCmd.Flags().StringVar(&runWorkflowFile, "workflow", "", "Path to workflow config file")
	_ = runCmd.MarkFlagRequired("issue")
	rootCmd.AddCommand(runCmd)
}

func runIssue(cmd *cobra.Command, args []string) error {
	// Validate prerequisites
	prereqs := cli.DefaultPrerequisites()
	if err := cli.ValidateRequired(prereqs); err != nil {
		return fmt.Errorf("%w\n\nInstall required tools and try again", err)
	}

	if !hasContainerRuntime() {
		return fmt.Errorf("a container runtime is required for agent mode.\nInstall OrbStack: https://orbstack.dev\nInstall Docker:   https://docs.docker.com/get-docker/\nInstall Colima:   https://github.com/abiosoft/colima")
	}
	if err := checkDockerDaemon(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		_ = sig
		cancel()
	}()

	// Resolve repo path
	sessSvc := session.NewSessionService()
	repoPath, err := resolveAgentRepo(ctx, runRepo, sessSvc)
	if err != nil {
		return err
	}

	// Enable logging
	logger.SetDebug(true)
	defer logger.Close()
	runLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Load workflow config
	wfCfg, err := workflow.LoadAndMergeWithFile(repoPath, runWorkflowFile)
	if err != nil {
		return fmt.Errorf("error loading workflow config: %w", err)
	}
	if wfCfg == nil {
		return fmt.Errorf("no workflow config found — run `erg workflow init` to create .erg/workflow.yaml")
	}

	// Ensure container image
	if wfCfg.Settings == nil || wfCfg.Settings.ContainerImage == "" {
		detected := container.Detect(ctx, repoPath)
		runLogger.Info("auto-detected languages", "languages", detected)
		image, _, err := container.EnsureImage(ctx, detected, version, runLogger)
		if err != nil {
			return fmt.Errorf("failed to auto-build container image: %w\n\n"+
				"You can skip auto-detection by setting container_image in .erg/workflow.yaml", err)
		}
		if wfCfg.Settings == nil {
			wfCfg.Settings = &workflow.SettingsConfig{}
		}
		wfCfg.Settings.ContainerImage = image
	}

	if err := validateWorkflowConfig(wfCfg); err != nil {
		return err
	}

	// Build AgentConfig
	var cfgOpts []agentconfig.AgentConfigOption
	cfgOpts = append(cfgOpts, agentconfig.WithRepos([]string{repoPath}))
	if wfCfg.Settings != nil {
		if wfCfg.Settings.ContainerImage != "" {
			cfgOpts = append(cfgOpts, agentconfig.WithContainerImage(wfCfg.Settings.ContainerImage))
		}
		if wfCfg.Settings.BranchPrefix != "" {
			cfgOpts = append(cfgOpts, agentconfig.WithBranchPrefix(wfCfg.Settings.BranchPrefix))
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
		if wfCfg.Settings.CleanupMerged != nil {
			cfgOpts = append(cfgOpts, agentconfig.WithCleanupMerged(*wfCfg.Settings.CleanupMerged))
		}
	}
	cfg := agentconfig.NewAgentConfig(cfgOpts...)
	if wfCfg.Source.Provider == "asana" && wfCfg.Source.Filter.Project != "" {
		cfg.SetAsanaProject(repoPath, wfCfg.Source.Filter.Project)
	}
	if wfCfg.Source.Provider == "linear" && wfCfg.Source.Filter.Team != "" {
		cfg.SetLinearTeam(repoPath, wfCfg.Source.Filter.Team)
	}

	// Build provider registry and fetch the specific issue
	gitSvc := git.NewGitService()
	githubProvider := issues.NewGitHubProvider(gitSvc)
	asanaProvider := issues.NewAsanaProvider(cfg)
	linearProvider := issues.NewLinearProvider(cfg)
	issueRegistry := issues.NewProviderRegistry(githubProvider, asanaProvider, linearProvider)

	providerSource := issues.Source(wfCfg.Source.Provider)
	if providerSource == "" {
		providerSource = issues.SourceGitHub
	}

	p := issueRegistry.GetProvider(providerSource)
	if p == nil {
		return fmt.Errorf("provider %q not registered", wfCfg.Source.Provider)
	}

	getter, ok := p.(issues.IssueGetter)
	if !ok {
		return fmt.Errorf("provider %q does not support single-issue lookup", wfCfg.Source.Provider)
	}

	runLogger.Info("fetching issue", "provider", wfCfg.Source.Provider, "id", runIssueID)
	issue, err := getter.GetIssue(ctx, repoPath, runIssueID)
	if err != nil {
		return fmt.Errorf("failed to fetch issue %q: %w", runIssueID, err)
	}
	runLogger.Info("found issue", "title", issue.Title, "url", issue.URL)

	// Use a distinct lock/state key to avoid conflicting with a running daemon on the same repo
	runKey := fmt.Sprintf("run-%s-%s", repoPath, runIssueID)

	// Build daemon options
	opts := []daemon.Option{
		daemon.WithOnce(true),
		daemon.WithRepoFilter(repoPath),
		daemon.WithPreseededIssue(*issue),
		daemon.WithDaemonID(runKey),
	}
	if wfCfg.Settings != nil && wfCfg.Settings.AutoMerge != nil {
		opts = append(opts, daemon.WithAutoMerge(*wfCfg.Settings.AutoMerge))
	}
	if runWorkflowFile != "" {
		opts = append(opts, daemon.WithWorkflowFile(runWorkflowFile))
	}

	d := daemon.New(cfg, gitSvc, sessSvc, issueRegistry, runLogger, opts...)
	if err := d.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
