package restic

import (
	"testing"

	"tern/internal/model"
)

func TestRecoveryRepository(t *testing.T) {
	cases := []struct {
		host string
		port int
		path string
		want string
	}{
		{"nas2.example", 22, "/srv/repo", "sftp:backup@nas2.example:/srv/repo"},
		{"nas2.example", 22, "repo", "sftp:backup@nas2.example:repo"},
		{"nas2.example", 2222, "/srv/repo", "sftp://backup@nas2.example:2222//srv/repo"},
		{"nas2.example", 2222, "repo", "sftp://backup@nas2.example:2222/repo"},
		{"fd7a:115c::1", 22, "/srv/repo", "sftp://backup@[fd7a:115c::1]:22//srv/repo"},
		{"192.0.2.7", 22, "/srv/repo", "sftp:backup@192.0.2.7:/srv/repo"},
	}
	for _, c := range cases {
		conn := model.Connection{Type: model.ConnSSH, Host: c.host, Port: c.port, Username: "backup"}
		if got := RecoveryRepository(conn, c.path); got != c.want {
			t.Errorf("RecoveryRepository(%s:%d, %q) = %q, want %q", c.host, c.port, c.path, got, c.want)
		}
	}
}
