package workflow

import (
	"fmt"
	"strings"
)

// WaitStateGuidance generates a human-readable guidance message for a wait state
// that requires human action. Returns empty string for automated wait states
// (ci.complete, ci.wait_for_checks, pr.mergeable) or when guidance is suppressed.
//
// prURL is included in the message for pr.reviewed events so the human can
// navigate directly to the pull request.
//
// If state.Guidance is non-nil, it is used directly (empty string suppresses guidance).
func WaitStateGuidance(state *State, prURL string) string {
	if state.Guidance != nil {
		return *state.Guidance
	}

	params := NewParamHelper(state.Params)

	switch state.Event {
	case "gate.approved":
		return gateApprovedGuidance(params)
	case "plan.user_replied":
		return planUserRepliedGuidance(params)
	case "pr.reviewed":
		return prReviewedGuidance(prURL)
	case "asana.in_section":
		section := params.String("section", "")
		if section != "" {
			return fmt.Sprintf("Move this task to the **%s** section in Asana to proceed.", section)
		}
		return "Move this task to the required section in Asana to proceed."
	case "linear.in_state":
		ls := params.String("state", "")
		if ls != "" {
			return fmt.Sprintf("Move this issue to the **%s** state in Linear to proceed.", ls)
		}
		return "Move this issue to the required state in Linear to proceed."
	default:
		// ci.complete, ci.wait_for_checks, pr.mergeable — automated, no human action needed
		return ""
	}
}

func gateApprovedGuidance(params *ParamHelper) string {
	trigger := params.String("trigger", "label_added")
	switch trigger {
	case "comment_match":
		pattern := params.String("pattern", "")
		if pattern != "" {
			return fmt.Sprintf("Reply to this issue with a comment matching: `%s`", pattern)
		}
		return "Reply to this issue with an approval comment to proceed."
	default: // label_added
		label := params.String("label", "approved")
		return fmt.Sprintf("Add the label **`%s`** to this issue to proceed.", label)
	}
}

func planUserRepliedGuidance(params *ParamHelper) string {
	approvalPattern := params.String("approval_pattern", "")
	if approvalPattern != "" {
		examples := patternExamples(approvalPattern)
		if len(examples) > 0 {
			return fmt.Sprintf(
				"The plan above is ready for your review. Reply with your feedback to request changes, "+
					"or reply with an approval (e.g. %s) to proceed.",
				strings.Join(examples, ", "),
			)
		}
	}
	return "The plan above is ready for your review. Reply with your feedback or approval to proceed."
}

func prReviewedGuidance(prURL string) string {
	if prURL != "" {
		return fmt.Sprintf("A pull request is ready for your review: %s\n\nPlease review and approve it to proceed.", prURL)
	}
	return "A pull request is ready for your review. Please review and approve it to proceed."
}

// patternExamples extracts simple literal alternatives from a regex pattern like
// "(?i)(LGTM|looks good|approved?|proceed)" and returns up to 3 as quoted strings.
// Returns nil if the pattern is too complex to parse usefully.
func patternExamples(pattern string) []string {
	s := pattern
	s = strings.TrimPrefix(s, "(?i)")
	s = strings.Trim(s, "^$")
	s = strings.Trim(s, "()")

	parts := strings.Split(s, "|")
	if len(parts) == 0 {
		return nil
	}

	var examples []string
	for _, p := range parts {
		clean := strings.TrimRight(p, "?")
		if !hasComplexRegex(clean) {
			examples = append(examples, fmt.Sprintf("%q", clean))
		}
		if len(examples) == 3 {
			break
		}
	}
	return examples
}

func hasComplexRegex(s string) bool {
	for _, c := range s {
		switch c {
		case '.', '*', '+', '[', ']', '{', '}', '\\', '^', '$', '(', ')':
			return true
		}
	}
	return false
}
