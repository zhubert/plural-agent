package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-agent/internal/workflow"
	"github.com/zhubert/plural-core/session"
)

var (
	statusRepo string
	statusMap  bool
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status and active work items",
	Long: `Reads the persisted daemon state file and displays where work items are
in the active workflow.

Use --map to see items positioned on the workflow graph.

Examples:
  plural-agent status                        # Use current git repo as default
  plural-agent status --repo owner/repo      # Check specific repo
  plural-agent status --repo owner/repo --map  # Workflow map view`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().StringVar(&statusRepo, "repo", "", "Repo to check status for (owner/repo or filesystem path)")
	statusCmd.Flags().BoolVar(&statusMap, "map", false, "Show items positioned on the workflow graph")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	repo := statusRepo
	if repo == "" {
		sessSvc := session.NewSessionService()
		var err error
		repo, err = resolveAgentRepo(context.Background(), "", sessSvc)
		if err != nil {
			return err
		}
	}

	// Check if state file exists before loading
	stateFilePath := daemonstate.StateFilePath(repo)
	if _, err := os.Stat(stateFilePath); os.IsNotExist(err) {
		fmt.Println("No daemon state found for this repo.")
		return nil
	}

	// Load daemon state
	state, err := daemonstate.LoadDaemonState(repo)
	if err != nil {
		return fmt.Errorf("failed to load daemon state: %w", err)
	}

	// Get daemon PID and running status from lock file
	pid, running := daemonstate.ReadLockStatus(repo)

	// Load workflow config for max_concurrent and map view (ignore error, use defaults)
	wfCfg, _ := workflow.LoadAndMerge(repo)
	maxConcurrent := 0
	if wfCfg != nil && wfCfg.Settings != nil {
		maxConcurrent = wfCfg.Settings.MaxConcurrent
	}

	// Collect display items: all non-completed items (active + queued + failed + abandoned)
	var items []*daemonstate.WorkItem
	for _, item := range state.WorkItems {
		if item.State != daemonstate.WorkItemCompleted {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	// Count queued items
	queuedCount := 0
	for _, item := range state.WorkItems {
		if item.State == daemonstate.WorkItemQueued {
			queuedCount++
		}
	}

	w := os.Stdout
	if len(items) == 0 {
		fmt.Fprintln(w, "No active work items.")
	} else if statusMap {
		// For map view, only show non-terminal items on the graph
		var activeItems []*daemonstate.WorkItem
		for _, item := range items {
			if !item.IsTerminal() {
				activeItems = append(activeItems, item)
			}
		}
		printMapView(w, activeItems, wfCfg)
	} else {
		printTableView(w, items)
	}

	fmt.Fprintln(w)
	printFooter(w, state.ActiveSlotCount(), maxConcurrent, queuedCount, pid, running)
	return nil
}

// printTableView renders work items as an aligned table.
func printTableView(w io.Writer, items []*daemonstate.WorkItem) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ISSUE\tSTEP\tPHASE\tAGE\tPR")
	for _, item := range items {
		issue := formatIssue(item)
		step := formatStep(item)
		phase := item.Phase
		if phase == "" {
			phase = "idle"
		}
		age := formatAge(item.StepEnteredAt)
		pr := item.PRURL
		if pr == "" {
			pr = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", issue, step, phase, age, pr)
	}
	tw.Flush()
}

// printMapView renders work items positioned on the workflow graph.
func printMapView(w io.Writer, items []*daemonstate.WorkItem, cfg *workflow.Config) {
	if cfg == nil {
		fmt.Fprintln(w, "(no workflow config available)")
		printTableView(w, items)
		return
	}

	// Build index: step name → work items at that step
	stepItems := make(map[string][]*daemonstate.WorkItem)
	for _, item := range items {
		step := item.CurrentStep
		if step == "" {
			step = cfg.Start
		}
		stepItems[step] = append(stepItems[step], item)
	}

	// Walk the primary (happy) path through the workflow
	path := primaryWorkflowPath(cfg)
	pathSet := make(map[string]bool, len(path))
	for _, s := range path {
		pathSet[s] = true
	}

	// Print states on the primary path
	for _, stateName := range path {
		stateItems := stepItems[stateName]
		if len(stateItems) == 0 {
			fmt.Fprintf(w, "  %s\n", stateName)
		} else {
			labels := make([]string, 0, len(stateItems))
			for _, item := range stateItems {
				labels = append(labels, formatIssue(item))
			}
			annotation := strings.Join(labels, ", ")
			pad := 26 - len(stateName)
			if pad < 1 {
				pad = 1
			}
			fmt.Fprintf(w, "  %s %s %s\n", stateName, strings.Repeat("·", pad), annotation)
		}
	}

	// Show any off-path states that have active items (sorted for determinism)
	var offPathSteps []string
	for step := range stepItems {
		if !pathSet[step] && len(stepItems[step]) > 0 {
			offPathSteps = append(offPathSteps, step)
		}
	}
	sort.Strings(offPathSteps)
	for _, step := range offPathSteps {
		stepItemList := stepItems[step]
		labels := make([]string, 0, len(stepItemList))
		for _, item := range stepItemList {
			labels = append(labels, formatIssue(item))
		}
		annotation := strings.Join(labels, ", ")
		pad := 26 - len(step)
		if pad < 1 {
			pad = 1
		}
		fmt.Fprintf(w, "  %s %s %s\n", step, strings.Repeat("·", pad), annotation)
	}
}

// primaryWorkflowPath walks the workflow graph from cfg.Start following the
// happy path: for task/wait/pass states use Next, for choice states use the
// first choice's Next (falling back to Default). Stops at terminal states.
func primaryWorkflowPath(cfg *workflow.Config) []string {
	if cfg == nil || cfg.Start == "" {
		return nil
	}

	var path []string
	visited := make(map[string]bool)
	current := cfg.Start

	for current != "" && !visited[current] {
		visited[current] = true
		path = append(path, current)

		state, ok := cfg.States[current]
		if !ok {
			break
		}

		switch state.Type {
		case workflow.StateTypeTask, workflow.StateTypeWait, workflow.StateTypePass:
			current = state.Next
		case workflow.StateTypeChoice:
			if len(state.Choices) > 0 {
				current = state.Choices[0].Next
			} else {
				current = state.Default
			}
		case workflow.StateTypeSucceed, workflow.StateTypeFail:
			// Terminal — include it but stop traversal
			current = ""
		default:
			current = ""
		}
	}

	return path
}

// printFooter prints slot usage, queue depth, and daemon PID status.
func printFooter(w io.Writer, slotCount, maxConcurrent, queuedCount, pid int, running bool) {
	parts := make([]string, 0, 3)
	if maxConcurrent > 0 {
		parts = append(parts, fmt.Sprintf("Slots: %d/%d active", slotCount, maxConcurrent))
	} else {
		parts = append(parts, fmt.Sprintf("Slots: %d active", slotCount))
	}
	parts = append(parts, fmt.Sprintf("Queued: %d", queuedCount))
	if pid > 0 {
		status := "running"
		if !running {
			status = "dead"
		}
		parts = append(parts, fmt.Sprintf("Daemon PID: %d (%s)", pid, status))
	} else {
		parts = append(parts, "Daemon: not running")
	}
	fmt.Fprintln(w, strings.Join(parts, "  |  "))
}

// formatIssue formats the issue reference for display.
// GitHub issues render as "#42 Title"; others as "ID Title".
// The result is truncated to 30 characters.
func formatIssue(item *daemonstate.WorkItem) string {
	ref := item.IssueRef
	var s string
	switch ref.Source {
	case "github", "GitHub":
		s = fmt.Sprintf("#%s %s", ref.ID, ref.Title)
	case "":
		s = item.ID
	default:
		s = fmt.Sprintf("%s %s", ref.ID, ref.Title)
	}
	if len(s) > 30 {
		s = s[:27] + "..."
	}
	return s
}

// formatStep returns the display string for the item's current step.
func formatStep(item *daemonstate.WorkItem) string {
	switch item.State {
	case daemonstate.WorkItemFailed:
		return "(failed)"
	case daemonstate.WorkItemAbandoned:
		return "(abandoned)"
	case daemonstate.WorkItemQueued:
		if item.CurrentStep != "" {
			return item.CurrentStep
		}
		return "(queued)"
	default:
		return item.CurrentStep
	}
}

// formatAge returns a human-friendly duration since t (e.g. "5m", "2h", "3d").
func formatAge(t time.Time) string {
	return formatAgeAt(t, time.Now())
}

// formatAgeAt returns a human-friendly duration between t and now.
// Returns "—" if t is the zero time.
func formatAgeAt(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
