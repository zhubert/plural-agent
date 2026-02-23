package workflow

import (
	"fmt"
	"strings"
)

// GenerateMermaid produces a mermaid stateDiagram-v2 string from a workflow config.
func GenerateMermaid(cfg *Config) string {
	var sb strings.Builder

	sb.WriteString("stateDiagram-v2\n")
	sb.WriteString(fmt.Sprintf("    [*] --> %s\n", cfg.Start))

	// Walk each state and emit edges
	for name, state := range cfg.States {
		switch state.Type {
		case StateTypeSucceed, StateTypeFail:
			// Terminal states transition to [*]
			sb.WriteString(fmt.Sprintf("    %s --> [*]\n", name))

		case StateTypeTask:
			label := state.Action
			if state.Next != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : %s\n", name, state.Next, label))
			}
			if state.Error != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : error\n", name, state.Error))
			}

			// Emit before-hooks
			if len(state.Before) > 0 {
				hookName := name + "_before"
				sb.WriteString(fmt.Sprintf("    %s --> %s : before hooks\n", hookName, name))
			}

			// Emit after-hooks
			if len(state.After) > 0 {
				hookName := name + "_hooks"
				sb.WriteString(fmt.Sprintf("    %s --> %s : after hooks\n", name, hookName))
			}

		case StateTypeWait:
			label := state.Event
			if state.Timeout != nil {
				label += fmt.Sprintf(" (timeout: %s)", state.Timeout.Duration)
			}
			if state.Next != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : %s\n", name, state.Next, label))
			}
			if state.TimeoutNext != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : timeout\n", name, state.TimeoutNext))
			}
			if state.Error != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : error\n", name, state.Error))
			}

		case StateTypeChoice:
			for _, rule := range state.Choices {
				label := formatChoiceLabel(rule)
				sb.WriteString(fmt.Sprintf("    %s --> %s : %s\n", name, rule.Next, label))
			}
			if state.Default != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : default\n", name, state.Default))
			}

		case StateTypePass:
			if state.Next != "" {
				sb.WriteString(fmt.Sprintf("    %s --> %s : pass\n", name, state.Next))
			}
		}

	}

	return sb.String()
}

// formatChoiceLabel creates a human-readable label for a choice rule.
func formatChoiceLabel(rule ChoiceRule) string {
	if rule.Equals != nil {
		return fmt.Sprintf("%s == %v", rule.Variable, rule.Equals)
	}
	if rule.NotEquals != nil {
		return fmt.Sprintf("%s != %v", rule.Variable, rule.NotEquals)
	}
	if rule.IsPresent != nil {
		if *rule.IsPresent {
			return fmt.Sprintf("%s exists", rule.Variable)
		}
		return fmt.Sprintf("%s not exists", rule.Variable)
	}
	return rule.Variable
}

