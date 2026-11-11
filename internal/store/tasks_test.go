package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"tern/internal/model"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func sampleTask(name string) model.Task {
	t := model.Task{
		ID: model.NewID(), Version: 1, Type: model.TaskSync, Name: name, Enabled: true,
		Schedule: "0 3 * * *", CatchUp: true,
		Pre: model.Hook{Script: "echo pre", Where: "ssh", ConnectionID: "hookconn", IgnoreFailure: true},
		Sync: &model.SyncSpec{
			Protocol: "sftp", ConnectionID: "mainconn", Direction: "push",
			Pairs:    []model.FolderPair{{Local: "/a", Remote: "x"}, {Local: "/b", Remote: "y"}},
			Excludes: []string{"*.tmp"},
		},
		CreatedAt: t0, UpdatedAt: t0,
	}
	t.ApplyDefaults()
	return t
}

func mustCreateTask(t *testing.T, s *Store, task model.Task) model.Task {
	t.Helper()
	if err := s.CreateTask(context.Background(), task, ""); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func TestTaskRoundTrip(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	want := mustCreateTask(t, s, sampleTask("photos"))
	got, err := s.GetTask(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	list, err := s.ListTasks(ctx)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], want) {
		t.Errorf("ListTasks = %+v, %v", list, err)
	}
	if _, err := s.GetTask(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTask(nope) = %v", err)
	}
	dup := sampleTask("photos")
	if err := s.CreateTask(ctx, dup, ""); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate name = %v, want ErrDuplicate", err)
	}
}

func TestTaskRepoPassword(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	task := sampleTask("docs")
	if err := s.CreateTask(ctx, task, "sealed-blob"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TaskRepoPassword(ctx, task.ID); err != nil || got != "sealed-blob" {
		t.Errorf("TaskRepoPassword = %q, %v", got, err)
	}
	var spec string
	s.DB().QueryRow("SELECT spec FROM tasks WHERE id = ?", task.ID).Scan(&spec)
	if spec == "" || contains(spec, "sealed-blob") {
		t.Errorf("spec column = %q", spec)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestUpdateTaskVersion(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	task := mustCreateTask(t, s, sampleTask("photos"))
	task.Name, task.Enabled, task.UpdatedAt = "pictures", false, t0.Add(time.Hour)
	if err := s.UpdateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Version != 2 || got.Name != "pictures" || got.Enabled || !got.UpdatedAt.Equal(t0.Add(time.Hour)) || !got.CreatedAt.Equal(t0) {
		t.Errorf("after update: %+v", got)
	}
	if err := s.UpdateTask(ctx, task); !errors.Is(err, ErrVersion) { // still carries version 1
		t.Errorf("stale update = %v, want ErrVersion", err)
	}
	ghost := sampleTask("ghost")
	if err := s.UpdateTask(ctx, ghost); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of missing task = %v, want ErrNotFound", err)
	}
}

func TestTasksUsingConnection(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	mustCreateTask(t, s, sampleTask("photos")) // main "mainconn", pre hook "hookconn"
	other := sampleTask("other")
	other.Sync.ConnectionID, other.Pre = "elsewhere", model.Hook{}
	mustCreateTask(t, s, other)
	for conn, want := range map[string][]string{"mainconn": {"photos"}, "hookconn": {"photos"}, "elsewhere": {"other"}, "unused": {}} {
		got, err := s.TasksUsingConnection(ctx, conn)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, %v; want %v", conn, got, err, want)
		}
	}
}

func TestTaskState(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	task := mustCreateTask(t, s, sampleTask("photos"))
	st, err := s.GetTaskState(ctx, task.ID)
	if err != nil || !st.LastFireAt.IsZero() || !st.LastSuccessAt.IsZero() {
		t.Fatalf("initial state = %+v, %v", st, err)
	}
	if err := s.SetTaskState(ctx, task.ID, func(ts *TaskState) { ts.LastFireAt, ts.LastPruneAt = t0, t0.Add(time.Minute) }); err != nil {
		t.Fatal(err)
	}
	st, _ = s.GetTaskState(ctx, task.ID)
	if !st.LastFireAt.Equal(t0) || !st.LastPruneAt.Equal(t0.Add(time.Minute)) || !st.LastCheckAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	if err := s.SetTaskState(ctx, "nope", func(*TaskState) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetTaskState(nope) = %v", err)
	}
}

func TestDeleteTaskKeepsRuns(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	task := mustCreateTask(t, s, sampleTask("photos"))
	r := newRun(task.ID, model.RunSync, t0)
	mustCreateRun(t, s, r)
	if err := s.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("task still there: %v", err)
	}
	if _, err := s.GetTaskState(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("task state still there: %v", err)
	}
	runs, err := s.ListRuns(ctx, RunFilter{TaskID: task.ID})
	if err != nil || len(runs) != 1 || runs[0].ID != r.ID {
		t.Errorf("runs after delete = %+v, %v", runs, err)
	}
	if err := s.DeleteTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
}
