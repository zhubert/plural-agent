package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/logger"
)

// Snapshot is the full dashboard state sent to clients.
type Snapshot struct {
	Daemons   []DaemonInfo `json:"daemons"`
	Timestamp time.Time    `json:"timestamp"`
}

// DaemonInfo holds the state of a single daemon.
type DaemonInfo struct {
	Repo          string         `json:"repo"`
	PID           int            `json:"pid"`
	Running       bool           `json:"running"`
	UptimeSeconds int            `json:"uptime_seconds"`
	CostUSD       float64        `json:"cost_usd"`
	InputTokens   int            `json:"input_tokens"`
	OutputTokens  int            `json:"output_tokens"`
	LastPollAt    time.Time      `json:"last_poll_at"`
	WorkItems     []WorkItemInfo `json:"work_items"`
	SlotCount     int            `json:"slot_count"`
}

// WorkItemInfo holds the state of a single work item.
type WorkItemInfo struct {
	ID                string          `json:"id"`
	IssueRef          config.IssueRef `json:"issue_ref"`
	State             string          `json:"state"`
	CurrentStep       string          `json:"current_step"`
	Phase             string          `json:"phase"`
	SessionID         string          `json:"session_id"`
	Branch            string          `json:"branch"`
	PRURL             string          `json:"pr_url,omitempty"`
	CommentsAddressed int             `json:"comments_addressed"`
	FeedbackRounds    int             `json:"feedback_rounds"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	ErrorCount        int             `json:"error_count"`
	CostUSD           float64         `json:"cost_usd"`
	InputTokens       int             `json:"input_tokens"`
	OutputTokens      int             `json:"output_tokens"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	StepEnteredAt     time.Time       `json:"step_entered_at"`
	Repo              string          `json:"repo,omitempty"`
}

// CollectAll discovers all running daemons and gathers their state.
func CollectAll() (*Snapshot, error) {
	daemons, err := daemonstate.DiscoverRunning()
	if err != nil {
		return nil, fmt.Errorf("discovering daemons: %w", err)
	}

	snap := &Snapshot{
		Timestamp: time.Now(),
		Daemons:   make([]DaemonInfo, 0, len(daemons)),
	}

	for _, d := range daemons {
		state, err := daemonstate.LoadDaemonState(d.Key)
		if err != nil {
			continue
		}

		costUSD, outputTokens, inputTokens := state.GetSpend()

		// Prefer human-readable labels (e.g. "zhubert/erg, zhubert/plural") over
		// raw filesystem paths or opaque daemon IDs.
		repoDisplay := state.RepoPath
		repoLabels, repoPathLabels := state.GetRepoLabels()
		if len(repoLabels) > 0 {
			repoDisplay = strings.Join(repoLabels, ", ")
		}

		info := DaemonInfo{
			Repo:          repoDisplay,
			PID:           d.PID,
			Running:       true,
			UptimeSeconds: int(time.Since(state.StartedAt).Seconds()),
			CostUSD:       costUSD,
			InputTokens:   inputTokens,
			OutputTokens:  outputTokens,
			LastPollAt:    state.GetLastPollAt(),
			SlotCount:     state.ActiveSlotCount(),
		}

		allItems := state.GetAllWorkItems()
		info.WorkItems = make([]WorkItemInfo, 0, len(allItems))
		for _, item := range allItems {
			// Resolve per-item repo label from the _repo_path stored in StepData.
			itemRepo := ""
			if rp, ok := item.StepData["_repo_path"].(string); ok && rp != "" {
				if label, ok := repoPathLabels[rp]; ok && label != "" {
					itemRepo = label
				} else {
					itemRepo = rp
				}
			}
			info.WorkItems = append(info.WorkItems, WorkItemInfo{
				ID:                item.ID,
				IssueRef:          item.IssueRef,
				State:             string(item.State),
				CurrentStep:       item.CurrentStep,
				Phase:             item.Phase,
				SessionID:         item.SessionID,
				Branch:            item.Branch,
				PRURL:             item.PRURL,
				CommentsAddressed: item.CommentsAddressed,
				FeedbackRounds:    item.FeedbackRounds,
				ErrorMessage:      item.ErrorMessage,
				ErrorCount:        item.ErrorCount,
				CostUSD:           item.CostUSD,
				InputTokens:       item.InputTokens,
				OutputTokens:      item.OutputTokens,
				CreatedAt:         item.CreatedAt,
				UpdatedAt:         item.UpdatedAt,
				CompletedAt:       item.CompletedAt,
				StepEnteredAt:     item.StepEnteredAt,
				Repo:              itemRepo,
			})
		}

		snap.Daemons = append(snap.Daemons, info)
	}

	return snap, nil
}

// streamLogMsg is a minimal struct for parsing stream log JSON entries.
type streamLogMsg struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// LogLine represents a single parsed log line for display.
type LogLine struct {
	Type string `json:"type"`           // "text" or "tool"
	Text string `json:"text"`           // tool arg/description, or text body
	Name string `json:"name,omitempty"` // tool name (tool lines only)
}

// ReadSessionLog reads and parses the stream log for a session.
// The stream log contains pretty-printed (multi-line) JSON objects,
// so we use json.Decoder to handle arbitrary formatting correctly.
// Non-JSON lines (e.g. raw process output) are skipped gracefully.
func ReadSessionLog(sessionID string, tailN int) ([]LogLine, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("no session ID")
	}
	logPath, err := logger.StreamLogPath(sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}

	var lines []LogLine

	for len(data) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		var msg streamLogMsg
		if err := dec.Decode(&msg); err != nil {
			// Skip non-JSON content: advance past the current line
			// and retry from the next line.
			idx := bytes.IndexByte(data, '\n')
			if idx < 0 {
				break
			}
			data = data[idx+1:]
			continue
		}
		// Advance data past what the decoder consumed.
		consumed := int(dec.InputOffset())
		data = data[consumed:]

		if msg.Type != "assistant" {
			continue
		}
		for _, c := range msg.Message.Content {
			switch c.Type {
			case "text":
				for ln := range strings.SplitSeq(c.Text, "\n") {
					ln = strings.TrimRight(ln, "\r")
					if strings.TrimSpace(ln) != "" {
						lines = append(lines, LogLine{Type: "text", Text: ln})
					}
				}
			case "tool_use":
				desc := toolDesc(c.Name, c.Input)
				lines = append(lines, LogLine{Type: "tool", Name: c.Name, Text: desc})
			}
		}
	}

	if tailN > 0 && len(lines) > tailN {
		lines = lines[len(lines)-tailN:]
	}
	return lines, nil
}

// toolDesc extracts a short description from tool input JSON.
func toolDesc(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	field := ""
	switch name {
	case "Read", "Edit", "Write":
		field = "file_path"
	case "Glob", "Grep":
		field = "pattern"
	case "Bash":
		field = "command"
	case "WebFetch":
		field = "url"
	case "WebSearch":
		field = "query"
	}
	if field != "" {
		if v, ok := m[field].(string); ok && v != "" {
			if name == "Read" || name == "Edit" || name == "Write" {
				parts := strings.Split(v, "/")
				v = parts[len(parts)-1]
			}
			if runes := []rune(v); len(runes) > 50 {
				v = string(runes[:47]) + "..."
			}
			return v
		}
	}
	return ""
}
