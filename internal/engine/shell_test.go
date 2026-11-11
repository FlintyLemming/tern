package engine

import "testing"

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/srv/a":          "/srv/a",
		"":                "''",
		"my photos":       "'my photos'",
		"it's":            `'it'\''s'`,
		`:webdav,url="x"`: `':webdav,url="x"'`,
		"$(rm -rf /)":     "'$(rm -rf /)'",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
