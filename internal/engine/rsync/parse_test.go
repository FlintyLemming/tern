package rsync

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"tern/internal/model"
)

// feed runs a fixture through p the way proc delivers it: lines end at
// \n or \r, empty ones are skipped.
func feed(t *testing.T, p *Parser, fixture string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			p.Line(sc.Bytes())
		}
	}
}

// The example line of the plan, in the format of rsync before 3.4: plain
// byte counts and 1024-based rates.
const exampleLine = "    1,238,099  42%   14.98MB/s    0:00:07 (xfr#5, to-chk=169/396)"

func progressOf(t *testing.T, line string) model.Progress {
	t.Helper()
	var got []model.Progress
	p := NewParser(func(pr model.Progress) { got = append(got, pr) })
	if text := p.Line([]byte(line)); text != "" {
		t.Errorf("Line(%q) = %q, want \"\" for a progress line", line, text)
	}
	if len(got) != 1 {
		t.Fatalf("Line(%q): %d progress reports", line, len(got))
	}
	return got[0]
}

func TestParseProgressLine(t *testing.T) {
	pr := progressOf(t, exampleLine)
	if pr.BytesDone != 1238099 || pr.BytesTotal != 2947855 || pr.FilesDone != 227 || pr.FilesTotal != 396 || pr.ETASec != 7 {
		t.Errorf("progress = %+v", pr)
	}
	if math.Abs(pr.SpeedBps-15707668) > 1 {
		t.Errorf("speed = %f, want 15707668±1", pr.SpeedBps)
	}
}

func TestParseProgressUnits(t *testing.T) {
	for line, want := range map[string]float64{
		"    1,238,099  42%  512.00kB/s    0:00:07": 524288,
		"    1,238,099  42%    1.00GB/s    0:00:07": 1073741824,
	} {
		if pr := progressOf(t, line); math.Abs(pr.SpeedBps-want) > 1 {
			t.Errorf("%q: speed = %f, want %f", line, pr.SpeedBps, want)
		}
	}
}

// rsync 3.4 and later (the pinned image has 3.5.2) print byte counts with
// a "B" and rates in units of 1000: progress.c renders the rate at human
// level 2.
func TestParseProgressCurrentFormat(t *testing.T) {
	pr := progressOf(t, "      2,097,152B  31%   2.05MB/s    0:00:02  ")
	if pr.BytesDone != 2097152 || pr.BytesTotal != 6765006 || pr.ETASec != 2 || math.Abs(pr.SpeedBps-2050000) > 1 {
		t.Errorf("progress = %+v", pr)
	}
	pr = progressOf(t, "             12B 100%   7.00KB/s    0:00:00 (xfr#2, to-chk=0/4)")
	if pr.BytesDone != 12 || pr.BytesTotal != 12 || pr.FilesDone != 4 || pr.FilesTotal != 4 || math.Abs(pr.SpeedBps-7000) > 1 {
		t.Errorf("progress = %+v", pr)
	}
	if pr := progressOf(t, "              5B   0%       0B/s    0:00:00  "); pr.BytesDone != 5 || pr.BytesTotal != 0 || pr.SpeedBps != 0 {
		t.Errorf("progress at 0%% = %+v", pr)
	}
}

func TestParseDryRunFixture(t *testing.T) {
	p := NewParser(nil)
	feed(t, p, "dryrun.txt")
	plan := p.Plan()
	if plan.TransferCount != 1 || plan.DeleteCount != 2 || plan.Bytes != 7 || !slices.Equal(plan.Transfers, []string{"sub/b.txt"}) {
		t.Errorf("plan = %+v", plan)
	}
	if slices.Contains(plan.Deletes, "old/") {
		t.Errorf("the deleted directory was counted: %q", plan.Deletes)
	}
	if p.DirDeletes() != 1 {
		t.Errorf("DirDeletes = %d, want 1", p.DirDeletes())
	}
}

func TestParseCopyFixture(t *testing.T) {
	var progress []model.Progress
	p := NewParser(func(pr model.Progress) { progress = append(progress, pr) })
	feed(t, p, "copy.txt")
	if bytes, files := p.Totals(); bytes != 12 || files != 2 {
		t.Errorf("Totals = %d, %d; want 12, 2", bytes, files)
	}
	if len(progress) == 0 {
		t.Error("no progress reported")
	}
}

func TestParseWeirdNames(t *testing.T) {
	p := NewParser(nil)
	feed(t, p, "weird.txt")
	plan := p.Plan()
	if plan.TransferCount != 4 || plan.DeleteCount != 1 {
		t.Errorf("plan = %+v", plan)
	}
	for _, name := range []string{"new\nline.txt", "-dash.txt", "名 字.txt", "plain.txt"} {
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
		p.Line(fmt.Appendf(nil, "*deleting   f%d", i))
	}
	if plan := p.Plan(); plan.DeleteCount != 100000 || len(plan.Deletes) != 1000 {
		t.Errorf("DeleteCount %d, %d listed", plan.DeleteCount, len(plan.Deletes))
	}
}

func TestParseThousandsSeparators(t *testing.T) {
	p := NewParser(nil)
	p.Line([]byte("Literal data: 1,234,567 bytes"))
	p.Line([]byte("Number of regular files transferred: 1,024"))
	if bytes, files := p.Totals(); bytes != 1234567 || files != 1024 {
		t.Errorf("Totals = %d, %d", bytes, files)
	}
}

// rsync writes "\#ooo" for bytes it will not print raw, and for a
// backslash that would otherwise read like such an escape.
func TestDecodeEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"plain.txt":           "plain.txt",
		`new\#012line.txt`:    "new\nline.txt",
		`cr\#015x`:            "cr\rx",
		`lit\#134#123.txt`:    `lit\#123.txt`,
		`back\slash.txt`:      `back\slash.txt`,
		`short\#01`:           `short\#01`,
		"tab\tx.txt":          "tab\tx.txt",
		`\#342\#200\#233.txt`: "\xe2\x80\x9b.txt",
	} {
		if got := decodeName(in); got != want {
			t.Errorf("decodeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEntryLinesLogged(t *testing.T) {
	p := NewParser(nil)
	if got := p.Line([]byte(">f+++++++++ a.txt")); got != ">f+++++++++ a.txt" {
		t.Errorf("entry line = %q", got)
	}
	if got := p.Line([]byte("Literal data: 12 bytes")); got != "Literal data: 12 bytes" {
		t.Errorf("stats line = %q", got)
	}
}
