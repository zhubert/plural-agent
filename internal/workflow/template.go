package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExpandTemplates resolves all states of type "template" in cfg, expanding them
// inline. Template states are replaced with a pass state pointing to the
// template's entry point; internal template states are copied into the config
// with a unique namespace prefix; exit ports are wired to the caller's mapped
// local states. Returns the modified config or an error.
//
// baseDir is used to resolve relative template file paths.
func ExpandTemplates(cfg *Config, baseDir string) (*Config, error) {
	return expandTemplatesWithVisited(cfg, baseDir, nil)
}

// expandTemplatesWithVisited is the recursive implementation of ExpandTemplates
// that tracks which template files are currently being expanded (for cycle detection).
func expandTemplatesWithVisited(cfg *Config, baseDir string, visiting map[string]bool) (*Config, error) {
	if visiting == nil {
		visiting = make(map[string]bool)
	}

	// Collect template state names up front to avoid mutating cfg.States while iterating.
	var templateNames []string
	for name, state := range cfg.States {
		if state.Type == StateTypeTemplate {
			templateNames = append(templateNames, name)
		}
	}

	for _, name := range templateNames {
		state := cfg.States[name]

		// Pre-expansion validation.
		if state.Use == "" {
			return nil, fmt.Errorf("template state %q: use is required", name)
		}
		if len(state.Exits) == 0 {
			return nil, fmt.Errorf("template state %q: exits is required", name)
		}
		// Validate that exit values reference states that exist in the calling config.
		for exitName, localState := range state.Exits {
			if _, ok := cfg.States[localState]; !ok {
				return nil, fmt.Errorf("template state %q: exit %q references non-existent local state %q", name, exitName, localState)
			}
		}

		// Compute a canonical key for cycle detection so that equivalent paths
		// (e.g. "tmpl.yaml" vs "./tmpl.yaml") map to the same entry in visiting.
		// Built-in references are already stable identifiers.
		canonicalKey := state.Use
		if !strings.HasPrefix(state.Use, "builtin:") {
			canonicalKey = filepath.Clean(filepath.Join(baseDir, state.Use))
		}

		// Circular reference check.
		if visiting[canonicalKey] {
			return nil, fmt.Errorf("circular template reference: %q is already being expanded", state.Use)
		}

		// Load the template definition.
		tmpl, err := loadTemplate(state.Use, baseDir)
		if err != nil {
			return nil, fmt.Errorf("template state %q: %w", name, err)
		}

		// Validate template structure.
		if tmpl.Entry == "" {
			return nil, fmt.Errorf("template %q: entry is required", state.Use)
		}
		if len(tmpl.States) == 0 {
			return nil, fmt.Errorf("template %q: no states defined", state.Use)
		}
		if _, ok := tmpl.States[tmpl.Entry]; !ok {
			return nil, fmt.Errorf("template %q: entry state %q does not exist in template states", state.Use, tmpl.Entry)
		}

		// Validate that all caller-mapped exits are declared by the template.
		for exitName := range state.Exits {
			if _, ok := tmpl.Exits[exitName]; !ok {
				return nil, fmt.Errorf("template state %q: exit %q is not declared by template %q (available: %s)",
					name, exitName, state.Use, joinKeys(tmpl.Exits))
			}
		}

		// Recursively expand nested templates inside this template first.
		tmplCfg := &Config{States: cloneStateMap(tmpl.States)}
		newVisiting := make(map[string]bool, len(visiting)+1)
		for k, v := range visiting {
			newVisiting[k] = v
		}
		newVisiting[canonicalKey] = true
		expandedTmpl, err := expandTemplatesWithVisited(tmplCfg, baseDir, newVisiting)
		if err != nil {
			return nil, fmt.Errorf("in template %q: %w", state.Use, err)
		}

		// Resolve effective params: template defaults overridden by caller params.
		params := resolveParams(tmpl.Params, state.Params)

		// Generate unique namespace prefix from the local state name.
		// If the sanitized name collides with an already-expanded prefix (e.g.
		// because two state names like "a-b" and "a_b" sanitize identically),
		// append a numeric counter until the prefix is free.
		sanitized := sanitizeName(name)
		prefix := "_t_" + sanitized + "_"
		for counter := 1; ; counter++ {
			collision := false
			for existingName := range cfg.States {
				if strings.HasPrefix(existingName, prefix) {
					collision = true
					break
				}
			}
			if !collision {
				break
			}
			prefix = fmt.Sprintf("_t_%s%d_", sanitized, counter)
		}

		// Copy template states into cfg with the prefix applied.
		for tName, tState := range expandedTmpl.States {
			prefixedName := prefix + tName
			cloned := cloneState(tState)
			// Substitute params into string values of state.Params.
			applyParamSubstitution(cloned, params)
			// Rewrite all internal state references to use the prefix.
			// Use expandedTmpl.States (not tmpl.States) so that refs introduced
			// by nested template expansion (e.g. "_t_inner_step") are also prefixed.
			rewriteStateRefs(cloned, func(ref string) string {
				if _, ok := expandedTmpl.States[ref]; ok {
					return prefix + ref
				}
				return ref
			})
			cfg.States[prefixedName] = cloned
		}

		// Wire exit ports: replace each prefixed terminal state with a pass
		// state that transitions to the caller's mapped local state.
		for exitName, tmplTerminal := range tmpl.Exits {
			callerTarget, mapped := state.Exits[exitName]
			if !mapped {
				// This exit is not mapped by caller — leave the prefixed terminal state as-is.
				continue
			}
			cfg.States[prefix+tmplTerminal] = &State{
				Type: StateTypePass,
				Next: callerTarget,
			}
		}

		// Replace the template state itself with a pass state pointing to the entry.
		cfg.States[name] = &State{
			Type: StateTypePass,
			Next: prefix + tmpl.Entry,
		}
	}

	return cfg, nil
}

