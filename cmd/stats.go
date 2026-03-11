package cmd

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/session"
)

var statsRepo string

var statsCmd = &cobra.Command{
	Use:     "stats",
	Short:   "Show aggregate session performance analytics",
	GroupID: "daemon",
	Long: `Displays aggregate performance analytics from the orchestrator's persisted state,
including session history, cost tracking, failure analysis, and feedback stats.

Note: stats reflect only items still in the state file. Terminal items older
than the configured max age (default 7 days) are pruned and not shown.

Examples:
  erg stats                     # Show stats for current repo
  erg stats --repo owner/repo   # Show stats for specific repo`,
	RunE: runStats,
}

func init() {
	statsCmd.Flags().StringVar(&statsRepo, "repo", "", "Repo to show stats for (owner/repo or filesystem path)")
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	repo := statsRepo
	if repo == "" {
		sessSvc := session.NewSessionService()
		resolved, err := resolveAgentRepo(context.Background(), "", sessSvc)
		if err != nil {
			repo, err = findSingleRunningDaemon()
			if err != nil {
				return err
			}
		} else {
			repo = resolved
		}
	}

	state, err := daemonstate.LoadDaemonState(repo)
	if err != nil {
		return fmt.Errorf("failed to load orchestrator state: %w", err)
	}

	items := state.GetAllWorkItems()
	stats := computeSessionStats(items)
	formatStats(os.Stdout, stats)
	return nil
}

// SessionStats holds aggregated analytics computed from work items.
type SessionStats struct {
	Total     int
	Completed int
	Failed    int
	Active    int
	Queued    int

	// Time-to-merge for completed items (CreatedAt → CompletedAt)
	MergeDurations []time.Duration

	// Per-item cost data (sorted by CostUSD descending, for display)
	CostItems []WorkItemCostSummary

	// Failure analysis
	FailedItems []daemonstate.WorkItem

	// Feedback rounds (items with FeedbackRounds > 0)
	FeedbackItems []daemonstate.WorkItem

	// Active/queued work in progress
	InProgressItems []daemonstate.WorkItem
}

// WorkItemCostSummary holds cost information for a single work item.
type WorkItemCostSummary struct {
	Label        string
	CostUSD      float64
	TotalTokens  int
	InputTokens  int
	OutputTokens int
}

// computeSessionStats aggregates analytics from a slice of work items.
func computeSessionStats(items []daemonstate.WorkItem) SessionStats {
	var stats SessionStats
	stats.Total = len(items)

	for _, item := range items {
		switch item.State {
		case daemonstate.WorkItemCompleted:
			stats.Completed++
			if item.CompletedAt != nil && !item.CreatedAt.IsZero() {
				stats.MergeDurations = append(stats.MergeDurations, item.CompletedAt.Sub(item.CreatedAt))
			}
		case daemonstate.WorkItemFailed:
			stats.Failed++
			stats.FailedItems = append(stats.FailedItems, item)
		case daemonstate.WorkItemQueued:
			stats.Queued++
			stats.InProgressItems = append(stats.InProgressItems, item)
		default: // active
			stats.Active++
			stats.InProgressItems = append(stats.InProgressItems, item)
		}

		if item.CostUSD > 0 || item.InputTokens > 0 || item.OutputTokens > 0 {
			stats.CostItems = append(stats.CostItems, WorkItemCostSummary{
				Label:        formatWorkItemLabel(item),
				CostUSD:      item.CostUSD,
				TotalTokens:  item.InputTokens + item.OutputTokens,
				InputTokens:  item.InputTokens,
				OutputTokens: item.OutputTokens,
			})
		}

		if item.FeedbackRounds > 0 {
			stats.FeedbackItems = append(stats.FeedbackItems, item)
		}
	}

	// Sort cost items by cost descending
	sort.Slice(stats.CostItems, func(i, j int) bool {
		return stats.CostItems[i].CostUSD > stats.CostItems[j].CostUSD
	})

	return stats
}

// formatWorkItemLabel returns a short display label for a work item.
func formatWorkItemLabel(item daemonstate.WorkItem) string {
	return issueLabel(item.IssueRef, item.ID, 40)
}

// formatStats writes a human-readable stats report to w.
func formatStats(w io.Writer, stats SessionStats) {
	printOverview(w, stats)
	printTimeToMerge(w, stats)
	printTokenSpend(w, stats)
	printFailureAnalysis(w, stats)
	printFeedbackRounds(w, stats)
}

