//go:build image

package image

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func composeFile(t *testing.T) string {
	return filepath.Join(repoRoot(t), "deploy", "compose.yaml")
}

func TestComposeFileValid(t *testing.T) {
	if out, code := docker(t, time.Minute, "compose", "-f", composeFile(t), "config", "-q"); code != 0 {
		t.Fatalf("docker compose config: exit %d\n%s", code, out)
	}
}

// The deployment file is spec §13's example plus the stop grace period and
// the capability list (plan 7's global constraints).
func TestComposeMatchesSpec(t *testing.T) {
	out := mustDocker(t, "compose", "-f", composeFile(t), "config", "--format", "json")
	var cfg struct {
		Services map[string]struct {
			Image           string `json:"image"`
			Init            bool   `json:"init"`
			Restart         string `json:"restart"`
			StopGracePeriod string `json:"stop_grace_period"`
			Ports           []struct {
				Target    int    `json:"target"`
				Published string `json:"published"`
			} `json:"ports"`
			Volumes []struct {
				Target   string `json:"target"`
				ReadOnly bool   `json:"read_only"`
			} `json:"volumes"`
			Sysctls     map[string]string `json:"sysctls"`
			SecurityOpt []string          `json:"security_opt"`
			CapDrop     []string          `json:"cap_drop"`
			CapAdd      []string          `json:"cap_add"`
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	s, ok := cfg.Services["tern"]
	if !ok || len(cfg.Services) != 1 {
		t.Fatalf("services = %v, want exactly tern", cfg.Services)
	}
	check := func(what string, ok bool, got any) {
		if !ok {
			t.Errorf("%s: got %v", what, got)
		}
	}
	check("init", s.Init, s.Init)
	check("restart", s.Restart == "unless-stopped", s.Restart)
	check("stop_grace_period", s.StopGracePeriod == "2m0s", s.StopGracePeriod)
	check("ports", len(s.Ports) == 1 && s.Ports[0].Target == 8080 && s.Ports[0].Published == "8080", s.Ports)
	type vol struct {
		target string
		ro     bool
	}
	var vols []vol
	for _, v := range s.Volumes {
		vols = append(vols, vol{v.Target, v.ReadOnly})
	}
	check("volumes", slices.Equal(vols, []vol{{"/data", false}, {"/secrets", false}, {"/sources/photos", true}, {"/targets/inbox", false}}), vols)
	check("sysctls", len(s.Sysctls) == 1 && s.Sysctls["net.ipv4.tcp_congestion_control"] == "bbr", s.Sysctls)
	check("security_opt", slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}), s.SecurityOpt)
	check("cap_drop", slices.Equal(s.CapDrop, []string{"ALL"}), s.CapDrop)
	check("cap_add", slices.Equal(s.CapAdd, []string{"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "SETUID", "SETGID"}), s.CapAdd)
	check("TZ", s.Environment["TZ"] == "Asia/Shanghai", s.Environment)
	// PUID and PGID stay commented out: by default tern runs as root.
	_, puid := s.Environment["PUID"]
	_, pgid := s.Environment["PGID"]
	check("no PUID/PGID by default", !puid && !pgid, s.Environment)
}
