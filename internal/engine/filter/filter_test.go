package filter

import (
	"reflect"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, rule := range []string{"*.tmp", "node_modules/", "/cache/", "/a/b/*.log", "/a/**/x.log", "[Tt]humbs.db", "pri$ce", "with space.txt", "文档"} {
		if err := Validate(rule); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", rule, err)
		}
	}
	for _, rule := range []string{"", "a/b", "!x", "#x", ";x", "- x", "+ x", "a{b,c}", "a}", `a\b`, "a\nb", "a\x00b", "/", "//", "/a//b",
		// Spellings the three tools read differently.
		"a**b", "/a/x**", "[!a]b", " x", "x ", "[abc", "/.", "/a/../b", ".."} {
		if err := Validate(rule); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", rule)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		rule, path string
		isDir      bool
		want       bool
	}{
		{"*.tmp", "a/b/x.tmp", false, true},
		{"*.tmp", "x.tmp.bak", false, false},
		{"*.tmp", "x.tmp", false, true},
		{"*.tmp", "cache.tmp/inner.txt", false, true}, // inside an excluded directory
		{"node_modules/", "a/node_modules", true, true},
		{"node_modules/", "a/node_modules", false, false},
		{"node_modules/", "a/node_modules/x.js", false, true},
		{"/cache/", "cache", true, true},
		{"/cache/", "cache", false, false},
		{"/cache/", "a/cache", true, false},
		{"/cache/", "cache/deep/file", false, true},
		{"/cache", "cache", false, true},
		{"/a/**/x.log", "a/b/c/x.log", false, true},
		{"/a/**/x.log", "a/b/x.log", false, true},
		{"/a/*/x.log", "a/b/c/x.log", false, false},
		{"/a/*/x.log", "a/b/x.log", false, true},
		{"/a/b/*.log", "a/b/x.log", false, true},
		{"/a/b/*.log", "z/a/b/x.log", false, false},
		{"?.txt", "ab.txt", false, false},
		{"?.txt", "a.txt", false, true},
		{"[Tt]humbs.db", "p/thumbs.db", false, true},
		{"[Tt]humbs.db", "p/Xhumbs.db", false, false},
		{"*", "a/b", false, true},
	}
	for _, c := range cases {
		if got := Match([]string{c.rule}, c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, %q, dir=%v) = %v, want %v", c.rule, c.path, c.isDir, got, c.want)
		}
	}
	if !Match([]string{"*.bak", "/cache/"}, "cache/x", false) {
		t.Error("the second of two rules did not match")
	}
	if Match(nil, "a", false) {
		t.Error("no rules matched something")
	}
}

func TestToRclone(t *testing.T) {
	got := ToRclone([]string{"*.tmp", "/cache/"})
	if want := []string{"- *.tmp", "- /cache/"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ToRclone = %q, want %q", got, want)
	}
}

func TestToRsync(t *testing.T) {
	in := []string{"*.tmp", "/cache/"}
	got := ToRsync(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("ToRsync = %q, want %q", got, in)
	}
	got[0] = "changed"
	if in[0] != "*.tmp" {
		t.Error("ToRsync returned its argument's backing array")
	}
}

func TestToRestic(t *testing.T) {
	got := ToRestic([]string{"*.tmp", "/cache/", "pri$ce"}, []string{"/sources/a", "/sources/b"})
	want := []string{"*.tmp", "/sources/a/cache", "/sources/b/cache", "pri$$ce"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToRestic = %q, want %q", got, want)
	}
}

func TestToResticDetails(t *testing.T) {
	// A directory-only name rule loses its slash; a source that itself
	// holds pattern characters is taken literally; the root is a source too.
	got := ToRestic([]string{"node_modules/", "/x"}, []string{"/data/a[1]$", "/"})
	want := []string{"node_modules", `/data/a\[1]$$/x`, "/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToRestic = %q, want %q", got, want)
	}
}