// loadTemplate loads a TemplateConfig from a file path or a built-in reference.
// Built-in references have the form "builtin:<name>".
func loadTemplate(use, baseDir string) (*TemplateConfig, error) {
	if strings.HasPrefix(use, "builtin:") {
		builtinName := strings.TrimPrefix(use, "builtin:")
		return builtinTemplate(builtinName)
	}

	// Reject absolute paths — template paths must be relative to the repo root.
	if filepath.IsAbs(use) {
		return nil, fmt.Errorf("absolute template paths are not allowed: %q", use)
	}

	// Resolve relative path against baseDir and check it stays within baseDir.
	baseDirClean := filepath.Clean(baseDir)
	path := filepath.Clean(filepath.Join(baseDirClean, use))
	if path != baseDirClean && !strings.HasPrefix(path, baseDirClean+string(os.PathSeparator)) {
		return nil, fmt.Errorf("template path %q escapes the base directory", use)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("template file %q not found", use)
		}
		return nil, fmt.Errorf("failed to read template file %q: %w", use, err)
	}

	var tmpl TemplateConfig
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("failed to parse template file %q: %w", use, err)
	}

	return &tmpl, nil
}

// builtinTemplate returns a TemplateConfig for a named built-in template.
func builtinTemplate(name string) (*TemplateConfig, error) {
	switch name {
	case "plan":
		return PlanTemplateConfig(), nil
	case "code":
		return CodeTemplateConfig(), nil
	case "pr":
		return PRTemplateConfig(), nil
	case "ci":
		return CITemplateConfig(), nil
	case "review":
		return ReviewTemplateConfig(), nil
	case "merge":
		return MergeTemplateConfig(), nil
	case "asana_move_section":
		return AsanaMoveSectionTemplateConfig(), nil
	case "linear_move_state":
		return LinearMoveStateTemplateConfig(), nil
	case "asana_await_section":
		return AsanaAwaitSectionTemplateConfig(), nil
	case "linear_await_state":
		return LinearAwaitStateTemplateConfig(), nil
	default:
		return nil, fmt.Errorf("unknown built-in template %q (available: plan, code, pr, ci, review, merge, asana_move_section, linear_move_state, asana_await_section, linear_await_state)", name)
	}
}

// resolveParams merges template parameter defaults with caller-supplied overrides.
// Caller overrides take precedence over defaults.
func resolveParams(defs []TemplateParam, overrides map[string]any) map[string]any {
	result := make(map[string]any, len(defs))
	for _, p := range defs {
		if p.Default != nil {
			result[p.Name] = p.Default
		}
	}
	for k, v := range overrides {
		result[k] = v
	}
	return result
}

var paramPlaceholder = regexp.MustCompile(`\{\{(\w+)\}\}`)

