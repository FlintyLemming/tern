package restic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"tern/internal/engine"
	"tern/internal/model"
)

func TestParseRestoreParams(t *testing.T) {
	bad := []map[string]string{
		{"snapshot_id": "latest", "target_dir": "/t"},
		{"snapshot_id": "ABCD1234", "target_dir": "/t"},
		{"snapshot_id": "abc", "target_dir": "/t"},
		{"snapshot_id": "", "target_dir": "/t"},
		{"target_dir": "/t"},
		{"snapshot_id": "abcd1234 --target /etc", "target_dir": "/t"},
		{"snapshot_id": "abcd1234", "subpath": "photos", "target_dir": "/t"},
		{"snapshot_id": "abcd1234", "subpath": "/photos/../etc", "target_dir": "/t"},
		{"snapshot_id": "abcd1234", "subpath": "/photos/", "target_dir": "/t"},
		{"snapshot_id": "abcd1234", "subpath": "/a\nb", "target_dir": "/t"},
		{"snapshot_id": "abcd1234"},
		{"snapshot_id": "abcd1234", "target_dir": ""},
	}
	for _, m := range bad {
		if _, err := ParseRestoreParams(m); err == nil {
			t.Errorf("ParseRestoreParams(%v) = nil error", m)
		}
	}
	var pe *ParamError
	if _, err := ParseRestoreParams(map[string]string{"snapshot_id": "nope", "target_dir": "/t"}); !errors.As(err, &pe) || pe.Field != "snapshot_id" || pe.Code != "invalid" {
		t.Errorf("err = %#v", err)
	}
	if _, err := ParseRestoreParams(map[string]string{"snapshot_id": "abcd1234"}); !errors.As(err, &pe) || pe.Field != "target_dir" || pe.Code != "required" {
		t.Errorf("err = %#v", err)
	}

	got, err := ParseRestoreParams(map[string]string{"snapshot_id": "abcd1234", "target_dir": "/targets/r"})
	if want := (RestoreParams{SnapshotID: "abcd1234", TargetDir: "/targets/r"}); err != nil || got != want {
		t.Errorf("got %+v, %v", got, err)
	}
	got, err = ParseRestoreParams(map[string]string{"snapshot_id": "abcd1234abcd1234", "subpath": "/photos/2024", "target_dir": "/targets/r"})
	if want := (RestoreParams{SnapshotID: "abcd1234abcd1234", Subpath: "/photos/2024", TargetDir: "/targets/r"}); err != nil || got != want {
		t.Errorf("got %+v, %v", got, err)
	}
}

func yes(string) (bool, error) { return true, nil }
func no(string) (bool, error)  { return false, nil }

func TestRestoreTargetRules(t *testing.T) {
	root := t.TempDir()
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	source := mk("sources", "photos")
	os.WriteFile(filepath.Join(source, "a.jpg"), []byte("x"), 0o644)
	dataDir, secretsDir := mk("data"), mk("secrets")
	targets := mk("targets")
	full := mk("targets", "full")
	os.WriteFile(filepath.Join(full, "leftover"), nil, 0o644)
	empty := mk("targets", "empty")
	file := filepath.Join(targets, "a-file")
	os.WriteFile(file, nil, 0o644)
	readonly := mk("readonly")
	os.Chmod(readonly, 0o500)
	t.Cleanup(func() { os.Chmod(readonly, 0o700) })
	link := filepath.Join(targets, "link-to-data")
	if err := os.Symlink(dataDir, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		target  string
		mounted func(string) (bool, error)
		want    string // "" = accepted
	}{
		{"relative", "restore-1", yes, "not_absolute"},
		{"empty", "", yes, "not_absolute"},
		{"not clean", targets + "/../targets/new", yes, "not_absolute"},
		{"trailing slash", targets + "/new/", yes, "not_absolute"},
		{"control character", targets + "/a\nb", yes, "not_absolute"},
		{"has a file", full, yes, "not_empty"},
		{"is a file", file, yes, "not_empty"},
		{"not mounted", filepath.Join(targets, "new"), no, "not_mounted"},
		{"mount check fails", filepath.Join(targets, "new"), func(string) (bool, error) { return false, errors.New("boom") }, "not_mounted"},
		{"inside a source", filepath.Join(source, "r"), yes, "inside_source"},
		{"the source itself", source, yes, "inside_source"},
		{"inside the data dir", filepath.Join(dataDir, "r"), yes, "inside_data"},
		{"the data dir itself", dataDir, yes, "inside_data"},
		{"inside the secrets dir", filepath.Join(secretsDir, "r"), yes, "inside_data"},
		{"through a symlink into the data dir", filepath.Join(link, "r"), yes, "inside_data"},
		{"new directory", filepath.Join(targets, "new"), yes, ""},
		{"new nested directory", filepath.Join(targets, "a", "b"), yes, ""},
		{"existing empty directory", empty, yes, ""},
		{"sibling with a common prefix", source + "-restored", yes, ""},
	}
	if os.Geteuid() != 0 { // root writes anywhere
		cases = append(cases, struct {
			name    string
			target  string
			mounted func(string) (bool, error)
			want    string
		}{"parent not writable", filepath.Join(readonly, "r"), yes, "not_writable"})
	}
	for _, c := range cases {
		err := ValidateRestoreTarget(c.target, []string{source}, dataDir, secretsDir, c.mounted)
		var pe *ParamError
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v, want it accepted", c.name, err)
		case c.want != "" && (!errors.As(err, &pe) || pe.Field != "target_dir" || pe.Code != c.want):
			t.Errorf("%s: err = %v, want code %s", c.name, err, c.want)
		}
	}
	// The probe for writability leaves nothing behind.
	if entries, _ := os.ReadDir(targets); len(entries) != 4 {
		t.Errorf("targets now holds %d entries, want the 4 it started with", len(entries))
	}
}

