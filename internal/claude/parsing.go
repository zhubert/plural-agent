package claude

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// PermissionDenial represents a permission that was denied during the session.
// This is reported in the result message's permission_denials array.
type PermissionDenial struct {
	Tool        string `json:"tool"`        // Tool name that was denied (e.g., "Bash", "Edit")
	Description string `json:"description"` // Human-readable description of what was requested
	Reason      string `json:"reason"`      // Why it was denied (optional)
}

// streamMessage represents a JSON message from Claude's stream-json output
type streamMessage struct {
	Type            string `json:"type"`               // "system", "assistant", "user", "result"
	Subtype         string `json:"subtype"`            // "init", "success", etc.
	ParentToolUseID string `json:"parent_tool_use_id"` // Non-empty when message is from a subagent (e.g., Haiku via Task)
	Message         struct {
		ID      string `json:"id,omitempty"`    // Message ID for tracking API calls
		Model   string `json:"model,omitempty"` // Model that generated this message (e.g., "claude-haiku-4-5-20251001")
		Content []struct {
			Type      string          `json:"type"`         // "text", "tool_use", "tool_result"
			ID        string          `json:"id,omitempty"` // tool use ID (for tool_use)
			Text      string          `json:"text,omitempty"`
			Name      string          `json:"name,omitempty"`        // tool name
			Input     json.RawMessage `json:"input,omitempty"`       // tool input
			ToolUseID string          `json:"tool_use_id,omitempty"` // tool use ID reference (for tool_result)
			ToolUseId string          `json:"toolUseId,omitempty"`   // camelCase variant from Claude CLI
		} `json:"content"`
		Usage *StreamUsage `json:"usage,omitempty"` // Token usage (for assistant messages)
	} `json:"message"`
	Result            string                      `json:"result,omitempty"`             // Final result text
	Error             string                      `json:"error,omitempty"`              // Error message (alternative to result)
	Errors            []string                    `json:"errors,omitempty"`             // Error messages array (used by error_during_execution)
	PermissionDenials []PermissionDenial          `json:"permission_denials,omitempty"` // Permissions denied during session
	SessionID         string                      `json:"session_id,omitempty"`
	DurationMs        int                         `json:"duration_ms,omitempty"`     // Total duration in milliseconds
	DurationAPIMs     int                         `json:"duration_api_ms,omitempty"` // API duration in milliseconds
	NumTurns          int                         `json:"num_turns,omitempty"`       // Number of conversation turns
	TotalCostUSD      float64                     `json:"total_cost_usd,omitempty"`  // Total cost in USD
	Usage             *StreamUsage                `json:"usage,omitempty"`           // Token usage breakdown
	ModelUsage        map[string]*ModelUsageEntry `json:"modelUsage,omitempty"`      // Per-model usage breakdown (includes sub-agents)
}

// parseStreamMessage parses a JSON line from Claude's stream-json output
// and returns zero or more ResponseChunks representing the message content.
func parseStreamMessage(line string, log *slog.Logger) []ResponseChunk {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	// Skip lines that aren't JSON objects. Claude CLI with --verbose may output
	// non-JSON informational lines to stdout that we should silently ignore.
	if !strings.HasPrefix(line, "{") {
		log.Debug("skipping non-JSON line from Claude CLI", "line", truncateForLog(line))
		return nil
	}

	var msg streamMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		log.Warn("failed to parse stream message", "error", err, "line", truncateForLog(line))
		return nil
	}

	if msg.Type == "" {
		log.Warn("unrecognized JSON message type", "line", truncateForLog(line))
		return nil
	}

	var chunks []ResponseChunk

	switch msg.Type {
	case "system":
		if msg.Subtype == "init" {
			log.Debug("session initialized")
		}

	case "assistant":
		for _, content := range msg.Message.Content {
			switch content.Type {
			case "text":
				if content.Text != "" {
					chunks = append(chunks, ResponseChunk{
						Type:    ChunkTypeText,
						Content: content.Text,
					})
				}
			case "tool_use":
				inputDesc := extractToolInputDescription(content.Name, content.Input)
				chunks = append(chunks, ResponseChunk{
					Type:      ChunkTypeToolUse,
					ToolName:  content.Name,
					ToolInput: inputDesc,
					ToolUseID: content.ID,
				})
				log.Debug("tool use", "tool", content.Name, "id", content.ID, "input", inputDesc)
			}
		}

	case "user":
		// User messages in stream-json are tool results — logged but not emitted as chunks
		for _, content := range msg.Message.Content {
			toolUseID := content.ToolUseID
			if toolUseID == "" {
				toolUseID = content.ToolUseId
			}
			if content.Type == "tool_result" || toolUseID != "" {
				log.Debug("tool result received", "toolUseID", toolUseID)
			}
		}

	case "result":
		log.Debug("result received", "subtype", msg.Subtype, "result", msg.Result)
	}

	return chunks
}

// toolInputConfig defines how to extract a description from a tool's input.
type toolInputConfig struct {
	Field       string // JSON field to extract
	ShortenPath bool   // Whether to shorten file paths to just filename
	MaxLen      int    // Maximum length before truncation (0 = no limit)
}

// toolInputConfigs maps tool names to their input extraction configuration.
var toolInputConfigs = map[string]toolInputConfig{
	"Read":      {Field: "file_path", ShortenPath: true},
	"Edit":      {Field: "file_path", ShortenPath: true},
	"Write":     {Field: "file_path", ShortenPath: true},
	"Glob":      {Field: "pattern"},
	"Grep":      {Field: "pattern", MaxLen: 30},
	"WebSearch": {Field: "query"},
	"Bash":      {Field: "command", MaxLen: 40},
	"Task":      {Field: "description"},
	"WebFetch":  {Field: "url", MaxLen: 40},
}

// DefaultToolInputMaxLen is the default max length for tool descriptions.
const DefaultToolInputMaxLen = 40

// extractToolInputDescription extracts a brief, human-readable description from tool input.
func extractToolInputDescription(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}

	var inputMap map[string]any
	if err := json.Unmarshal(input, &inputMap); err != nil {
		return ""
	}

	if cfg, ok := toolInputConfigs[toolName]; ok {
		if value, exists := inputMap[cfg.Field].(string); exists {
			return formatToolInput(value, cfg.ShortenPath, cfg.MaxLen)
		}
	}

	// Default: return first string value found
	for _, v := range inputMap {
		if s, ok := v.(string); ok && s != "" {
			return truncateString(s, DefaultToolInputMaxLen)
		}
	}
	return ""
}

// formatToolInput formats a tool input value according to the config.
func formatToolInput(value string, shorten bool, maxLen int) string {
	if shorten {
		value = shortenPath(value)
	}
	if maxLen > 0 {
		value = truncateString(value, maxLen)
	}
	return value
}

// truncateString truncates a string to maxLen characters, including "..." suffix.
func truncateString(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// shortenPath returns just the filename or last path component
func shortenPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

// FormatToolIcon returns a human-readable verb for the tool type.
func FormatToolIcon(toolName string) string {
	switch toolName {
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
	default:
		return "Using " + toolName
	}
}

// truncateForLog truncates long strings for log messages
func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
