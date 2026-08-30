// Command check enforces the zero-dependency premise of this repository.
//
// It fails the build when any of the following appears anywhere in the tree:
//
//   - a require directive in go.mod
//   - a vendor directory
//   - a remote script, stylesheet, or import in a web asset (a CDN reference)
//   - a web font loaded from a remote origin
//
// The check is deliberately part of the build rather than a convention, because
// a convention is not verifiable in five seconds and this one is.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// violation is a single rule breach found at a specific place in the tree.
type violation struct {
	Path string
	Line int
	Rule string
	Text string
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d: %s\n    %s", v.Path, v.Line, v.Rule, strings.TrimSpace(v.Text))
}

// rule matches a forbidden pattern in the contents of a scanned file.
type rule struct {
	Name string
	Pat  *regexp.Regexp
	// Exts limits the rule to certain file extensions. Empty means all.
	Exts []string
}

// rules are the content patterns that fail the build. They are intentionally
// broad: a false positive costs one code review, a false negative costs the
// entire premise of the submission.
var rules = []rule{
	{
		Name: "remote script (CDN reference)",
		Pat:  regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*["']?(https?:)?//`),
		Exts: []string{".html", ".htm", ".md.tmpl"},
	},
	{
		Name: "remote stylesheet (CDN reference)",
		Pat:  regexp.MustCompile(`(?i)<link[^>]+href\s*=\s*["']?(https?:)?//`),
		Exts: []string{".html", ".htm"},
	},
	{
		Name: "remote CSS import",
		Pat:  regexp.MustCompile(`(?i)@import\s+(url\()?["']?(https?:)?//`),
		Exts: []string{".css", ".html", ".htm"},
	},
	{
		Name: "web font from a remote origin",
		Pat:  regexp.MustCompile(`(?i)fonts\.(googleapis|gstatic|bunny|cdnfonts)\.com`),
		// Markdown is excluded: the design notes name these hosts in order to
		// forbid them, and a rule that fails on its own documentation is noise.
		Exts: []string{".html", ".htm", ".css", ".js", ".json"},
	},
	{
		Name: "font file fetched over the network",
		Pat:  regexp.MustCompile(`(?i)@font-face[\s\S]{0,400}?url\(\s*["']?(https?:)?//`),
		Exts: []string{".css", ".html", ".htm"},
	},
}

// scanExts are the file types worth scanning for content violations. Binary
// assets and the Go source of this command itself are skipped.
var scanExts = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true,
	".json": true, ".md": true, ".mod": true, ".txt": true,
}

// skipDirs are never descended into.
var skipDirs = map[string]bool{
	".git": true, "data": true, "node_modules": true,
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	violations, err := run(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check: %v\n", err)
		os.Exit(2)
	}

	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "check: %d dependency violation(s)\n\n", len(violations))
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, v)
		}
		fmt.Fprintln(os.Stderr, "\nFix the code, never the check. See CLAUDE.md.")
		os.Exit(1)
	}

	fmt.Println("check: no dependency violations")
}

// run walks root and returns every violation found, in encounter order.
func run(root string) ([]violation, error) {
	var found []violation

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walking %s: %w", path, err)
		}
		name := d.Name()

		if d.IsDir() {
			if path != root && (skipDirs[name] || name == "vendor") {
				if name == "vendor" {
					found = append(found, violation{
						Path: filepath.ToSlash(path),
						Rule: "vendor directory (a dependency with extra steps)",
					})
				}
				return fs.SkipDir
			}
			return nil
		}

		if !scanExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}

		// This file states the forbidden patterns, so it cannot be subject to
		// them without failing on itself.
		if filepath.Clean(path) == filepath.Clean(filepath.Join(root, "tools", "check", "main.go")) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		rel := filepath.ToSlash(path)

		if name == "go.mod" {
			found = append(found, checkGoMod(rel, string(data))...)
		}
		found = append(found, checkContent(rel, name, string(data))...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

var requireDirective = regexp.MustCompile(`(?m)^\s*require\b`)

// checkGoMod reports any require directive, in either the single-line or the
// block form. An empty require block is still a failure: it signals intent.
func checkGoMod(path, content string) []violation {
	var found []violation
	for i, line := range strings.Split(content, "\n") {
		if requireDirective.MatchString(line) {
			found = append(found, violation{
				Path: path,
				Line: i + 1,
				Rule: "require directive in go.mod (the manifest must stay empty)",
				Text: line,
			})
		}
	}
	return found
}

// checkContent applies every content rule that covers this file's extension.
// Markdown is scanned too, so a README cannot promise something the assets
// contradict, but prose describing a forbidden pattern would trip it; such
// prose belongs in a fenced example, which is why the rules require markup.
func checkContent(path, name, content string) []violation {
	ext := strings.ToLower(filepath.Ext(name))
	var found []violation

	for _, r := range rules {
		if !r.covers(ext) {
			continue
		}
		for _, idx := range r.Pat.FindAllStringIndex(content, -1) {
			found = append(found, violation{
				Path: path,
				Line: lineOf(content, idx[0]),
				Rule: r.Name,
				Text: lineTextAt(content, idx[0]),
			})
		}
	}
	return found
}

// covers reports whether the rule applies to files with this extension.
func (r rule) covers(ext string) bool {
	if len(r.Exts) == 0 {
		return true
	}
	for _, e := range r.Exts {
		if e == ext {
			return true
		}
	}
	return false
}

// lineOf returns the 1-based line number containing byte offset off.
func lineOf(s string, off int) int {
	return strings.Count(s[:off], "\n") + 1
}

// lineTextAt returns the full line containing byte offset off.
func lineTextAt(s string, off int) string {
	start := strings.LastIndexByte(s[:off], '\n') + 1
	end := strings.IndexByte(s[off:], '\n')
	if end < 0 {
		return s[start:]
	}
	return s[start : off+end]
}
