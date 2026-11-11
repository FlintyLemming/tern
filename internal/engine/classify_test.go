package engine

import (
	"testing"

	"tern/internal/model"
)

func TestClassifyStderr(t *testing.T) {
	cases := []struct {
		tail string
		want model.ErrKind
	}{
		{"Host key verification failed.", model.KindHostKey},
		{"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@", model.KindHostKey},
		{"backup@nas2: Permission denied (publickey).", model.KindAuth},
		{"Received disconnect: Too many authentication failures", model.KindAuth},
		{"401 Unauthorized", model.KindAuth},
		{"403 Forbidden", model.KindAuth},
		{"Timeout, server 10.0.0.2 not responding.", model.KindNetwork},
		{"ssh: connect to host nas2 port 22: Connection timed out", model.KindNetwork},
		{"ssh: connect to host nas2 port 22: Connection refused", model.KindNetwork},
		{"client_loop: send disconnect: Broken pipe", model.KindNetwork},
		{"ssh: Could not resolve hostname nas2: Temporary failure in name resolution", model.KindNetwork},
		{"connect: no route to host", model.KindNetwork},
		{"connect: Network is unreachable", model.KindNetwork},
		{"read tcp: i/o timeout", model.KindNetwork},
		{"unexpected EOF", model.KindNetwork},
		{"subprocess ssh: connection lost", model.KindNetwork},
		{"bash: line 1: rsync: command not found", model.KindConfig},
		{"sh: rsync: not found", model.KindConfig},
	}
	for _, c := range cases {
		if got, ok := ClassifyStderr(c.tail); !ok || got != c.want {
			t.Errorf("ClassifyStderr(%q) = %q, %v; want %q", c.tail, got, ok, c.want)
		}
	}
	for _, tail := range []string{"something else entirely", ""} {
		if got, ok := ClassifyStderr(tail); ok || got != "" {
			t.Errorf("ClassifyStderr(%q) = %q, %v; want unrecognized", tail, got, ok)
		}
	}
}

func TestClassifyOrder(t *testing.T) {
	if got, _ := ClassifyStderr("Connection closed by 10.0.0.2\nHost key verification failed."); got != model.KindHostKey {
		t.Errorf("got %q, want host_key", got)
	}
	if got, _ := ClassifyStderr("Connection reset by peer\nbackup@nas2: Permission denied (publickey,password)."); got != model.KindAuth {
		t.Errorf("got %q, want auth", got)
	}
}

func TestClassifyCaseInsensitive(t *testing.T) {
	if got, ok := ClassifyStderr("CONNECTION RESET BY PEER"); !ok || got != model.KindNetwork {
		t.Errorf("got %q, %v", got, ok)
	}
}
