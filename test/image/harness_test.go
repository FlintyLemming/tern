//go:build image

// Package image tests the production image from the outside, through the
// docker command line on the build host. Run them with `make image-test`,
// which builds the image first; TERN_IMAGE names another tag to test.
package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var img = envOr("TERN_IMAGE", "tern:image-test")

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// repoRoot is the repository's top directory, two levels above this file.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// docker runs the docker command line and returns its combined output and
// exit code. It fails the test only when docker cannot be run at all.
func docker(t *testing.T, timeout time.Duration, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("docker %s: timed out after %s\n%s", strings.Join(args, " "), timeout, out.String())
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exit):
		return out.String(), exit.ExitCode()
	}
	t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
	return "", 0
}

// mustDocker is docker for commands that have to succeed.
func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, code := docker(t, 2*time.Minute, args...)
	if code != 0 {
		t.Fatalf("docker %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return strings.TrimSpace(out)
}

var seq atomic.Int64

// unique is a docker object name no other test run uses.
func unique(kind string) string {
	return fmt.Sprintf("tern-image-%s-%d-%d", kind, os.Getpid(), seq.Add(1))
}

// volume creates a named volume, removed when the test ends.
func volume(t *testing.T) string {
	t.Helper()
	name := mustDocker(t, "volume", "create", unique("vol"))
	t.Cleanup(func() { docker(t, time.Minute, "volume", "rm", "-f", name) })
	return name
}

// mounts are the two volumes every node needs, as docker run arguments.
func mounts(t *testing.T) []string {
	return []string{"-v", volume(t) + ":/data", "-v", volume(t) + ":/secrets"}
}

// start runs the image detached with args before the image name, publishing
// port 8080 on a free loopback port. The container is removed at cleanup;
// its log is printed first when the test failed.
func start(t *testing.T, args ...string) string {
	t.Helper()
	name := unique("ctr")
	run := append([]string{"run", "-d", "--name", name, "-p", "127.0.0.1::8080"}, args...)
	mustDocker(t, append(run, img)...)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := docker(t, time.Minute, "logs", name)
			t.Logf("logs of %s:\n%s", name, out)
		}
		docker(t, time.Minute, "rm", "-f", "-v", name)
	})
	return name
}

// waitHealthy waits until docker reports the container healthy.
func waitHealthy(t *testing.T, name string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for time.Now().Before(deadline) {
		out, _ := docker(t, time.Minute, "inspect", "-f", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", name)
		last = strings.TrimSpace(out)
		if last == "running healthy" {
			return
		}
		if strings.HasPrefix(last, "exited") || strings.HasPrefix(last, "dead") {
			break
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s is not healthy within %s: %s", name, within, last)
}

// baseURL is where the host reaches the container's port 8080.
func baseURL(t *testing.T, name string) string {
	t.Helper()
	out := mustDocker(t, "port", name, "8080/tcp")
	return "http://" + strings.Fields(out)[0]
}

// ternIDs returns the real uid and gid of the tern process. The entrypoint
// replaces itself with tern (through su-exec when PUID is set), so tern is
// PID 1.
func ternIDs(t *testing.T, name string) (uid, gid string) {
	t.Helper()
	out := mustDocker(t, "exec", name, "sh", "-c", `cat /proc/1/comm; grep -E '^(Uid|Gid):' /proc/1/status`)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || strings.TrimSpace(lines[0]) != "tern" {
		t.Fatalf("PID 1 is not tern:\n%s", out)
	}
	return strings.Fields(lines[1])[1], strings.Fields(lines[2])[1]
}

// postJSON sends body to the container's API and returns the status code.
func postJSON(t *testing.T, url string, body any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// setupKeyfile walks the setup wizard's request in key file mode.
func setupKeyfile(t *testing.T, name string) {
	t.Helper()
	code := postJSON(t, baseURL(t, name)+"/api/v1/setup", map[string]any{
		"password": "image-test-admin-password", "node_name": "image-test", "unlock_mode": "keyfile",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
}

// droppedCaps are compose's cap_drop/cap_add and no-new-privileges.
var droppedCaps = []string{
	"--cap-drop", "ALL",
	"--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--cap-add", "DAC_READ_SEARCH",
	"--cap-add", "FOWNER", "--cap-add", "SETUID", "--cap-add", "SETGID",
	"--security-opt", "no-new-privileges:true",
}
