package rclone

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"tern/internal/model"
)

// feed runs every line of a fixture through p.
func feed(t *testing.T, p *Parser, fixture string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		p.Line(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestParseDryRunFixture(t *testing.T) {
	p := NewParser(nil)
	feed(t, p, "dryrun.jsonl")
	plan := p.Plan()
	if plan.TransferCount != 1 || plan.DeleteCount != 2 || plan.Bytes != 7 || !slices.Equal(plan.Transfers, []string{"sub/b.txt"}) {
		t.Errorf("plan = %+v", plan)
	}
	if !slices.Contains(plan.Deletes, "old/x.txt") || !slices.Contains(plan.Deletes, "old/y.txt") {
		t.Errorf("deletes = %q", plan.Deletes)
	}
}

func TestParseCopyFixture(t *testing.T) {
	var progress []model.Progress
	p := NewParser(func(pr model.Progress) { progress = append(progress, pr) })
	feed(t, p, "copy.jsonl")
	if bytes, files := p.Totals(); bytes != 12 || files != 2 {
		t.Errorf("Totals = %d, %d; want 12, 2", bytes, files)
	}
	if len(progress) == 0 {
		t.Fatal("no progress reported")
	}
	if last := progress[len(progress)-1]; last.BytesDone != 12 || last.BytesTotal != 12 || last.FilesDone != 2 || last.FilesTotal != 2 {
		t.Errorf("last progress = %+v", last)
	}
}

func TestParseWeirdNames(t *testing.T) {
	p := NewParser(nil)
	feed(t, p, "weird.jsonl")
	plan := p.Plan()
	if plan.TransferCount != 4 || plan.DeleteCount != 1 {
		t.Errorf("plan = %+v", plan)
	}
	for _, name := range []string{"名 字.txt", "-dash.txt", "new\nline.txt"} {
		if !slices.Contains(plan.Transfers, name) {
			t.Errorf("transfers %q lack %q", plan.Transfers, name)
		}
	}
	if !slices.Equal(plan.Deletes, []string{"-old 旧.txt"}) {
		t.Errorf("deletes = %q", plan.Deletes)
	}
}

func TestParseHugePlan(t *testing.T) {
	p := NewParser(nil)
	for i := 0; i < 100000; i++ {
		p.Line(fmt.Appendf(nil, `{"level":"notice","msg":"Skipped delete as --dry-run is set (size 1)","skipped":"delete","size":1,"object":"d/f%d"}`, i))
	}
	if plan := p.Plan(); plan.DeleteCount != 100000 || len(plan.Deletes) != 1000 {
		t.Errorf("DeleteCount %d, %d listed", plan.DeleteCount, len(plan.Deletes))
	}
}

func TestParseGarbage(t *testing.T) {
	p := NewParser(func(model.Progress) {})
	for _, line := range []string{"", "not json at all", "{", "[1,2]", "123", `{"stats":"nope"}`, "\x00\xff"} {
		if got := p.Line([]byte(line)); got != line {
			t.Errorf("Line(%q) = %q, want the line unchanged", line, got)
		}
	}
	if plan := p.Plan(); plan.TransferCount != 0 || plan.DeleteCount != 0 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestLogLineReadable(t *testing.T) {
	p := NewParser(nil)
	if got := p.Line([]byte(`{"level":"error","msg":"boom","object":"a.txt"}`)); got != "ERROR a.txt: boom" {
		t.Errorf("error line = %q", got)
	}
	if got := p.Line([]byte(`{"level":"info","msg":"There was nothing to transfer"}`)); got != "INFO There was nothing to transfer" {
		t.Errorf("line without object = %q", got)
	}
	stats := `{"level":"info","msg":"\nTransferred: 12 B / 12 B\n","stats":{"bytes":12,"totalBytes":12,"transfers":2,"totalTransfers":2,"eta":null,"speed":0}}`
	if got := p.Line([]byte(stats)); got != "" {
		t.Errorf("stats line = %q, want \"\"", got)
	}
}

// "update modification time" moves no data: rclone only touches the
// destination's mtime. Nothing else in the dry run counts either.
func TestParseSkippedKinds(t *testing.T) {
	p := NewParser(nil)
	for _, skipped := range []string{"update modification time", "set directory modification time", "remove directory", "make directory"} {
		p.Line(fmt.Appendf(nil, `{"level":"notice","msg":"x","skipped":%q,"size":5,"object":"same.txt"}`, skipped))
	}
	p.Line([]byte(`{"level":"notice","msg":"x","skipped":"copy","size":6,"object":"diff.txt"}`))
	p.Line([]byte(`{"level":"notice","msg":"x","skipped":"update","size":3,"object":"other.txt"}`))
	if plan := p.Plan(); plan.TransferCount != 2 || plan.Bytes != 9 || plan.DeleteCount != 0 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestDecodeName(t *testing.T) {
	for in, want := range map[string]string{
		"plain.txt":    "plain.txt",
		"new␊line.txt": "new\nline.txt",
		"tab␉x.txt":    "tab\tx.txt",
		"del␡x.txt":    "del\x7fx.txt",
		"a‛␊b.txt":     "a␊b.txt",
		"‛‛quote.txt":  "‛quote.txt",
		"pic␀.txt":     "pic␀.txt",
		"‛x.txt":       "‛x.txt",
		"end‛":         "end‛",
	} {
		if got := decodeName(in); got != want {
			t.Errorf("decodeName(%q) = %q, want %q", in, got, want)
		}
	}
}
