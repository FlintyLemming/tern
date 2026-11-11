// Package filter is tern's one exclude-rule syntax and its translation for
// restic, rclone and rsync, so a rule means the same whichever tool runs.
//
// A rule is a glob: "*" matches any run of characters except "/", "?" one
// such character, "[abc]" a set, and "**" as a whole path segment crosses
// directories. There are two kinds:
//
//   - name rules have no "/" (a trailing one aside) and match a file or
//     directory name at any depth: "*.tmp", "node_modules/";
//   - anchored rules start with "/" and match from the sync root (for a
//     backup: from each source directory): "/cache/", "/a/**/x.log".
//
// A trailing "/" restricts the rule to directories. Whatever a rule matches
// is excluded together with everything beneath it.
//
// Known differences between the tools, which the syntax cannot hide:
//
//   - restic has no "directories only": "name/" is handled as "name" in a
//     backup, so a plain file of that name is excluded too;
//   - "/a/**/x.log" needs at least one directory between a and x.log for
//     rclone and rsync (and for Match); restic also accepts none.
package filter

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

type rule struct {
	segs     []string
	anchored bool
	dirOnly  bool
}

func parse(r string) (rule, error) {
	switch {
	case r == "":
		return rule{}, errors.New("filter: empty rule")
	case !utf8.ValidString(r) || strings.IndexFunc(r, unicode.IsControl) >= 0:
		return rule{}, errors.New("filter: rule has control characters")
	case strings.ContainsAny(r, `{}\`):
		return rule{}, fmt.Errorf(`filter: %q uses "{", "}" or "\", which are not supported`, r)
	case strings.TrimSpace(r) != r:
		return rule{}, fmt.Errorf("filter: %q starts or ends with white space", r)
	case strings.ContainsAny(r[:1], "!#;") || strings.HasPrefix(r, "+ ") || strings.HasPrefix(r, "- "):
		return rule{}, fmt.Errorf("filter: %q starts like an include rule or a comment", r)
	}
	out := rule{anchored: strings.HasPrefix(r, "/"), dirOnly: strings.HasSuffix(r, "/")}
	body := strings.TrimSuffix(strings.TrimPrefix(r, "/"), "/")
	if r == "/" || body == "" {
		return rule{}, fmt.Errorf("filter: %q names nothing", r)
	}
	out.segs = strings.Split(body, "/")
	if len(out.segs) > 1 && !out.anchored {
		return rule{}, fmt.Errorf(`filter: %q has a "/" inside but does not start with "/"`, r)
	}
	for _, seg := range out.segs {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return rule{}, fmt.Errorf("filter: %q has an empty or dot segment", r)
		case seg == "**":
			continue
		case strings.Contains(seg, "**"):
			// restic reads this as "*", rclone and rsync as "any depth".
			return rule{}, fmt.Errorf(`filter: in %q "**" must be a whole path segment`, r)
		case strings.Contains(seg, "[!"):
			// Negation is "[^" for restic and rclone; only rsync takes both.
			return rule{}, fmt.Errorf(`filter: %q uses "[!"; write "[^" instead`, r)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return rule{}, fmt.Errorf("filter: %q is not a valid pattern", r)
		}
	}
	return out, nil
}

// Validate reports why rule is not an exclude rule tern accepts.
func Validate(rule string) error {
	_, err := parse(rule)
	return err
}

func segMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return ok && err == nil
}

// prefixLens returns every n for which pattern matches exactly the first n
// of parts.
func prefixLens(pattern, parts []string) []int {
	lens := map[int]bool{}
	var walk func(pi, si int)
	walk = func(pi, si int) {
		if pi == len(pattern) {
			lens[si] = true
			return
		}
		if pattern[pi] == "**" { // one or more segments
			for next := si + 1; next <= len(parts); next++ {
				walk(pi+1, next)
			}
			return
		}
		if si < len(parts) && segMatch(pattern[pi], parts[si]) {
			walk(pi+1, si+1)
		}
	}
	walk(0, 0)
	out := make([]int, 0, len(lens))
	for n := range lens {
		out = append(out, n)
	}
	return out
}

// Match is the reference implementation of the syntax: whether rules
// exclude relPath, which is relative to the sync root, "/"-separated and
// without a leading "/". Invalid rules match nothing.
func Match(rules []string, relPath string, isDir bool) bool {
	parts := strings.Split(relPath, "/")
	// The first n segments name a directory unless they are the whole
	// path of a file.
	dirAt := func(n int) bool { return n < len(parts) || isDir }
	for _, raw := range rules {
		r, err := parse(raw)
		if err != nil {
			continue
		}
		if r.anchored {
			for _, n := range prefixLens(r.segs, parts) {
				if n > 0 && (!r.dirOnly || dirAt(n)) {
					return true
				}
			}
			continue
		}
		for i, name := range parts {
			if (r.segs[0] == "**" || segMatch(r.segs[0], name)) && (!r.dirOnly || dirAt(i+1)) {
				return true
			}
		}
	}
	return false
}

// ToRclone returns the lines of a --filter-from file.
func ToRclone(rules []string) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = "- " + r
	}
	return out
}

// ToRsync returns the lines of an --exclude-from file.
func ToRsync(rules []string) []string {
	return append([]string(nil), rules...)
}

var resticLiteral = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

// ToRestic returns the lines of an --exclude-file for a backup of sources.
// An anchored rule becomes one absolute pattern per source directory.
// restic expands environment variables in the file, hence "$$".
func ToRestic(rules []string, sources []string) []string {
	var out []string
	add := func(line string) { out = append(out, strings.ReplaceAll(line, "$", "$$")) }
	for _, r := range rules {
		body := strings.TrimSuffix(r, "/")
		if !strings.HasPrefix(r, "/") {
			add(body)
			continue
		}
		for _, src := range sources {
			add(resticLiteral.Replace(strings.TrimSuffix(src, "/")) + body)
		}
	}
	return out
}
