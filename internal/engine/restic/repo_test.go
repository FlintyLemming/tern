package restic

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"tern/internal/model"
)

func attachWith(t *testing.T, steps ...fakeStep) (RepoState, error, func() []fakeCall) {
	t.Helper()
	bin, calls := fakeRestic(t, steps...)
	iv := testInvocation(t)
	iv.Bin = bin
	state, err := Attach(context.Background(), iv, []byte(repoPW))
	return state, err, calls
}

func TestAttachExisting(t *testing.T) {
	state, err, calls := attachWith(t, stepOK)
	if err != nil || state != RepoExisting {
		t.Fatalf("Attach = %q, %v", state, err)
	}
	got := calls()
	if !reflect.DeepEqual(subs(got), []string{"cat"}) || !got[0].Has("cat", "config") || got[0].Password != repoPW {
		t.Errorf("calls = %+v", got)
	}
}

func TestAttachNew(t *testing.T) {
	state, err, calls := attachWith(t, fakeStep{Exit: 10}, stepOK)
	if err != nil || state != RepoNew {
		t.Fatalf("Attach = %q, %v", state, err)
	}
	got := calls()
	if !reflect.DeepEqual(subs(got), []string{"cat", "init"}) || got[1].Password != repoPW {
		t.Errorf("calls = %+v", got)
	}
}

func TestAttachWrongPassword(t *testing.T) {
	_, err, calls := attachWith(t, fakeStep{Exit: 12})
	if model.KindOf(err) != model.KindAuth {
		t.Fatalf("err = %v, want an auth error", err)
	}
	if got := subs(calls()); !reflect.DeepEqual(got, []string{"cat"}) {
		t.Errorf("calls = %q; a repository that refused the password must not be initialized", got)
	}
}

func TestAttachInitFails(t *testing.T) {
	_, err, _ := attachWith(t, fakeStep{Exit: 10}, fakeStep{Exit: 1, Stderr: "Fatal: create repository failed: sftp: permission denied " + repoPW + "\n"})
	if model.KindOf(err) != model.KindUnknown || !strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), repoPW) {
		t.Errorf("err = %v", err)
	}
}

func TestAttachNetwork(t *testing.T) {
	_, err, calls := attachWith(t, fakeStep{Exit: 1, Stderr: "ssh: connect to host nas2 port 22: Connection refused\n"})
	if model.KindOf(err) != model.KindNetwork {
		t.Fatalf("err = %v, want a network error", err)
	}
	if got := subs(calls()); !reflect.DeepEqual(got, []string{"cat"}) {
		t.Errorf("calls = %q", got)
	}
}

func TestRecoveryInfo(t *testing.T) {
	conn := model.Connection{Type: model.ConnSSH, Host: "nas2.example", Port: 22, Username: "backup"}
	got := RecoveryInfo(conn, "/srv/repo", "pw-1")
	want := Recovery{Repository: "sftp:backup@nas2.example:/srv/repo", Password: "pw-1", Commands: []string{
		"export RESTIC_REPOSITORY='sftp:backup@nas2.example:/srv/repo'",
		"export RESTIC_PASSWORD='pw-1'",
		"restic snapshots",
		"restic restore latest --target /path/to/restore",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RecoveryInfo = %+v\nwant %+v", got, want)
	}
}

func TestRecoveryCommandsQuote(t *testing.T) {
	got := RecoveryInfo(model.Connection{Host: "h", Port: 22, Username: "u"}, "/r", "it's")
	if want := `export RESTIC_PASSWORD='it'\''s'`; got.Commands[1] != want {
		t.Errorf("line 2 = %q, want %q", got.Commands[1], want)
	}
	if got.Password != "it's" {
		t.Errorf("Password = %q", got.Password)
	}
}

func TestValidateRepoPassword(t *testing.T) {
	for _, pw := range []string{" lead", "trail ", "\ttab", "a\nb", "a\x00b", "a\x7fb", "", strings.Repeat("x", 1025), "bad\xffutf8", "nbsp\u00a0"} {
		if ValidateRepoPassword(pw) == nil {
			t.Errorf("ValidateRepoPassword(%q) = nil, want an error", pw)
		}
	}
	for _, pw := range []string{`p@$$'w"ö密码`, "x", "in the middle", strings.Repeat("x", 1024), strings.Repeat("密", 341)} {
		if err := ValidateRepoPassword(pw); err != nil {
			t.Errorf("ValidateRepoPassword(%q) = %v", pw, err)
		}
	}
}

func TestGenerateRepoPassword(t *testing.T) {
	a, b := GenerateRepoPassword(), GenerateRepoPassword()
	re := regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	if !re.MatchString(a) || !re.MatchString(b) || a == b {
		t.Errorf("generated %q and %q", a, b)
	}
	if err := ValidateRepoPassword(a); err != nil {
		t.Errorf("a generated password is not valid: %v", err)
	}
}

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
