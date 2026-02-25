package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/session"
	"github.com/zhubert/erg/internal/workflow"
)

var (
	statusRepo string
	statusTail bool
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status summary",
	Long: `Shows a one-shot summary of the daemon status including PID, uptime,
and work item counts.

Examples:
  erg status                     # Show status for current repo
  erg status --repo owner/repo   # Check specific repo
  erg status --tail              # Live split-screen log view per active session`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().StringVar(&statusRepo, "repo", "", "Repo to check status for (owner/repo or filesystem path)")
	statusCmd.Flags().BoolVar(&statusTail, "tail", false, "Show live split-screen log view for active sessions")
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

	if statusTail {
		return runTailView(repo)
	}
	return displaySummary(repo)
}

// clearScreen clears the terminal using ANSI escape codes.
func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

// printMatrixView renders a state×job matrix: rows=workflow states, columns=work items.
func printMatrixView(w io.Writer, items []*daemonstate.WorkItem, cfg *workflow.Config) {
	if cfg == nil {
		fmt.Fprintln(w, "(no workflow config available)")
		printTableView(w, items)
		return
	}

	// Get primary path states as row labels
	path := primaryWorkflowPath(cfg)
	if len(path) == 0 {
		printTableView(w, items)
		return
	}

	// Build index: step name → items at that step
	stepItems := make(map[string][]*daemonstate.WorkItem)
	for _, item := range items {
		step := item.CurrentStep
		if step == "" {
			step = cfg.Start
		}
		stepItems[step] = append(stepItems[step], item)
	}

	// Determine state column width (auto-sized to longest state name)
	stateColWidth := 8
	for _, s := range path {
		if len(s) > stateColWidth {
			stateColWidth = len(s)
		}
	}
	for step := range stepItems {
		if len(step) > stateColWidth {
			stateColWidth = len(step)
		}
	}

	// Determine item column width (auto-sized to longest issue label, min 16)
	itemColWidth := 16
	headers := make([]string, len(items))
	for i, item := range items {
		h := formatIssue(item)
		headers[i] = h
		if len(h) > itemColWidth {
			itemColWidth = len(h)
		}
	}

	// Print header row
	fmt.Fprintf(w, "%-*s", stateColWidth, "STATE")
	for _, h := range headers {
		fmt.Fprintf(w, "  %-*s", itemColWidth, h)
	}
	fmt.Fprintln(w)

	// Print separator line
	fmt.Fprint(w, strings.Repeat("─", stateColWidth))
	for range headers {
		fmt.Fprint(w, "  "+strings.Repeat("─", itemColWidth))
	}
	fmt.Fprintln(w)

	// Track which states are on the primary path
	pathSet := make(map[string]bool, len(path))
	for _, s := range path {
		pathSet[s] = true
	}

	// Print rows for the primary path
	for _, stateName := range path {
		printMatrixRow(w, stateName, items, cfg, stateColWidth, itemColWidth)
	}

	// Print off-path states that have active items
	var offPathSteps []string
	for step := range stepItems {
		if !pathSet[step] && len(stepItems[step]) > 0 {
			offPathSteps = append(offPathSteps, step)
		}
	}
	if len(offPathSteps) > 0 {
		sort.Strings(offPathSteps)
		// Dotted separator before off-path rows
		fmt.Fprint(w, strings.Repeat("·", stateColWidth))
		for range headers {
			fmt.Fprint(w, "  "+strings.Repeat("·", itemColWidth))
		}
		fmt.Fprintln(w)
		for _, step := range offPathSteps {
			printMatrixRow(w, step, items, cfg, stateColWidth, itemColWidth)
		}
	}
}

// printMatrixRow renders one row of the matrix for the given state name.
func printMatrixRow(w io.Writer, stateName string, items []*daemonstate.WorkItem, cfg *workflow.Config, stateColWidth, itemColWidth int) {
	fmt.Fprintf(w, "%-*s", stateColWidth, stateName)
	for _, item := range items {
		step := item.CurrentStep
		if step == "" {
			step = cfg.Start
		}
		if step == stateName {
			cell := formatCellInfo(item)
			if len(cell) > itemColWidth {
				cell = cell[:itemColWidth]
			}
			fmt.Fprintf(w, "  %-*s", itemColWidth, cell)
		} else {
			fmt.Fprintf(w, "  %-*s", itemColWidth, "")
		}
	}
	fmt.Fprintln(w)
}

// formatCellInfo returns a brief description for a work item in its current state cell.
func formatCellInfo(item *daemonstate.WorkItem) string {
	if item.State == daemonstate.WorkItemFailed {
		return "(failed)"
	}
	phase := item.Phase
	if phase == "" {
		phase = "idle"
	}
	age := formatAge(item.StepEnteredAt)
	return fmt.Sprintf("%s %s", phase, age)
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
			// Pick the first choice that leads forward through the graph,
			// preferring choices that don't dead-end back into already-visited
			// states. This ensures the displayed path follows the "happy path"
			// (e.g., ci_passed → await_review) rather than a bounded loop
			// (e.g., conflicting → rebase → await_ci).
			picked := ""
			for _, c := range state.Choices {
				if c.Next == "" || visited[c.Next] {
					continue
				}
				// Check if this choice leads to a state that immediately
				// loops back to an already-visited state
				if nextState, ok := cfg.States[c.Next]; ok {
					if nextState.Next != "" && visited[nextState.Next] {
						continue
					}
				}
				picked = c.Next
				break
			}
			// Fallback: first unvisited choice
			if picked == "" {
				for _, c := range state.Choices {
					if c.Next != "" && !visited[c.Next] {
						picked = c.Next
						break
					}
				}
			}
			if picked == "" {
				picked = state.Default
			}
			current = picked
		case workflow.StateTypeSucceed, workflow.StateTypeFail:
			// Terminal — include it but stop traversal
			current = ""
		default:
			current = ""
		}
	}

	return path
}

// printFooter prints slot usage, queue depth, daemon PID status, and spend.
func printFooter(w io.Writer, slotCount, maxConcurrent, queuedCount, pid int, running bool, costUSD float64, totalTokens int) {
	parts := make([]string, 0, 4)
	if maxConcurrent > 0 {
		parts = append(parts, fmt.Sprintf("Slots: %d/%d active", slotCount, maxConcurrent))
	} else {
		parts = append(parts, fmt.Sprintf("Slots: %d active", slotCount))
	}
	parts = append(parts, fmt.Sprintf("Queued: %d", queuedCount))
	if costUSD > 0 || totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("Spend: $%.4f (%s tokens)", costUSD, formatTokenCount(totalTokens)))
	}
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
	case daemonstate.WorkItemQueued:
		if item.CurrentStep != "" {
			return item.CurrentStep
		}
		return "(queued)"
	default:
		return item.CurrentStep
	}
}

// formatTokenCount formats a token count with K/M suffix for readability.
func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
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