// applyParamSubstitution replaces {{param_name}} placeholders in the string
// values of state.Params with the corresponding value from params.
// Only string values within state.Params are substituted.
func applyParamSubstitution(state *State, params map[string]any) {
	if len(state.Params) == 0 || len(params) == 0 {
		return
	}
	for k, v := range state.Params {
		if s, ok := v.(string); ok {
			// When the entire value is a single {{name}} placeholder, replace it
			// with the typed value from params (preserving bool, int, etc.).
			if m := paramPlaceholder.FindStringSubmatch(s); m != nil && m[0] == s {
				if val, ok := params[m[1]]; ok {
					state.Params[k] = val
					continue
				}
			}
			state.Params[k] = substituteParams(s, params)
		}
	}
}

// substituteParams replaces {{name}} placeholders in s with values from params.
func substituteParams(s string, params map[string]any) string {
	return paramPlaceholder.ReplaceAllStringFunc(s, func(match string) string {
		sub := paramPlaceholder.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		if val, ok := params[sub[1]]; ok {
			return fmt.Sprintf("%v", val)
		}
		return match
	})
}

// rewriteStateRefs rewrites all state-name references in a state using the
// provided rename function. Only references in transition fields are rewritten
// (next, error, timeout_next, default, choices[*].next, catch[*].next).
func rewriteStateRefs(state *State, rename func(string) string) {
	if state.Next != "" {
		state.Next = rename(state.Next)
	}
	if state.Error != "" {
		state.Error = rename(state.Error)
	}
	if state.TimeoutNext != "" {
		state.TimeoutNext = rename(state.TimeoutNext)
	}
	if state.Default != "" {
		state.Default = rename(state.Default)
	}
	for i := range state.Choices {
		if state.Choices[i].Next != "" {
			state.Choices[i].Next = rename(state.Choices[i].Next)
		}
	}
	for i := range state.Catch {
		if state.Catch[i].Next != "" {
			state.Catch[i].Next = rename(state.Catch[i].Next)
		}
	}
}

// cloneState returns a deep copy of a State.
func cloneState(s *State) *State {
	if s == nil {
		return nil
	}
	clone := *s
	if s.Params != nil {
		clone.Params = make(map[string]any, len(s.Params))
		for k, v := range s.Params {
			clone.Params[k] = v
		}
	}
	if s.Data != nil {
		clone.Data = make(map[string]any, len(s.Data))
		for k, v := range s.Data {
			clone.Data[k] = v
		}
	}
	if s.Exits != nil {
		clone.Exits = make(map[string]string, len(s.Exits))
		for k, v := range s.Exits {
			clone.Exits[k] = v
		}
	}
	if s.Choices != nil {
		clone.Choices = make([]ChoiceRule, len(s.Choices))
		copy(clone.Choices, s.Choices)
	}
	if s.Catch != nil {
		clone.Catch = make([]CatchConfig, len(s.Catch))
		for i, c := range s.Catch {
			clone.Catch[i] = c
			if c.Errors != nil {
				clone.Catch[i].Errors = make([]string, len(c.Errors))
				copy(clone.Catch[i].Errors, c.Errors)
			}
		}
	}
	if s.Retry != nil {
		clone.Retry = make([]RetryConfig, len(s.Retry))
		for i, r := range s.Retry {
			clone.Retry[i] = r
			if r.Interval != nil {
				iv := *r.Interval
				clone.Retry[i].Interval = &iv
			}
			if r.Errors != nil {
				clone.Retry[i].Errors = make([]string, len(r.Errors))
				copy(clone.Retry[i].Errors, r.Errors)
			}
		}
	}
	if s.Before != nil {
		clone.Before = make([]HookConfig, len(s.Before))
		copy(clone.Before, s.Before)
	}
	if s.After != nil {
		clone.After = make([]HookConfig, len(s.After))
		copy(clone.After, s.After)
	}
	if s.Timeout != nil {
		t := *s.Timeout
		clone.Timeout = &t
	}
	return &clone
}

// cloneStateMap returns a deep copy of a map[string]*State.
func cloneStateMap(m map[string]*State) map[string]*State {
	result := make(map[string]*State, len(m))
	for k, v := range m {
		result[k] = cloneState(v)
	}
	return result
}

// sanitizeName converts a state name into a safe identifier component by
// replacing any non-alphanumeric/underscore character with an underscore.
func sanitizeName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

// joinKeys returns the keys of a map[string]string as a comma-separated string.
func joinKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
