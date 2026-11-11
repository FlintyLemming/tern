package restic

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"tern/internal/model"
)

const taskID = "0123456789abcdef0123456789abcdef"

const twoSnapshots = `[{"time":"2026-11-09T03:00:01.123456789+08:00","tree":"t1","paths":["/sources/photos"],"hostname":"nas-1","username":"root","tags":["tern-` + taskID + `"],"id":"aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111","short_id":"aaaa1111"},` +
	`{"time":"2026-11-10T03:00:02Z","parent":"aaaa1111","tree":"t2","paths":["/sources/photos","/sources/docs"],"hostname":"laptop","username":"me","id":"bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222","short_id":"bbbb2222"}]`

func listWith(t *testing.T, steps ...fakeStep) ([]Snapshot, error, func() []fakeCall) {
	t.Helper()
	bin, calls := fakeRestic(t, steps...)
	iv := testInvocation(t)
	iv.Bin = bin
	if err := iv.WritePassword([]byte(repoPW)); err != nil {
		t.Fatal(err)
	}
	snaps, err := ListSnapshots(context.Background(), iv, taskID)
	return snaps, err, calls
}

func TestListSnapshotsParses(t *testing.T) {
	snaps, err, calls := listWith(t, fakeStep{Stdout: twoSnapshots + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Snapshot{
		{ID: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222", ShortID: "bbbb2222", Time: time.Date(2026, 11, 10, 3, 0, 2, 0, time.UTC),
			Host: "laptop", Paths: []string{"/sources/photos", "/sources/docs"}, Tags: []string{}, Mine: false},
		{ID: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", ShortID: "aaaa1111", Time: time.Date(2026, 11, 8, 19, 0, 1, 123456789, time.UTC),
			Host: "nas-1", Paths: []string{"/sources/photos"}, Tags: []string{"tern-" + taskID}, Mine: true},
	}
	if !reflect.DeepEqual(snaps, want) {
		t.Errorf("snapshots =\n%+v\nwant\n%+v", snaps, want)
	}
	got := calls()
	if len(got) != 1 || !got[0].Has("snapshots", "--json") || got[0].Password != repoPW {
		t.Errorf("calls = %+v", got)
	}
}

func TestListSnapshotsEmpty(t *testing.T) {
	for _, out := range []string{"[]", "null", "[]\n"} {
		snaps, err, _ := listWith(t, fakeStep{Stdout: out})
		if err != nil || snaps == nil || len(snaps) != 0 {
			t.Errorf("output %q: %v, %v; want an empty, non-nil slice", out, snaps, err)
		}
	}
}

func TestListSnapshotsMany(t *testing.T) {
	// All on one line, far beyond the line limit of the process runner.
	var b strings.Builder
	b.WriteString("[")
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"time":%q,"paths":["/sources/photos"],"hostname":"nas-1","tags":["tern-%s"],"id":"%064x","short_id":"%08x"}`,
			base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339), taskID, i, i)
	}
	b.WriteString("]")
	snaps, err, _ := listWith(t, fakeStep{Stdout: b.String()})
	if err != nil || len(snaps) != 3000 {
		t.Fatalf("got %d snapshots, %v", len(snaps), err)
	}
	if snaps[0].ShortID != fmt.Sprintf("%08x", 2999) || snaps[2999].ShortID != "00000000" {
		t.Errorf("order: first %s, last %s", snaps[0].ShortID, snaps[2999].ShortID)
	}
}

func TestListSnapshotsErrors(t *testing.T) {
	_, err, _ := listWith(t, fakeStep{Exit: 12})
	if model.KindOf(err) != model.KindAuth {
		t.Errorf("exit 12: %v", err)
	}
	_, err, _ = listWith(t, fakeStep{Exit: 1, Stderr: "ssh: connect to host nas2 port 22: Connection refused\n"})
	if model.KindOf(err) != model.KindNetwork {
		t.Errorf("exit 1: %v", err)
	}
	if _, err, _ = listWith(t, fakeStep{Stdout: "garbage"}); err == nil {
		t.Error("garbage output was accepted")
	}
}
