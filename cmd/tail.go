package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/logger"
)

// ioctlWinsize is the TIOCGWINSZ ioctl argument struct (Linux/macOS).
type ioctlWinsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

// terminalSize returns the current terminal dimensions (rows, cols).
// Falls back to (24, 80) if size cannot be determined.
func terminalSize() (int, int) {
	ws := &ioctlWinsize{}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(os.Stdout.Fd()),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	if errno != 0 || ws.Row == 0 || ws.Col == 0 {
		return 24, 80
	}
	return int(ws.Row), int(ws.Col)
}

// writeFrameFlickerFree writes content to the terminal without a visible blank flash.
// It moves the cursor to the top-left, overwrites each line in-place (appending
// \033[K to erase any leftover characters to the right), and then erases everything
// below the new content with \033[J. This avoids the blank-screen flicker that
// occurs when using a full clear (\033[2J) before redrawing.
func writeFrameFlickerFree(content string) {
	// Move cursor to home position without clearing the screen.
	fmt.Print("\033[H")
	// Append erase-to-end-of-line before each newline so stale characters
	// from a previously wider line do not bleed through.
	out := strings.ReplaceAll(content, "\n", "\033[K\n")
	fmt.Print(out)
	// Erase from the current cursor position to the end of the screen,
	// removing any lines left over from a previously taller frame.
	fmt.Print("\033[J")
}

// tailStreamMsg is a minimal struct for parsing stream log JSON entries.
type tailStreamMsg struct {
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

// tailToolVerb returns a short display verb for the given tool name.
func tailToolVerb(name string) string {
	switch name {
	case "Read":
		return "Reading"
	case "Edit":
		return "Editing"
	case "Write":
		return "Writing"
	case "Glob", "Grep":
		return "Searching"
	case "Bash":
		return "Running"
	case "Task":
		return "Delegating"
	case "WebFetch":
		return "Fetching"
	case "WebSearch":
		return "Searching"
	case "TodoWrite":
		return "Updating todos"
	default:
		return "Using " + name
	}
}

// tailToolDesc extracts a short description from tool input JSON.
func tailToolDesc(name string, input json.RawMessage) string {
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
	case "Glob":
		field = "pattern"
	case "Grep":
		field = "pattern"
	case "Bash":
		field = "command"
	case "Task":
		field = "description"
	case "WebFetch":
		field = "url"
	case "WebSearch":
		field = "query"
	}
	if field != "" {
		if v, ok := m[field].(string); ok && v != "" {
			// For file paths shorten to last component
			if name == "Read" || name == "Edit" || name == "Write" {
				parts := strings.Split(v, "/")
				v = parts[len(parts)-1]
			}
			if len(v) > 35 {
				v = v[:32] + "..."
			}
			return v
		}
	}
	return ""
}

// readStreamLogLines reads and parses a stream log file for a session,
// returning display lines (text content and tool use summaries).
func readStreamLogLines(sessionID string) ([]string, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("no session ID")
	}
	logPath, err := logger.StreamLogPath(sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	dec := json.NewDecoder(f)
	for {
		var msg tailStreamMsg
		if err := dec.Decode(&msg); err != nil {
			// EOF or parse error — stop reading
			break
		}
		if msg.Type != "assistant" {
			continue
		}
		for _, c := range msg.Message.Content {
			switch c.Type {
			case "text":
				// Split multi-line text into individual lines
				for _, ln := range strings.Split(c.Text, "\n") {
					ln = strings.TrimRight(ln, "\r")
					if strings.TrimSpace(ln) != "" {
						lines = append(lines, ln)
					}
				}
			case "tool_use":
				verb := tailToolVerb(c.Name)
				desc := tailToolDesc(c.Name, c.Input)
				if desc != "" {
					lines = append(lines, fmt.Sprintf("[%s: %s]", verb, desc))
				} else {
					lines = append(lines, fmt.Sprintf("[%s]", verb))
				}
			}
		}
	}
	return lines, nil
}

// fitLine truncates or pads s to exactly width runes.
func fitLine(s string, width int) string {
	runes := []rune(s)
	if len(runes) > width {
		if width > 3 {
			return string(runes[:width-3]) + "..."
		}
		return string(runes[:width])
	}
	// Pad with spaces
	return s + strings.Repeat(" ", width-len(runes))
}

