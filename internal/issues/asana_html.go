package issues

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// markdownToAsanaHTML converts a markdown string to Asana's supported HTML
// subset for rich-text comments (stories). Asana supports: <body>, <strong>,
// <em>, <code>, <pre>, <ul>, <ol>, <li>, <a href>.
//
// It also translates HTML-comment markers (<!-- erg:plan -->, <!-- erg:step=X -->)
// to visible text markers ([erg:plan], [erg:step=X]) since Asana rejects HTML
// comments inside html_text.
func markdownToAsanaHTML(md string) string {
	md = translateMarkersForAsana(md)

	lines := strings.Split(md, "\n")
	var out []string

	inCodeBlock := false
	inUL := false
	inOL := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Fenced code block toggle.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inCodeBlock {
				out = append(out, "</pre>")
				inCodeBlock = false
			} else {
				if inUL {
					out = append(out, "</ul>")
					inUL = false
				}
				if inOL {
					out = append(out, "</ol>")
					inOL = false
				}
				out = append(out, "<pre>")
				inCodeBlock = true
			}
			continue
		}

		if inCodeBlock {
			out = append(out, html.EscapeString(line))
			continue
		}

		// Try parsing as list items once, reuse result for both
		// "still in list?" check and item extraction.
		ulText, isUL := parseUnorderedItem(line)
		olText, isOL := parseOrderedItem(line)

		// Close open lists if the line doesn't continue them.
		if inUL && !isUL {
			out = append(out, "</ul>")
			inUL = false
		}
		if inOL && !isOL {
			out = append(out, "</ol>")
			inOL = false
		}

		// ATX headers: ## Header -> <strong>Header</strong>
		if hdr, ok := parseHeader(line); ok {
			out = append(out, "<strong>"+convertInline(html.EscapeString(hdr))+"</strong>")
			continue
		}

		// Unordered list item: - item or * item
		if isUL {
			if !inUL {
				out = append(out, "<ul>")
				inUL = true
			}
			out = append(out, "<li>"+convertInline(html.EscapeString(ulText))+"</li>")
			continue
		}

		// Ordered list item: 1. item
		if isOL {
			if !inOL {
				out = append(out, "<ol>")
				inOL = true
			}
			out = append(out, "<li>"+convertInline(html.EscapeString(olText))+"</li>")
			continue
		}

		// Regular line: convert inline formatting.
		out = append(out, convertInline(html.EscapeString(line)))
	}

	// Close any open blocks.
	if inCodeBlock {
		out = append(out, "</pre>")
	}
	if inUL {
		out = append(out, "</ul>")
	}
	if inOL {
		out = append(out, "</ol>")
	}

	return "<body>" + strings.Join(out, "\n") + "</body>"
}

// --- Marker translation ---

var htmlCommentMarkerRe = regexp.MustCompile(`<!--\s*(erg:\S+?)\s*-->`)

// translateMarkersForAsana converts HTML-comment erg markers to visible text
// form suitable for Asana html_text. e.g. <!-- erg:plan --> -> [erg:plan]
func translateMarkersForAsana(text string) string {
	return htmlCommentMarkerRe.ReplaceAllString(text, "[$1]")
}

var textMarkerRe = regexp.MustCompile(`\[(erg:\S+?)\]`)

// translateMarkersFromAsana converts visible text erg markers back to
// HTML-comment form so the rest of the system can detect them.
// e.g. [erg:plan] -> <!-- erg:plan -->
func translateMarkersFromAsana(text string) string {
	return textMarkerRe.ReplaceAllString(text, "<!-- $1 -->")
}

// --- Header parsing ---

var headerRe = regexp.MustCompile(`^#{1,6}\s+(.+)$`)

func parseHeader(line string) (string, bool) {
	m := headerRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// --- List item parsing ---

var unorderedItemRe = regexp.MustCompile(`^\s*[-*]\s+(.+)$`)

func parseUnorderedItem(line string) (string, bool) {
	m := unorderedItemRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

var orderedItemRe = regexp.MustCompile(`^\s*\d+\.\s+(.+)$`)

func parseOrderedItem(line string) (string, bool) {
	m := orderedItemRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// --- Inline formatting ---
//
// Applied AFTER HTML-escaping, so we need to match on escaped text but
// produce unescaped HTML tags. Inline code is extracted first so that
// formatting inside backticks (e.g. `**not bold**`) is not interpreted.
// Bold must be checked before italic since ** is a prefix of *.

// convertInline applies inline markdown formatting to an already HTML-escaped
// line. Inline code spans are protected from bold/italic processing via
// placeholder substitution.
func convertInline(escaped string) string {
	// Step 1: Extract inline code spans into placeholders so their content
	// is not processed by bold/italic/link regexes.
	var codeSpans []string
	escaped = inlineCodeRe.ReplaceAllStringFunc(escaped, func(match string) string {
		m := inlineCodeRe.FindStringSubmatch(match)
		placeholder := fmt.Sprintf("\x00CODE%d\x00", len(codeSpans))
		codeSpans = append(codeSpans, "<code>"+m[1]+"</code>")
		return placeholder
	})

	// Step 2: Apply remaining inline formatting.
	// Links: [text](url)
	escaped = linkRe.ReplaceAllString(escaped, `<a href="$2">$1</a>`)
	// Bold: **text**
	escaped = boldRe.ReplaceAllString(escaped, `<strong>$1</strong>`)
	// Italic: *text* (lookahead avoids consuming the trailing boundary,
	// so consecutive spans like *a* and *b* both match)
	escaped = italicRe.ReplaceAllString(escaped, `${1}<em>$2</em>`)

	// Step 3: Restore code spans.
	for i, span := range codeSpans {
		escaped = strings.Replace(escaped, fmt.Sprintf("\x00CODE%d\x00", i), span, 1)
	}

	return escaped
}

var (
	linkRe       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	boldRe       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicRe     = regexp.MustCompile(`(^|[^*])\*([^*]+?)\*`)
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
)
