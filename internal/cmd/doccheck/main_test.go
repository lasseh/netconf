package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeadingIDs(t *testing.T) {
	text := "# An API\n## After `ErrCommitUnknown`\n## What's next?\n## An API\n```md\n# Not a heading\n```\n## Æøå\n"
	ids := headingIDs(text)
	for _, want := range []string{"an-api", "after-errcommitunknown", "whats-next", "an-api-1", "æøå"} {
		if !ids[want] {
			t.Errorf("missing anchor %q in %v", want, ids)
		}
	}
	if ids["not-a-heading"] {
		t.Error("code fence created a heading anchor")
	}
}

func TestCheckFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.md")
	destination := filepath.Join(dir, "destination.md")
	if err := os.WriteFile(destination, []byte("# First read\n## After `ErrCommitUnknown`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"relative heading", "[Read](destination.md#first-read)", false},
		{"own heading", "# Start\n[Start](#start)", false},
		{"code heading", "[Recovery](destination.md#after-errcommitunknown)", false},
		{"external", "[Reference](https://example.invalid/missing#missing)", false},
		{"missing file", "[Read](missing.md)", true},
		{"missing heading", "[Read](destination.md#missing)", true},
		{"fenced link", "```md\n[Example](missing.md)\n```", false},
		{"tilde fence", "~~~~md\n[Example](missing.md)\n~~~\n[Still code](missing.md)\n~~~~", false},
		{"image", `<img src="missing.png">`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(source, []byte(tt.text), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := checkFile(source); (err != nil) != tt.wantErr {
				t.Fatalf("got %v, want error=%t", err, tt.wantErr)
			}
		})
	}
}
