package engine_test

import (
	"testing"

	"tern/internal/engine"
	"tern/internal/engine/fake"
	"tern/internal/model"
)

func TestBuildUnregistered(t *testing.T) {
	r := engine.NewRegistry()
	_, err := r.Build(model.Task{Type: model.TaskBackup}, engine.Request{Kind: model.RunBackup})
	if model.KindOf(err) != model.KindConfig {
		t.Errorf("err = %v (kind %q), want kind config", err, model.KindOf(err))
	}
}

func TestBuildRegistered(t *testing.T) {
	r := engine.NewRegistry()
	e := &fake.Engine{}
	r.Register(model.TaskSync, fake.Builder(e))
	got, err := r.Build(model.Task{Type: model.TaskSync}, engine.Request{Kind: model.RunSync, AllowDeletes: true})
	if err != nil || got != engine.Engine(e) {
		t.Fatalf("Build = %v, %v", got, err)
	}
	if reqs := e.Built(); len(reqs) != 1 || !reqs[0].AllowDeletes || reqs[0].Kind != model.RunSync {
		t.Errorf("Built = %+v", reqs)
	}
}
