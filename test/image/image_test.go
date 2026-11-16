//go:build image

package image

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// elfMachine is the e_machine field of an ELF header for each architecture.
var elfMachine = map[string]string{"amd64": "3e00", "arm64": "b700"}

func TestToolsOnBothArchitectures(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			platform := "linux/" + arch
			run := func(tool string, args ...string) (string, int) {
				a := append([]string{"run", "--rm", "--platform", platform, "--entrypoint", tool, img}, args...)
				return docker(t, 2*time.Minute, a...)
			}
			// An amd64 binary in the arm64 image would still run here under
			// emulation: check what each binary was built for.
			for _, bin := range []string{"/usr/local/bin/restic", "/usr/local/bin/rclone", "/usr/local/bin/tern", "/usr/bin/rsync", "/usr/bin/ssh", "/usr/bin/curl"} {
				out, code := run("od", "-An", "-tx1", "-j18", "-N2", bin)
				if got := strings.Join(strings.Fields(out), ""); code != 0 || got != elfMachine[arch] {
					t.Errorf("%s: ELF machine %q (exit %d), want %s", bin, got, code, elfMachine[arch])
				}
			}
			if out, code := run("restic", "version"); code != 0 || !strings.Contains(out, "0.19.1") || !strings.Contains(out, platform) {
				t.Errorf("restic version: exit %d\n%s", code, out)
			}
			if out, code := run("rclone", "version"); code != 0 || !strings.Contains(out, "v1.76.0") || !strings.Contains(out, "os/arch: "+arch) {
				t.Errorf("rclone version: exit %d\n%s", code, out)
			}
			// Post-task commands send notifications with curl (spec §10).
			if out, code := run("curl", "--version"); code != 0 || !strings.Contains(out, "https") {
				t.Errorf("curl --version: exit %d, want https among the protocols\n%s", code, out)
			}
			for _, c := range [][]string{{"rsync", "--version"}, {"ssh", "-V"}, {"tern", "--version"}} {
				if out, code := run(c[0], c[1:]...); code != 0 {
					t.Errorf("%s: exit %d\n%s", strings.Join(c, " "), code, out)
				}
			}
		})
	}
}

func TestStartsHealthy(t *testing.T) {
	name := start(t, mounts(t)...)
	waitHealthy(t, name, 60*time.Second)
	resp, err := http.Get(baseURL(t, name) + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/status = %d", resp.StatusCode)
	}
}

func TestRootByDefault(t *testing.T) {
	name := start(t, mounts(t)...)
	waitHealthy(t, name, 60*time.Second)
	if uid, gid := ternIDs(t, name); uid != "0" || gid != "0" {
		t.Fatalf("tern runs as %s:%s, want 0:0", uid, gid)
	}
}

func TestPUID(t *testing.T) {
	name := start(t, append(mounts(t), "-e", "PUID=1000", "-e", "PGID=1000")...)
	waitHealthy(t, name, 60*time.Second)
	if uid, gid := ternIDs(t, name); uid != "1000" || gid != "1000" {
		t.Fatalf("tern runs as %s:%s, want 1000:1000", uid, gid)
	}
	setupKeyfile(t, name)
	out := mustDocker(t, "exec", name, "stat", "-c", "%n %u:%g %a", "/data/tern.db", "/secrets/master.key")
	want := "/data/tern.db 1000:1000 600\n/secrets/master.key 1000:1000 600"
	if out != want {
		t.Fatalf("ownership:\n%s\nwant:\n%s", out, want)
	}
}

func TestPUIDUnwritableData(t *testing.T) {
	out, code := docker(t, 2*time.Minute, "run", "--rm",
		"-v", volume(t)+":/data:ro", "-v", volume(t)+":/secrets",
		"-e", "PUID=1000", "-e", "PGID=1000", img)
	if code != 1 {
		t.Fatalf("exit code %d, want 1\n%s", code, out)
	}
	for _, want := range []string{"cannot write", "/data", "1000"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRuntimeDirMustBeTmpfs(t *testing.T) {
	out, code := docker(t, 2*time.Minute, append([]string{"run", "--rm", "-e", "TERN_RUNTIME_DIR=/data/run"}, append(mounts(t), img)...)...)
	if code == 0 || !strings.Contains(out, "tmpfs") {
		t.Fatalf("exit code %d, want non-zero and a word about tmpfs:\n%s", code, out)
	}
}

func TestNoBuildToolsInImage(t *testing.T) {
	out, code := docker(t, time.Minute, "run", "--rm", "--entrypoint", "sh", img, "-c",
		`for t in go node npm gcc; do command -v "$t" >/dev/null && echo "found $t"; done; true`)
	if code != 0 || strings.Contains(out, "found") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestStopIsGraceful(t *testing.T) {
	name := start(t, mounts(t)...)
	waitHealthy(t, name, 60*time.Second)
	began := time.Now()
	mustDocker(t, "stop", "-t", "60", name)
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("docker stop took %s", took)
	}
	if code := mustDocker(t, "inspect", "-f", "{{.State.ExitCode}}", name); code != "0" {
		t.Fatalf("exit code %s, want 0", code)
	}
}

func TestRunsWithDroppedCaps(t *testing.T) {
	for _, id := range []string{"0", "1000"} {
		t.Run("PUID="+id, func(t *testing.T) {
			args := append(append(mounts(t), droppedCaps...), "-e", "PUID="+id, "-e", "PGID="+id)
			name := start(t, args...)
			waitHealthy(t, name, 60*time.Second)
			if uid, _ := ternIDs(t, name); uid != id {
				t.Fatalf("tern runs as uid %s, want %s", uid, id)
			}
		})
	}
}
