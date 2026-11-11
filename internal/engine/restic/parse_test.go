package restic

import (
	"testing"

	"tern/internal/model"
)

type parsed struct {
	progress []model.Progress
	items    [][2]string
}

func parseLines(lines ...string) (*Parser, *parsed) {
	got := &parsed{}
	p := NewParser(
		func(pr model.Progress) { got.progress = append(got.progress, pr) },
		func(item, msg string) { got.items = append(got.items, [2]string{item, msg}) },
	)
	for _, l := range lines {
		p.Line([]byte(l))
	}
	return p, got
}

func TestBackupStatus(t *testing.T) {
	_, got := parseLines(`{"message_type":"status","seconds_elapsed":10,"seconds_remaining":40,"percent_done":0.2,"total_files":10,"files_done":2,"total_bytes":1000,"bytes_done":200}`)
	want := model.Progress{BytesDone: 200, BytesTotal: 1000, FilesDone: 2, FilesTotal: 10, SpeedBps: 20, ETASec: 40}
	if len(got.progress) != 1 || got.progress[0] != want {
		t.Errorf("progress = %+v, want %+v", got.progress, want)
	}
}

func TestBackupStatusNoElapsed(t *testing.T) {
	_, got := parseLines(`{"message_type":"status","percent_done":0,"total_files":10,"total_bytes":1000,"bytes_done":5}`)
	if len(got.progress) != 1 || got.progress[0].SpeedBps != 0 || got.progress[0].BytesDone != 5 {
		t.Errorf("progress = %+v", got.progress)
	}
}

func TestBackupSummary(t *testing.T) {
	p, got := parseLines(`{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":6,"data_added":4096,"total_files_processed":10,"total_bytes_processed":1000,"snapshot_id":"abcd1234"}`)
	s, ok := p.Summary()
	want := Summary{SnapshotID: "abcd1234", FilesNew: 3, FilesChanged: 1, FilesUnmodified: 6, DataAdded: 4096, BytesProcessed: 1000}
	if !ok || s != want {
		t.Errorf("Summary = %+v, %v; want %+v", s, ok, want)
	}
	if len(got.progress) != 0 || len(got.items) != 0 {
		t.Errorf("a summary line caused callbacks: %+v", got)
	}
}

func TestItemError(t *testing.T) {
	_, got := parseLines(`{"message_type":"error","error":{"message":"open /x: permission denied"},"during":"archival","item":"/x"}`)
	if len(got.items) != 1 || got.items[0] != [2]string{"/x", "open /x: permission denied"} {
		t.Errorf("items = %q", got.items)
	}
}

func TestRestoreStatus(t *testing.T) {
	_, got := parseLines(`{"message_type":"status","seconds_elapsed":5,"percent_done":0.5,"total_files":4,"files_restored":2,"total_bytes":800,"bytes_restored":400}`)
	want := model.Progress{BytesDone: 400, BytesTotal: 800, FilesDone: 2, FilesTotal: 4, SpeedBps: 80}
	if len(got.progress) != 1 || got.progress[0] != want {
		t.Errorf("progress = %+v, want %+v", got.progress, want)
	}
}

func TestRestoreSummary(t *testing.T) {
	p, _ := parseLines(`{"message_type":"summary","total_files":4,"files_restored":4,"total_bytes":800,"bytes_restored":800}`)
	s, ok := p.Summary()
	if want := (Summary{FilesRestored: 4, BytesRestored: 800}); !ok || s != want {
		t.Errorf("Summary = %+v, %v; want %+v", s, ok, want)
	}
}

func TestGarbageIgnored(t *testing.T) {
	p, got := parseLines("not json", "", `{"message_type":"status",`, `{"message_type":"future_type"}`, `[1,2]`, `null`, `{"message_type":7}`)
	if len(got.progress) != 0 || len(got.items) != 0 {
		t.Errorf("callbacks from garbage: %+v", got)
	}
	if _, ok := p.Summary(); ok {
		t.Error("garbage produced a summary")
	}
	NewParser(nil, nil).Line([]byte(`{"message_type":"status","bytes_done":1}`)) // nil callbacks are fine
	NewParser(nil, nil).Line([]byte(`{"message_type":"error","item":"/x","error":{"message":"m"}}`))
}

func TestNoSummary(t *testing.T) {
	p, _ := parseLines(`{"message_type":"status","seconds_elapsed":1,"total_bytes":10,"bytes_done":1}`)
	if _, ok := p.Summary(); ok {
		t.Error("Summary reported without a summary line")
	}
}
