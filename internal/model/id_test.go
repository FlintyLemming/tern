package model

import (
	"regexp"
	"testing"
)

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := NewID()
		if !re.MatchString(id) {
			t.Fatalf("id %q does not match %s", id, re)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
