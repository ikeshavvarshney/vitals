package main

import "testing"

func TestCheckGoMod(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{
			name:    "empty manifest",
			content: "module vitals\n\ngo 1.23\n",
			want:    0,
		},
		{
			name:    "single-line require",
			content: "module vitals\n\ngo 1.23\n\nrequire golang.org/x/net v0.1.0\n",
			want:    1,
		},
		{
			name:    "require block",
			content: "module vitals\n\ngo 1.23\n\nrequire (\n\tgolang.org/x/net v0.1.0\n)\n",
			want:    1,
		},
		{
			name:    "empty require block still counts",
			content: "module vitals\n\ngo 1.23\n\nrequire ()\n",
			want:    1,
		},
		{
			name:    "indented require",
			content: "module vitals\n\n  require example.com/x v1.0.0\n",
			want:    1,
		},
		{
			name:    "word require inside a comment is not a directive",
			content: "module vitals\n\n// we require nothing\ngo 1.23\n",
			want:    0,
		},
		{
			name:    "two require lines report twice",
			content: "require a v1.0.0\nrequire b v1.0.0\n",
			want:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkGoMod("go.mod", tt.content)
			if len(got) != tt.want {
				t.Errorf("checkGoMod() = %d violations, want %d\n%v", len(got), tt.want, got)
			}
		})
	}
}

func TestCheckContent(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content string
		want    int
	}{
		{
			name:    "local script is fine",
			file:    "index.html",
			content: `<script src="/b.js" defer></script>`,
			want:    0,
		},
		{
			name:    "relative script is fine",
			file:    "index.html",
			content: `<script src="assets/app.js"></script>`,
			want:    0,
		},
		{
			name:    "https CDN script",
			file:    "index.html",
			content: `<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>`,
			want:    1,
		},
		{
			name:    "protocol-relative CDN script",
			file:    "index.html",
			content: `<script src="//unpkg.com/vue"></script>`,
			want:    1,
		},
		{
			name:    "unquoted CDN script",
			file:    "index.html",
			content: `<script src=https://example.com/a.js></script>`,
			want:    1,
		},
		{
			name:    "remote stylesheet",
			file:    "index.html",
			content: `<link rel="stylesheet" href="https://cdn.example.com/a.css">`,
			want:    1,
		},
		{
			name:    "local stylesheet is fine",
			file:    "index.html",
			content: `<link rel="stylesheet" href="/dash.css">`,
			want:    0,
		},
		{
			name:    "google fonts link trips both the link rule and the font rule",
			file:    "index.html",
			content: `<link href="https://fonts.googleapis.com/css2?family=Inter" rel="stylesheet">`,
			want:    2,
		},
		{
			name:    "remote css import",
			file:    "dash.css",
			content: `@import url("https://fonts.googleapis.com/css2");`,
			want:    2,
		},
		{
			name:    "font-face fetching over the network",
			file:    "dash.css",
			content: "@font-face {\n  font-family: 'X';\n  src: url(https://example.com/x.woff2);\n}",
			want:    1,
		},
		{
			name:    "system font stack is fine",
			file:    "dash.css",
			content: "body { font-family: system-ui, -apple-system, sans-serif; }",
			want:    0,
		},
		{
			name:    "self-hosted font file is fine",
			file:    "dash.css",
			content: "@font-face { font-family: 'X'; src: url(/fonts/x.woff2); }",
			want:    0,
		},
		{
			name:    "html rules do not apply to markdown",
			file:    "README.md",
			content: `<script src="https://vitals.example.com/b.js" defer></script>`,
			want:    0,
		},
		{
			name:    "font hosts named in markdown prose are allowed",
			file:    "DESIGN.md",
			content: "A `fonts.googleapis.com` link is a third-party runtime dependency.",
			want:    0,
		},
		{
			name:    "font host in a javascript asset is not allowed",
			file:    "app.js",
			content: `var u = "https://fonts.gstatic.com/s/inter.woff2";`,
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkContent(tt.file, tt.file, tt.content)
			if len(got) != tt.want {
				t.Errorf("checkContent(%s) = %d violations, want %d\n%v", tt.file, len(got), tt.want, got)
			}
		})
	}
}

func TestLineOf(t *testing.T) {
	const s = "alpha\nbeta\ngamma"
	tests := []struct {
		off  int
		want int
	}{
		{0, 1},
		{4, 1},
		{6, 2},
		{11, 3},
	}
	for _, tt := range tests {
		if got := lineOf(s, tt.off); got != tt.want {
			t.Errorf("lineOf(%d) = %d, want %d", tt.off, got, tt.want)
		}
	}
}

func TestLineTextAt(t *testing.T) {
	const s = "alpha\nbeta\ngamma"
	tests := []struct {
		off  int
		want string
	}{
		{0, "alpha"},
		{6, "beta"},
		{11, "gamma"},
	}
	for _, tt := range tests {
		if got := lineTextAt(s, tt.off); got != tt.want {
			t.Errorf("lineTextAt(%d) = %q, want %q", tt.off, got, tt.want)
		}
	}
}
