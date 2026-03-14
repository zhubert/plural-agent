package issues

import (
	"strings"
	"testing"
)

func TestMarkdownToAsanaHTML(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want string
	}{
		{
			name: "plain text",
			md:   "Hello world",
			want: "<body>Hello world</body>",
		},
		{
			name: "h2 header",
			md:   "## Implementation Plan",
			want: "<body><strong>Implementation Plan</strong></body>",
		},
		{
			name: "h3 header",
			md:   "### Root Cause Analysis",
			want: "<body><strong>Root Cause Analysis</strong></body>",
		},
		{
			name: "bold text",
			md:   "This is **bold** text",
			want: "<body>This is <strong>bold</strong> text</body>",
		},
		{
			name: "italic text",
			md:   "This is *italic* text",
			want: "<body>This is <em>italic</em> text</body>",
		},
		{
			name: "inline code",
			md:   "Use the `foo()` function",
			want: "<body>Use the <code>foo()</code> function</body>",
		},
		{
			name: "fenced code block",
			md:   "Before\n```ruby\ndef hello\n  puts 'hi'\nend\n```\nAfter",
			want: "<body>Before\n<pre>\ndef hello\n  puts &#39;hi&#39;\nend\n</pre>\nAfter</body>",
		},
		{
			name: "unordered list",
			md:   "Items:\n- First\n- Second\n- Third",
			want: "<body>Items:\n<ul>\n<li>First</li>\n<li>Second</li>\n<li>Third</li>\n</ul></body>",
		},
		{
			name: "ordered list",
			md:   "Steps:\n1. First\n2. Second\n3. Third",
			want: "<body>Steps:\n<ol>\n<li>First</li>\n<li>Second</li>\n<li>Third</li>\n</ol></body>",
		},
		{
			name: "link",
			md:   "See [the docs](https://example.com) for details",
			want: `<body>See <a href="https://example.com">the docs</a> for details</body>`,
		},
		{
			name: "plan marker translated",
			md:   "The plan\n<!-- erg:plan -->",
			want: "<body>The plan\n[erg:plan]</body>",
		},
		{
			name: "step marker translated",
			md:   "Comment body\n<!-- erg:step=notify -->",
			want: "<body>Comment body\n[erg:step=notify]</body>",
		},
		{
			name: "HTML entities escaped",
			md:   "Use x < y && y > z",
			want: "<body>Use x &lt; y &amp;&amp; y &gt; z</body>",
		},
		{
			name: "code block preserves special chars",
			md:   "```\nif x < 10 {\n  fmt.Println(\"hello\")\n}\n```",
			want: "<body><pre>\nif x &lt; 10 {\n  fmt.Println(&#34;hello&#34;)\n}\n</pre></body>",
		},
		{
			name: "empty input",
			md:   "",
			want: "<body></body>",
		},
		{
			name: "bold inside list item",
			md:   "- **Important** item\n- Normal item",
			want: "<body><ul>\n<li><strong>Important</strong> item</li>\n<li>Normal item</li>\n</ul></body>",
		},
		{
			name: "asterisk bullet list",
			md:   "* First\n* Second",
			want: "<body><ul>\n<li>First</li>\n<li>Second</li>\n</ul></body>",
		},
		{
			name: "mixed content",
			md:   "## Plan\n\nSome **bold** text and `code`.\n\n- Item one\n- Item two\n\n```\ncode block\n```\n\nDone.",
			want: "<body><strong>Plan</strong>\n\nSome <strong>bold</strong> text and <code>code</code>.\n\n<ul>\n<li>Item one</li>\n<li>Item two</li>\n</ul>\n\n<pre>\ncode block\n</pre>\n\nDone.</body>",
		},
		{
			name: "bold inside backtick code not processed",
			md:   "Use `**not bold**` here",
			want: "<body>Use <code>**not bold**</code> here</body>",
		},
		{
			name: "consecutive italic spans",
			md:   "*first* and *second*",
			want: "<body><em>first</em> and <em>second</em></body>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownToAsanaHTML(tt.md)
			if got != tt.want {
				t.Errorf("markdownToAsanaHTML():\n  got:  %s\n  want: %s", got, tt.want)
			}
		})
	}
}

func TestTranslateMarkersForAsana(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plan marker",
			input: "body\n<!-- erg:plan -->",
			want:  "body\n[erg:plan]",
		},
		{
			name:  "step marker",
			input: "body\n<!-- erg:step=notify -->",
			want:  "body\n[erg:step=notify]",
		},
		{
			name:  "multiple markers",
			input: "<!-- erg:plan -->\n<!-- erg:step=review -->",
			want:  "[erg:plan]\n[erg:step=review]",
		},
		{
			name:  "no markers",
			input: "plain text",
			want:  "plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateMarkersForAsana(tt.input)
			if got != tt.want {
				t.Errorf("translateMarkersForAsana():\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestTranslateMarkersFromAsana(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plan marker",
			input: "body\n[erg:plan]",
			want:  "body\n<!-- erg:plan -->",
		},
		{
			name:  "step marker",
			input: "body\n[erg:step=notify]",
			want:  "body\n<!-- erg:step=notify -->",
		},
		{
			name:  "no markers",
			input: "plain text",
			want:  "plain text",
		},
		{
			name:  "already html comment form (passthrough)",
			input: "body\n<!-- erg:plan -->",
			want:  "body\n<!-- erg:plan -->",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateMarkersFromAsana(tt.input)
			if got != tt.want {
				t.Errorf("translateMarkersFromAsana():\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	// Markers should survive a write->read cycle.
	original := "## Plan\n\nDo the thing.\n\n<!-- erg:plan -->"

	// Write side: convert to Asana HTML.
	html := markdownToAsanaHTML(original)
	if !strings.Contains(html, "[erg:plan]") {
		t.Fatalf("expected [erg:plan] in HTML, got: %s", html)
	}
	if strings.Contains(html, "<!-- erg:plan -->") {
		t.Fatalf("expected no HTML comment marker in HTML output, got: %s", html)
	}

	// Simulate Asana deriving text from html_text: strip tags, keep [erg:plan].
	derivedText := "Plan\n\nDo the thing.\n\n[erg:plan]"

	// Read side: translate back.
	restored := translateMarkersFromAsana(derivedText)
	if !strings.Contains(restored, "<!-- erg:plan -->") {
		t.Fatalf("expected <!-- erg:plan --> after round-trip, got: %s", restored)
	}
}
