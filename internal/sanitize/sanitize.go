// Package sanitize provides defenses against prompt injection in untrusted
// content (issue bodies, PR comments, user feedback) before it is sent to
// an LLM. The approach is defense-in-depth: strip hidden content that a
// human author would not see, and wrap untrusted text in clear delimiters
// so the model can distinguish instructions from data.
package sanitize

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// UntrustedContent sanitizes a string of untrusted user content and wraps it
// in delimiters that clearly mark it as user-provided data (not instructions).
// The tag parameter names the content (e.g. "issue_body", "review_comment").
func UntrustedContent(tag, content string) string {
	if content == "" {
		return ""
	}
	cleaned := StripHidden(content)
	return WrapUserContent(tag, cleaned)
}

// StripHidden removes content that is invisible to human readers but could
// carry hidden prompt-injection payloads:
//   - HTML comments (<!-- ... -->)
//   - Zero-width and other invisible Unicode characters
//   - HTML tags that hide content (<details style="display:none">, etc.)
//   - Right-to-left override characters that can mask text direction
func StripHidden(s string) string {
	s = stripHTMLComments(s)
	s = stripHiddenHTMLBlocks(s)
	s = stripInvisibleUnicode(s)
	return s
}

// WrapUserContent wraps content in XML-style delimiters that signal to the
// LLM that the enclosed text is untrusted user data, not system instructions.
func WrapUserContent(tag, content string) string {
	return fmt.Sprintf("<user-content type=%q>\n%s\n</user-content>", tag, content)
}

// htmlCommentRE matches HTML comments including multiline ones.
var htmlCommentRE = regexp.MustCompile(`<!--[\s\S]*?-->`)

func stripHTMLComments(s string) string {
	return htmlCommentRE.ReplaceAllString(s, "")
}

// hiddenStyleRE matches a style attribute containing display:none or visibility:hidden.
var hiddenStyleRE = regexp.MustCompile(
	`(?i)(?:display\s*:\s*none|visibility\s*:\s*hidden|font-size\s*:\s*0)`,
)

// htmlTagPairRE matches a complete opening + closing tag pair for common block/inline elements.
// Go's regexp2 doesn't support backreferences, so we enumerate the tags we care about.
var htmlTagPairs = []struct {
	open  *regexp.Regexp
	close string
}{
	{regexp.MustCompile(`(?i)<div\s[^>]*>`), "</div>"},
	{regexp.MustCompile(`(?i)<span\s[^>]*>`), "</span>"},
	{regexp.MustCompile(`(?i)<p\s[^>]*>`), "</p>"},
	{regexp.MustCompile(`(?i)<section\s[^>]*>`), "</section>"},
	{regexp.MustCompile(`(?i)<article\s[^>]*>`), "</article>"},
}

func stripHiddenHTMLBlocks(s string) string {
	for _, pair := range htmlTagPairs {
		for {
			loc := pair.open.FindStringIndex(s)
			if loc == nil {
				break
			}
			openTag := s[loc[0]:loc[1]]
			if !hiddenStyleRE.MatchString(openTag) {
				// This tag isn't hidden — skip past it to avoid infinite loop.
				// We search the remainder of the string on the next iteration.
				break
			}
			closeIdx := strings.Index(s[loc[1]:], pair.close)
			if closeIdx < 0 {
				// No closing tag found — strip from open tag to end.
				s = s[:loc[0]]
				break
			}
			end := loc[1] + closeIdx + len(pair.close)
			s = s[:loc[0]] + s[end:]
		}
	}
	return s
}

// invisibleRunes is the set of Unicode code points that are invisible to
// readers but could carry hidden information. Includes:
//   - Zero-width characters (U+200B, U+200C, U+200D, U+FEFF)
//   - Directional overrides (U+200E, U+200F, U+202A-U+202E, U+2066-U+2069)
//   - Word joiner (U+2060)
//   - Invisible separator/operator characters
//   - Tag characters (U+E0001-U+E007F) used in Unicode steganography
var invisibleRunes = map[rune]bool{
	'\u200B': true, // ZERO WIDTH SPACE
	'\u200C': true, // ZERO WIDTH NON-JOINER
	'\u200D': true, // ZERO WIDTH JOINER
	'\uFEFF': true, // ZERO WIDTH NO-BREAK SPACE (BOM)
	'\u200E': true, // LEFT-TO-RIGHT MARK
	'\u200F': true, // RIGHT-TO-LEFT MARK
	'\u202A': true, // LEFT-TO-RIGHT EMBEDDING
	'\u202B': true, // RIGHT-TO-LEFT EMBEDDING
	'\u202C': true, // POP DIRECTIONAL FORMATTING
	'\u202D': true, // LEFT-TO-RIGHT OVERRIDE
	'\u202E': true, // RIGHT-TO-LEFT OVERRIDE
	'\u2060': true, // WORD JOINER
	'\u2061': true, // FUNCTION APPLICATION
	'\u2062': true, // INVISIBLE TIMES
	'\u2063': true, // INVISIBLE SEPARATOR
	'\u2064': true, // INVISIBLE PLUS
	'\u2066': true, // LEFT-TO-RIGHT ISOLATE
	'\u2067': true, // RIGHT-TO-LEFT ISOLATE
	'\u2068': true, // FIRST STRONG ISOLATE
	'\u2069': true, // POP DIRECTIONAL ISOLATE
	'\u00AD': true, // SOFT HYPHEN
	'\u034F': true, // COMBINING GRAPHEME JOINER
	'\u061C': true, // ARABIC LETTER MARK
	'\u180E': true, // MONGOLIAN VOWEL SEPARATOR
}

func stripInvisibleUnicode(s string) string {
	return strings.Map(func(r rune) rune {
		if invisibleRunes[r] {
			return -1
		}
		// Strip Unicode tag characters (U+E0001 to U+E007F) used in
		// steganographic encoding of ASCII in invisible Unicode.
		if r >= 0xE0001 && r <= 0xE007F {
			return -1
		}
		// Strip characters from the "Format" (Cf) category that aren't
		// already handled above and aren't common whitespace.
		if unicode.Is(unicode.Cf, r) && !invisibleRunes[r] && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, s)
}
