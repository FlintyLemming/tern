package sysinfo

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"tern/internal/proc"
)

// Tool is one of the external programs tern drives.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Found   bool   `json:"found"`
}

var (
	toolCommands = []struct {
		name string
		args []string
	}{
		{"restic", []string{"version"}},
		{"rclone", []string{"version"}},
		{"rsync", []string{"--version"}},
		{"ssh", []string{"-V"}},
	}
	versionRE = regexp.MustCompile(`\d+\.\d+[0-9A-Za-z.\-]*`)

	toolsMu     sync.Mutex
	toolsCached []Tool
)

func resetTools() {
	toolsMu.Lock()
	toolsCached = nil
	toolsMu.Unlock()
}

// Tools reports restic, rclone, rsync and ssh, in that order, running each
// one's version command once per process. A missing or broken tool is
// reported as not found, never as an error.
func Tools(ctx context.Context) []Tool {
	toolsMu.Lock()
	defer toolsMu.Unlock()
	if toolsCached == nil {
		for _, c := range toolCommands {
			toolsCached = append(toolsCached, probeTool(ctx, c.name, c.args))
		}
	}
	return append([]Tool(nil), toolsCached...)
}

func probeTool(ctx context.Context, name string, args []string) Tool {
	t := Tool{Name: name}
	path, err := exec.LookPath(name)
	if err != nil {
		return t
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var out []string // stdout and stderr: ssh -V writes to stderr
	collect := func(line []byte) {
		mu.Lock()
		out = append(out, string(line))
		mu.Unlock()
	}
	res, err := proc.Run(ctx, proc.Cmd{Path: path, Args: args, OnStdout: collect, OnStderr: collect})
	if err != nil || res.ExitCode != 0 {
		return t
	}
	t.Found = true
	if len(out) > 0 {
		t.Version = versionRE.FindString(strings.TrimSpace(out[0]))
	}
	return t
}