// renderTailView renders the split-screen tail view to w.
// items are the active work items to display; termRows/termCols are terminal dimensions.
func renderTailView(w io.Writer, items []*daemonstate.WorkItem, termRows, termCols int) {
	n := len(items)
	if n == 0 {
		fmt.Fprintln(w, "No active sessions to tail.")
		return
	}

	// Compute column widths — divide terminal evenly, separator between columns
	// Total separators = n-1 (each "│" is 1 char)
	colWidth := (termCols - (n - 1)) / n
	if colWidth < 10 {
		colWidth = 10
	}

	// Content rows available: terminal rows minus 3 header rows (title, step, separator)
	contentRows := termRows - 4
	if contentRows < 1 {
		contentRows = 1
	}

	// Build each column's lines
	type colData struct {
		header    string
		subheader string
		lines     []string
	}
	cols := make([]colData, n)
	for i, item := range items {
		header := formatIssue(item)
		step := item.CurrentStep
		if step == "" {
			step = "pending"
		}
		phase := item.Phase
		if phase == "" {
			phase = "idle"
		}
		cols[i].header = header
		cols[i].subheader = fmt.Sprintf("%s / %s", step, phase)

		logLines, err := readStreamLogLines(item.SessionID)
		if err != nil {
			if item.SessionID == "" {
				cols[i].lines = []string{"(waiting for session)"}
			} else {
				cols[i].lines = []string{"(no log yet)"}
			}
		} else if len(logLines) == 0 {
			cols[i].lines = []string{"(no output yet)"}
		} else {
			// Take last contentRows lines
			if len(logLines) > contentRows {
				logLines = logLines[len(logLines)-contentRows:]
			}
			cols[i].lines = logLines
		}
	}

	// Print header row (issue title)
	for i, col := range cols {
		if i > 0 {
			fmt.Fprint(w, "│")
		}
		fmt.Fprint(w, fitLine(col.header, colWidth))
	}
	fmt.Fprintln(w)

	// Print subheader row (step / phase)
	for i, col := range cols {
		if i > 0 {
			fmt.Fprint(w, "│")
		}
		fmt.Fprint(w, fitLine(col.subheader, colWidth))
	}
	fmt.Fprintln(w)

	// Print separator row
	for i := range cols {
		if i > 0 {
			fmt.Fprint(w, "┼")
		}
		fmt.Fprint(w, strings.Repeat("─", colWidth))
	}
	fmt.Fprintln(w)

	// Print content rows — row-by-row across all columns
	for row := 0; row < contentRows; row++ {
		for i, col := range cols {
			if i > 0 {
				fmt.Fprint(w, "│")
			}
			line := ""
			if row < len(col.lines) {
				line = col.lines[row]
			}
			fmt.Fprint(w, fitLine(line, colWidth))
		}
		fmt.Fprintln(w)
	}
}

// runTailView continuously renders a live split-screen view of active session logs.
func runTailView(repo string) error {
	// Handle Ctrl+C gracefully
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Hide cursor for cleaner output
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h\n")

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// Draw immediately on first run
	if err := drawTailFrame(repo); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			clearScreen()
			return nil
		case <-ticker.C:
			if err := drawTailFrame(repo); err != nil {
				return err
			}
		}
	}
}

// drawTailFrame loads state and renders one frame of the tail view.
func drawTailFrame(repo string) error {
	state, err := daemonstate.LoadDaemonState(repo)
	if err != nil {
		writeFrameFlickerFree(fmt.Sprintf("Error loading state: %v\n", err))
		return nil
	}

	// Collect active items (non-terminal, any state) sorted by creation time
	var items []*daemonstate.WorkItem
	for _, item := range state.WorkItems {
		if !item.IsTerminal() {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	rows, cols := terminalSize()

	// Render into a buffer first, then write to the terminal in one flicker-free
	// pass so the screen never goes blank between frames.
	var buf bytes.Buffer
	if len(items) == 0 {
		fmt.Fprintln(&buf, "No active work items.")
		fmt.Fprintf(&buf, "\n  Updated: %s  (every 1s, Ctrl+C to quit)\n", time.Now().Format("15:04:05"))
	} else {
		renderTailView(&buf, items, rows-2, cols)
		fmt.Fprintf(&buf, "  Updated: %s  (every 1s, Ctrl+C to quit)\n", time.Now().Format("15:04:05"))
	}
	writeFrameFlickerFree(buf.String())
	return nil
}