const restoreDone = `{"message_type":"status","seconds_elapsed":5,"percent_done":0.5,"total_files":4,"files_restored":2,"total_bytes":800,"bytes_restored":400}` + "\n" +
	`{"message_type":"summary","total_files":4,"files_restored":4,"total_bytes":800,"bytes_restored":800}` + "\n"

// restoreFx prepares a restore into a fresh directory next to the source.
func restoreFx(t *testing.T, subpath string, steps ...fakeStep) (*fx, string, map[string]string) {
	t.Helper()
	f := newFx(t, steps...)
	f.deps.SecretsDir = t.TempDir()
	f.deps.Mounted = yes
	target := filepath.Join(t.TempDir(), "restore-1")
	params := map[string]string{"snapshot_id": "abcd1234", "target_dir": target}
	if subpath != "" {
		params["subpath"] = subpath
	}
	return f, target, params
}

func TestRestoreArgs(t *testing.T) {
	f, target, params := restoreFx(t, "/photos/2024", fakeStep{Stdout: restoreDone})
	eng := f.ready(model.RunRestore, params)
	if got := eng.Units(); !reflect.DeepEqual(got, []string{"restore"}) {
		t.Errorf("Units = %q", got)
	}
	if _, err := eng.RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	c := f.call("restore")
	if tail := c.Args[len(c.Args)-5:]; !reflect.DeepEqual(tail, []string{"restore", "abcd1234:/photos/2024", "--target", target, "--json"}) {
		t.Errorf("args end with %q", tail)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"restore"}) {
		t.Errorf("calls = %q", got)
	}

	f, target, params = restoreFx(t, "", fakeStep{Stdout: restoreDone})
	if _, err := f.ready(model.RunRestore, params).RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	c = f.call("restore")
	if tail := c.Args[len(c.Args)-5:]; !reflect.DeepEqual(tail, []string{"restore", "abcd1234", "--target", target, "--json"}) {
		t.Errorf("without subpath args end with %q", tail)
	}
}

func TestRestoreUnitResult(t *testing.T) {
	f, _, params := restoreFx(t, "", fakeStep{Stdout: restoreDone})
	res, err := f.ready(model.RunRestore, params).RunUnit(context.Background(), f.env, 0)
	want := model.UnitResult{Name: "restore", Status: model.StatusSuccess, Bytes: 800, Files: 4}
	if err != nil || res != want {
		t.Errorf("RunUnit = %+v, %v; want %+v", res, err, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.progress) != 1 || f.progress[0].Unit != "restore" || f.progress[0].BytesDone != 400 {
		t.Errorf("progress = %+v", f.progress)
	}
}

func TestRestoreBadParams(t *testing.T) {
	f := newFx(t)
	_, err := NewBuilder(f.deps)(f.task, engine.Request{Kind: model.RunRestore, Params: map[string]string{"snapshot_id": "latest", "target_dir": "/t"}})
	if model.KindOf(err) != model.KindConfig {
		t.Errorf("build with a bad snapshot id: %v", err)
	}
}

func TestRestorePreflightValidatesTarget(t *testing.T) {
	f, target, params := restoreFx(t, "")
	// Between the request and the run somebody put a file there.
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "x"), nil, 0o644)
	err := f.build(model.RunRestore, params).Preflight(context.Background(), f.env)
	wantKind(t, err, model.KindConfig, "not empty")
	if n := len(f.fake.Calls()); n != 0 {
		t.Errorf("%d restic calls", n)
	}

	f, _, params = restoreFx(t, "")
	params["target_dir"] = filepath.Join(f.src(), "r")
	wantKind(t, f.build(model.RunRestore, params).Preflight(context.Background(), f.env), model.KindConfig, "source")

	f, _, params = restoreFx(t, "")
	f.deps.Mounted = no
	wantKind(t, f.build(model.RunRestore, params).Preflight(context.Background(), f.env), model.KindConfig, "mounted")
}

// A retry of an interrupted restore finds its own half-restored files in
// the target; that must not stop it.
func TestRestoreRetryIntoOwnTarget(t *testing.T) {
	f, target, params := restoreFx(t, "", fakeStep{Exit: 1, Stderr: "subprocess ssh: Connection reset by peer\n"}, stepOK, fakeStep{Stdout: restoreDone})
	eng := f.ready(model.RunRestore, params)
	ctx := context.Background()
	_, err := eng.RunUnit(ctx, f.env, 0)
	if model.KindOf(err) != model.KindNetwork {
		t.Fatalf("first attempt: %v", err)
	}
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "half"), nil, 0o644) // what restic left
	if err := eng.Cleanup(ctx, f.env); err != nil {
		t.Fatal(err)
	}
	if err := eng.Preflight(ctx, f.env); err != nil {
		t.Fatalf("Preflight of the retry: %v", err)
	}
	if res, err := eng.RunUnit(ctx, f.env, 0); err != nil || res.Files != 4 {
		t.Fatalf("retry: %+v, %v", res, err)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"restore", "unlock", "restore"}) {
		t.Errorf("calls = %q", got)
	}
}

func TestRestoreDescribe(t *testing.T) {
	f, target, params := restoreFx(t, "/photos/2024")
	lines := f.build(model.RunRestore, params).Describe()
	if len(lines) != 3 || lines[2] != "restic restore abcd1234:/photos/2024 --target "+target {
		t.Errorf("Describe = %q", lines)
	}
}
