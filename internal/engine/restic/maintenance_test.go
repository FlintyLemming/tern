package restic

import (
	"context"
	"reflect"
	"testing"

	"tern/internal/model"
)

func TestPruneCalls(t *testing.T) {
	f := newFx(t)
	eng := f.ready(model.RunPrune, nil)
	if got := eng.Units(); !reflect.DeepEqual(got, []string{"prune"}) {
		t.Errorf("Units = %q", got)
	}
	res, err := eng.RunUnit(context.Background(), f.env, 0)
	if err != nil || res.Name != "prune" || res.Status != model.StatusSuccess {
		t.Fatalf("RunUnit = %+v, %v", res, err)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"unlock", "prune"}) {
		t.Errorf("calls = %q", got)
	}
	if f.acc.checks != 1 {
		t.Errorf("preflight checks = %d", f.acc.checks)
	}
	if c := f.call("prune"); c.Password != repoPW || !c.Has("-r", "sftp:tern-target:/srv/repo") {
		t.Errorf("prune call = %+v", c)
	}
}

func TestCheckCalls(t *testing.T) {
	f := newFx(t)
	eng := f.ready(model.RunCheck, nil)
	if got := eng.Units(); !reflect.DeepEqual(got, []string{"check"}) {
		t.Errorf("Units = %q", got)
	}
	res, err := eng.RunUnit(context.Background(), f.env, 0)
	if err != nil || res.Name != "check" || res.Status != model.StatusSuccess {
		t.Fatalf("RunUnit = %+v, %v", res, err)
	}
	if got := f.fake.Subs(); !reflect.DeepEqual(got, []string{"check"}) {
		t.Errorf("calls = %q", got)
	}
}

func TestPruneLockedRetryable(t *testing.T) {
	f := newFx(t, stepOK, fakeStep{Exit: 11})
	eng := f.ready(model.RunPrune, nil)
	_, err := eng.RunUnit(context.Background(), f.env, 0)
	if err == nil || !model.KindOf(err).Retryable() {
		t.Fatalf("err = %v (kind %q), want a retryable error", err, model.KindOf(err))
	}
	before := len(f.fake.Calls())
	if err := eng.Cleanup(context.Background(), f.env); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.Subs()[before:]; !reflect.DeepEqual(got, []string{"unlock"}) {
		t.Errorf("Cleanup called %q", got)
	}
	for _, c := range f.fake.Calls() {
		for _, a := range c.Args {
			if a == "--remove-all" {
				t.Errorf("%q", c.Args)
			}
		}
	}
}

func TestCheckFailure(t *testing.T) {
	f := newFx(t, fakeStep{Exit: 1, Stderr: "Fatal: repository contains errors\n"})
	eng := f.ready(model.RunCheck, nil)
	_, err := eng.RunUnit(context.Background(), f.env, 0)
	wantKind(t, err, model.KindUnknown, "repository contains errors")
}

func TestMaintenancePreflightNetwork(t *testing.T) {
	f := newFx(t)
	f.acc.checkErr = &model.KindError{Kind: model.KindNetwork, Msg: "down"}
	for _, kind := range []model.RunKind{model.RunPrune, model.RunCheck} {
		if err := f.build(kind, nil).Preflight(context.Background(), f.env); model.KindOf(err) != model.KindNetwork {
			t.Errorf("%s preflight = %v", kind, err)
		}
	}
	if n := len(f.fake.Calls()); n != 0 {
		t.Errorf("%d restic calls", n)
	}
}

func TestMaintenanceDescribe(t *testing.T) {
	f := newFx(t)
	for _, kind := range []model.RunKind{model.RunPrune, model.RunCheck} {
		lines := f.build(kind, nil).Describe()
		if len(lines) != 3 || lines[0] != "export RESTIC_REPOSITORY='sftp:backup@nas2.example:/srv/repo'" || lines[2] != "restic "+string(kind) {
			t.Errorf("%s Describe = %q", kind, lines)
		}
	}
}