func printOverview(w io.Writer, stats SessionStats) {
	fmt.Fprintln(w, "Overview")
	fmt.Fprintln(w, "────────")

	successRate := 0.0
	denominator := stats.Completed + stats.Failed
	if denominator > 0 {
		successRate = float64(stats.Completed) / float64(denominator) * 100
	}

	fmt.Fprintf(w, "  Sessions:  %d total  (%d completed, %d failed, %d active, %d queued)\n",
		stats.Total, stats.Completed, stats.Failed, stats.Active, stats.Queued)
	if denominator > 0 {
		fmt.Fprintf(w, "  Success:   %.1f%%\n", successRate)
	}

	// Compute avg cost from per-item data
	var totalCost float64
	for _, ci := range stats.CostItems {
		totalCost += ci.CostUSD
	}
	if len(stats.CostItems) > 0 {
		fmt.Fprintf(w, "  Avg cost:  $%.4f per tracked session (%d tracked)  ($%.4f total)\n",
			totalCost/float64(len(stats.CostItems)), len(stats.CostItems), totalCost)
	}

	fmt.Fprintln(w)
}

func printTimeToMerge(w io.Writer, stats SessionStats) {
	if len(stats.MergeDurations) == 0 {
		return
	}

	fmt.Fprintln(w, "Time to Merge")
	fmt.Fprintln(w, "─────────────")

	var total time.Duration
	minD := stats.MergeDurations[0]
	maxD := stats.MergeDurations[0]
	for _, d := range stats.MergeDurations {
		total += d
		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
	}
	avg := total / time.Duration(len(stats.MergeDurations))

	fmt.Fprintf(w, "  Avg:  %s\n", formatDuration(avg))
	fmt.Fprintf(w, "  Min:  %s\n", formatDuration(minD))
	fmt.Fprintf(w, "  Max:  %s\n", formatDuration(maxD))
	fmt.Fprintln(w)
}

func printTokenSpend(w io.Writer, stats SessionStats) {
	if len(stats.CostItems) == 0 {
		return
	}

	fmt.Fprintln(w, "Token Spend (top sessions by cost)")
	fmt.Fprintln(w, "──────────────────────────────────")

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ISSUE\tCOST\tTOKENS")

	limit := len(stats.CostItems)
	if limit > 10 {
		limit = 10
	}
	for _, ci := range stats.CostItems[:limit] {
		fmt.Fprintf(tw, "  %s\t$%.4f\t%s\n",
			ci.Label, ci.CostUSD, formatTokenCount(ci.TotalTokens))
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func printFailureAnalysis(w io.Writer, stats SessionStats) {
	if len(stats.FailedItems) == 0 {
		return
	}

	title := fmt.Sprintf("Failure Analysis (%d failed sessions)", len(stats.FailedItems))
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, strings.Repeat("─", len([]rune(title))))

	// Count by step at failure
	stepCounts := make(map[string]int)
	for _, item := range stats.FailedItems {
		step := item.CurrentStep
		if step == "" {
			step = "(unknown)"
		}
		stepCounts[step]++
	}

	stepNames := make([]string, 0, len(stepCounts))
	for s := range stepCounts {
		stepNames = append(stepNames, s)
	}
	sort.Slice(stepNames, func(i, j int) bool {
		return stepCounts[stepNames[i]] > stepCounts[stepNames[j]]
	})

	fmt.Fprintln(w, "  Step at failure:")
	for _, s := range stepNames {
		fmt.Fprintf(w, "    %-20s %d\n", s, stepCounts[s])
	}

	// Count common error messages
	errCounts := make(map[string]int)
	for _, item := range stats.FailedItems {
		if item.ErrorMessage != "" {
			errCounts[item.ErrorMessage]++
		}
	}
	if len(errCounts) > 0 {
		errMsgs := make([]string, 0, len(errCounts))
		for msg := range errCounts {
			errMsgs = append(errMsgs, msg)
		}
		sort.Slice(errMsgs, func(i, j int) bool {
			return errCounts[errMsgs[i]] > errCounts[errMsgs[j]]
		})

		fmt.Fprintln(w, "  Common errors:")
		limit := len(errMsgs)
		if limit > 5 {
			limit = 5
		}
		for _, msg := range errMsgs[:limit] {
			label := msg
			if len([]rune(label)) > 60 {
				label = string([]rune(label)[:57]) + "..."
			}
			fmt.Fprintf(w, "    %q  %d\n", label, errCounts[msg])
		}
	}

	fmt.Fprintln(w)
}

func printFeedbackRounds(w io.Writer, stats SessionStats) {
	if len(stats.FeedbackItems) == 0 {
		return
	}

	var total, maxR int
	for _, item := range stats.FeedbackItems {
		total += item.FeedbackRounds
		if item.FeedbackRounds > maxR {
			maxR = item.FeedbackRounds
		}
	}
	avg := float64(total) / float64(len(stats.FeedbackItems))

	fmt.Fprintln(w, "Feedback Rounds")
	fmt.Fprintln(w, "───────────────")
	fmt.Fprintf(w, "  Avg: %.1f  Max: %d  (across %d sessions)\n",
		avg, maxR, len(stats.FeedbackItems))
	fmt.Fprintln(w)
}

// formatDuration formats a duration as a human-readable string (e.g. "1h 23m").
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}
	d = d.Round(time.Minute)
	h := int(math.Floor(d.Hours()))
	m := int(d.Minutes()) % 60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
