// Command doccheck checks local inline Markdown links, image sources and
// Markdown heading anchors in tracked and non-ignored new documentation.
// It does not fetch external URLs or parse arbitrary HTML or Markdown extensions.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	inlineLink = regexp.MustCompile(`!?\[[^\]\n]*\]\(([^\s)]+)(?:\s+"[^"]*")?\)`)
	imageSrc   = regexp.MustCompile(`(?:src|srcset)="([^"\s]+)"`)
	heading    = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.+?)\s*#*\s*$`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	output, err := exec.CommandContext(context.Background(), "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.md").Output()
	if err != nil {
		return fmt.Errorf("list documentation from repository root: %w", err)
	}
	files := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	slices.Sort(files)
	files = slices.Compact(files)
	var failures []error
	for _, file := range files {
		if file == "" {
			continue
		}
		if err := checkFile(file); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	fmt.Printf("Checked local links and heading anchors in %d Markdown files. External URLs were not fetched.\n", len(files))
	return nil
}

func checkFile(file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	var failures []error
	for i, line := range proseLines(string(raw)) {
		links := inlineLink.FindAllStringSubmatch(line, -1)
		links = append(links, imageSrc.FindAllStringSubmatch(line, -1)...)
		for _, link := range links {
			if err := checkTarget(file, link[1]); err != nil {
				failures = append(failures, fmt.Errorf("%s:%d: %s: %w", file, i+1, link[1], err))
			}
		}
	}
	return errors.Join(failures...)
}

func checkTarget(source, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid link: %w", err)
	}
	if u.Scheme != "" || u.Host != "" {
		return nil
	}
	path := source
	if u.Path != "" {
		if strings.HasPrefix(u.Path, "/") {
			path = filepath.FromSlash(strings.TrimPrefix(u.Path, "/"))
		} else {
			path = filepath.Join(filepath.Dir(source), filepath.FromSlash(u.Path))
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	if u.Fragment == "" || info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read anchor destination: %w", err)
	}
	if !headingIDs(string(raw))[u.Fragment] {
		return fmt.Errorf("heading anchor #%s not found in %s", u.Fragment, path)
	}
	return nil
}

// proseLines keeps line numbers while excluding fenced code examples.
func proseLines(text string) []string {
	lines := strings.Split(text, "\n")
	fence := byte(0)
	fenceLength := 0
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 {
					fence, fenceLength = trimmed[0], n
				} else if trimmed[0] == fence && n >= fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
					fence = 0
				}
				lines[i] = ""
				continue
			}
		}
		if fence != 0 {
			lines[i] = ""
		}
	}
	return lines
}

func headingIDs(text string) map[string]bool {
	ids := make(map[string]bool)
	for _, line := range proseLines(text) {
		match := heading.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		// These docs use ATX headings and plain/code-formatted heading text.
		// Match GitHub's lowercase, punctuation removal and space replacement.
		var slug strings.Builder
		for _, r := range strings.ToLower(strings.TrimSpace(match[1])) {
			switch {
			case unicode.IsSpace(r):
				slug.WriteByte('-')
			case unicode.IsLetter(r), unicode.IsNumber(r), unicode.IsMark(r), r == '-', r == '_':
				slug.WriteRune(r)
			}
		}
		base, id := slug.String(), slug.String()
		for n := 1; ids[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		ids[id] = true
	}
	return ids
}
