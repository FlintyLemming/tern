//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"tern/internal/engine/filter"
	"tern/internal/model"
	kit "tern/test/testkit"
)

var protocols = []string{"sftp", "rsync", "webdav"}

func copyTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "expected")
	kit.WriteTree(t, dir, files)
	return dir
}

func pairTree(name string) map[string]string {
	return map[string]string{
		"a.txt":            "alpha " + name + "\n",
		"sub/b.txt":        "beta\n",
		"sub/deeper/c.bin": strings.Repeat("c", 100_000),
		"照片 one.jpg":       "not really a jpeg",
		"empty-file":       "",
	}
}

// Spec §16 item 3: three protocols, both directions, several folder pairs.
func TestSyncMatrix(t *testing.T) {
	a := kit.StartApp(t)
	for _, protocol := range protocols {
		conn := a.ConnFor(t, protocol)
		for _, direction := range []string{"push", "pull"} {
			t.Run(protocol+"/"+direction, func(t *testing.T) {
				rem := kit.NewRemoteDir(t, protocol)
				var pairs []model.FolderPair
				var check [][2]string // source, destination
				for _, name := range []string{"p1", "p2"} {
					r := rem.Sub(name)
					if direction == "push" {
						local := filepath.Join(t.TempDir(), name)
						kit.WriteTree(t, local, pairTree(name))
						pairs = append(pairs, model.FolderPair{Local: local, Remote: r.Task})
						check = append(check, [2]string{local, r.FS})
					} else {
						kit.WriteTree(t, r.FS, pairTree(name))
						local := kit.MountedDir(t)
						pairs = append(pairs, model.FolderPair{Local: local, Remote: r.Task})
						check = append(check, [2]string{r.FS, local})
					}
				}
				task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: protocol, Direction: direction, Pairs: pairs})
				run := a.RunAndWait(t, task, nil)
				a.WantRun(t, run, model.StatusSuccess)
				if len(run.Units) != 2 || run.Files != 10 {
					t.Errorf("units %+v, %d files", run.Units, run.Files)
				}
				for _, c := range check {
					kit.SameTree(t, c[0], c[1])
				}
			})
		}
	}
}

// Spec §15 asks how WebDAV servers handle modification times: whatever
// this one does is logged, only SFTP and rsync must transfer nothing.
func TestSecondRunTransfersNothing(t *testing.T) {
	a := kit.StartApp(t)
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "src")
			kit.WriteTree(t, local, pairTree("p"))
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
				Pairs: []model.FolderPair{{Local: local, Remote: kit.NewRemoteDir(t, protocol).Sub("dst").Task}}})
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			second := a.RunAndWait(t, task, nil)
			a.WantRun(t, second, model.StatusSuccess)
			switch {
			case second.Files == 0:
			case protocol == "webdav":
				t.Logf("to confirm (spec §15): an unchanged WebDAV sync transferred %d files again\n%s", second.Files, a.RunLog(second.ID))
			default:
				t.Errorf("an unchanged sync transferred %d files\n%s", second.Files, a.RunLog(second.ID))
			}
		})
	}
}

// Spec §2.3 item 2 and §16 item 5: a mirror whose source is empty or gone
// (the share is not mounted) leaves the destination alone.
func TestMirrorEmptySourceKeepsDestination(t *testing.T) {
	a := kit.StartApp(t)
	precious := map[string]string{"keep/me.txt": "precious\n", "b.txt": "b"}
	for _, protocol := range protocols {
		conn := a.ConnFor(t, protocol)
		for _, direction := range []string{"push", "pull"} {
			for _, how := range []string{"emptied", "removed"} {
				t.Run(protocol+"/"+direction+"/"+how, func(t *testing.T) {
					rem := kit.NewRemoteDir(t, protocol)
					var pair model.FolderPair
					var dst string
					if direction == "push" {
						pair = model.FolderPair{Local: filepath.Join(t.TempDir(), "src"), Remote: rem.Sub("dst").Task}
						if how == "emptied" {
							os.MkdirAll(pair.Local, 0o755)
						}
						dst = rem.Sub("dst").FS
					} else {
						src := rem.Sub("src")
						if how == "emptied" {
							os.MkdirAll(src.FS, 0o755)
						}
						pair = model.FolderPair{Local: kit.MountedDir(t), Remote: src.Task}
						dst = pair.Local
					}
					kit.WriteTree(t, dst, precious)
					task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: protocol, Direction: direction, Mode: "mirror", Pairs: []model.FolderPair{pair}})
					run := a.RunAndWait(t, task, nil)
					if run.Status != model.StatusFailed || (run.ErrorKind != model.KindGuard && run.ErrorKind != model.KindConfig) {
						t.Errorf("run = %s, %s: %s\n%s", run.Status, run.ErrorKind, run.ErrorSummary, a.RunLog(run.ID))
					}
					kit.SameTree(t, copyTree(t, precious), dst)
				})
			}
		}
	}
}

// Spec §7.3: a mirror deletes what the source no longer has, but not more
// than the limit allows unless the user lifts it for one run. Deleting a
// directory also checks that rsync's limit, which counts directories, is
// set high enough.
func TestMirrorDeletes(t *testing.T) {
	a := kit.StartApp(t)
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			tree := map[string]string{"keep.txt": "k", "x1.txt": "1", "x2.txt": "2", "gone/y.txt": "y"}
			local := filepath.Join(t.TempDir(), "src")
			kit.WriteTree(t, local, tree)
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol, Mode: "mirror",
				Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}})
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			kit.SameTree(t, local, dst.FS)

			for _, p := range []string{"x1.txt", "x2.txt", "gone"} {
				os.RemoveAll(filepath.Join(local, p))
			}
			a.UpdateTask(t, task, func(task map[string]any) { task["sync"].(map[string]any)["max_deletes"] = 2 })
			blocked := a.RunAndWait(t, task, nil)
			if blocked.Status != model.StatusFailed || blocked.ErrorKind != model.KindGuard || !strings.Contains(blocked.ErrorSummary, "would delete 3 files (limit 2)") {
				t.Errorf("run over the limit = %s, %s: %s\n%s", blocked.Status, blocked.ErrorKind, blocked.ErrorSummary, a.RunLog(blocked.ID))
			}
			kit.SameTree(t, copyTree(t, tree), dst.FS)

			allowed := a.RunAndWait(t, task, map[string]any{"allow_deletes_once": true})
			a.WantRun(t, allowed, model.StatusSuccess)
			kit.SameTree(t, local, dst.FS)
			if _, err := os.Stat(filepath.Join(dst.FS, "gone")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the deleted directory is still there: %v", err)
			}
		})
	}
}

func TestKeepNeverDeletes(t *testing.T) {
	a := kit.StartApp(t)
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "src")
			kit.WriteTree(t, local, map[string]string{"a.txt": "a", "b.txt": "b"})
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
				Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}})
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			os.Remove(filepath.Join(local, "b.txt"))
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			if got := kit.FileSet(t, dst.FS); !reflect.DeepEqual(got, []string{"a.txt", "b.txt"}) {
				t.Errorf("destination = %q", got)
			}
		})
	}
}

// One exclude syntax means the same to rclone, rsync and restic: each
// leaves out exactly what filter.Match says.
func TestExcludeConsistency(t *testing.T) {
	rules := []string{"*.tmp", "node_modules/", "/cache/", "/a/**/x.log"}
	tree := map[string]string{}
	for _, p := range []string{"keep.txt", "a.tmp", "sub/b.tmp", "sub/keep.txt", "node_modules/x.js", "sub/node_modules/y.js",
		"node_modules.txt", "cache/c.txt", "sub/cache/d.txt", "a/b/x.log", "a/b/c/x.log", "a/keep.log", "a/b/y.log"} {
		tree[p] = p
	}
	var want []string
	for p := range tree {
		if !filter.Match(rules, p, false) {
			want = append(want, p)
		}
	}
	sort.Strings(want)

	a := kit.StartApp(t)
	src := filepath.Join(kit.MountedDir(t), "tree")
	kit.WriteTree(t, src, tree)
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
				Pairs: []model.FolderPair{{Local: src, Remote: dst.Task}}, Spec: map[string]any{"excludes": rules}})
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			if got := kit.FileSet(t, dst.FS); !reflect.DeepEqual(got, want) {
				t.Errorf("synced %q\nwant %q", got, want)
			}
		})
	}
	t.Run("restic", func(t *testing.T) {
		conn := a.SSHConnection(t)
		task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{Spec: map[string]any{"excludes": rules}})
		a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
		var got []string
		for _, line := range strings.Split(a.Native(t, task, conn).OK("ls", "--json", "latest"), "\n") {
			var node struct {
				Type string `json:"type"`
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(line), &node) == nil && node.Type == "file" {
				rel, ok := strings.CutPrefix(node.Path, src+"/")
				if !ok {
					t.Fatalf("snapshot path %q is not under %s", node.Path, src)
				}
				got = append(got, rel)
			}
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("backed up %q\nwant %q", got, want)
		}
	})
}

// Review focus 1: names with spaces, CJK, a leading dash and a newline
// arrive intact both ways, and a dry run counts their deletion right.
func TestWeirdFilenames(t *testing.T) {
	weird := map[string]string{"名 字.txt": "1", "-dash.txt": "2", "new\nline.txt": "3", "sp ace/in dir.txt": "4"}
	a := kit.StartApp(t)
	for _, protocol := range protocols {
		conn := a.ConnFor(t, protocol)
		t.Run(protocol+"/push", func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "src")
			kit.WriteTree(t, local, weird)
			kit.WriteTree(t, local, map[string]string{"keep.txt": "k"})
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: protocol, Mode: "mirror", Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}})
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			kit.SameTree(t, local, dst.FS)

			for _, p := range []string{"名 字.txt", "-dash.txt", "new\nline.txt", "sp ace"} {
				os.RemoveAll(filepath.Join(local, p))
			}
			dry := a.RunAndWait(t, task, map[string]any{"kind": "dry_run"})
			a.WantRun(t, dry, model.StatusSuccess)
			if log := a.RunLog(dry.ID); !strings.Contains(log, "plan: 0 to transfer, 4 to delete") {
				t.Errorf("dry run log:\n%s", log)
			}
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			kit.SameTree(t, local, dst.FS)
		})
		t.Run(protocol+"/pull", func(t *testing.T) {
			src := kit.NewRemoteDir(t, protocol).Sub("src")
			kit.WriteTree(t, src.FS, weird)
			local := kit.MountedDir(t)
			task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: protocol, Direction: "pull", Pairs: []model.FolderPair{{Local: local, Remote: src.Task}}})
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			kit.SameTree(t, src.FS, local)
		})
	}
}

// Review focus 3: a link loop and a link out of the tree neither break nor
// hang a sync. rsync keeps links as links; rclone leaves them out.
func TestSymlinksDoNotBreak(t *testing.T) {
	a := kit.StartApp(t)
	local := filepath.Join(t.TempDir(), "src")
	kit.WriteTree(t, local, map[string]string{"a.txt": "a"})
	for name, to := range map[string]string{"loop": ".", "out": "/etc"} {
		if err := os.Symlink(to, filepath.Join(local, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol, Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}})
			began := time.Now()
			run := a.RunAndWait(t, task, nil)
			a.WantRun(t, run, model.StatusSuccess)
			if took := time.Since(began); took > 60*time.Second {
				t.Errorf("took %s", took)
			}
			for _, name := range []string{"loop", "out"} {
				fi, err := os.Lstat(filepath.Join(dst.FS, name))
				switch {
				case protocol == "rsync" && (err != nil || fi.Mode()&fs.ModeSymlink == 0):
					t.Errorf("%s: %v, %v; want a symbolic link", name, fi, err)
				case protocol != "rsync" && !errors.Is(err, fs.ErrNotExist):
					t.Errorf("%s is at the destination: %v", name, err)
				}
			}
			if got := kit.FileSet(t, dst.FS); !reflect.DeepEqual(got, []string{"a.txt"}) {
				t.Errorf("files at the destination: %q", got)
			}
		})
	}
}

// Review focus 4: a pull into a directory that is missing, or not on a
// mounted volume, fails instead of writing into the container.
func TestPullTargetNotMounted(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := kit.NewRemoteDir(t, "sftp").Sub("src")
	kit.WriteTree(t, src.FS, map[string]string{"a.txt": "a"})
	missing := filepath.Join(kit.MountedDir(t), "share")
	for _, local := range []string{missing, t.TempDir()} {
		task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: "sftp", Direction: "pull", Pairs: []model.FolderPair{{Local: local, Remote: src.Task}}})
		run := a.RunAndWait(t, task, nil)
		if run.Status != model.StatusFailed || run.ErrorKind != model.KindConfig || !strings.Contains(run.ErrorSummary, "mounted volume") {
			t.Errorf("pull into %s: %s, %s: %s", local, run.Status, run.ErrorKind, run.ErrorSummary)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the missing target was created: %v", err)
	}
}

// Spec §7.4: the destination never shows a half-written file under its
// final name.
func TestNoPartialFilesVisible(t *testing.T) {
	const size = 64 << 20
	a := kit.StartApp(t)
	local := filepath.Join(t.TempDir(), "src")
	kit.RandomFile(t, filepath.Join(local, "big.bin"), size)
	for _, protocol := range []string{"sftp", "rsync"} {
		t.Run(protocol, func(t *testing.T) {
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
				Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}, Spec: map[string]any{"limit_kib": 8192}})
			runID := a.StartRun(t, task, nil)
			seen, deadline := 0, time.Now().Add(3*time.Minute)
			for i := 0; ; i++ {
				if fi, err := os.Stat(filepath.Join(dst.FS, "big.bin")); err == nil && fi.Size() != size {
					t.Fatalf("big.bin is visible with %d of %d bytes", fi.Size(), size)
				} else if err == nil {
					seen++
				}
				if i%5 == 0 {
					var run model.Run
					a.Must(http.StatusOK, "GET", "/api/v1/runs/"+runID, nil, &run)
					if run.Status.Terminal() {
						a.WantRun(t, run, model.StatusSuccess)
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("the run did not finish")
				}
				time.Sleep(100 * time.Millisecond)
			}
			a.WaitIdle(t, task)
			if fi, err := os.Stat(filepath.Join(dst.FS, "big.bin")); err != nil || fi.Size() != size {
				t.Errorf("big.bin after the run: %v, %v", fi, err)
			}
		})
	}
}

// Spec §8.5 and §16 item 4: rsync continues a big file from where a
// canceled run left it.
func TestRsyncResumesBigFile(t *testing.T) {
	const size = 256 << 20
	a := kit.StartApp(t)
	local := filepath.Join(t.TempDir(), "src")
	kit.RandomFile(t, filepath.Join(local, "big.bin"), size)
	dst := kit.NewRemoteDir(t, "rsync").Sub("dst")
	task := a.NewSyncTask(t, a.SSHConnection(t), kit.SyncOpts{Protocol: "rsync",
		Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}, Spec: map[string]any{"limit_kib": 32768}})

	runID := a.StartRun(t, task, nil)
	a.CancelWhen(t, runID, func(p model.Progress) bool { return p.BytesDone >= size/2 })
	first := a.WaitRun(t, runID)
	a.WaitIdle(t, task)
	if first.Status != model.StatusCanceled {
		t.Fatalf("canceled run ended %s\n%s", first.Status, a.RunLog(runID))
	}

	a.UpdateTask(t, task, func(task map[string]any) { task["sync"].(map[string]any)["limit_kib"] = 0 })
	second := a.RunAndWait(t, task, nil)
	a.WantRun(t, second, model.StatusSuccess)
	if second.Bytes >= size*6/10 || second.Bytes == 0 {
		t.Errorf("the second run sent %d of %d bytes\n%s", second.Bytes, size, a.RunLog(second.ID))
	}
	kit.SameTree(t, local, dst.FS)
}

// Spec §8.5: rclone skips the files a canceled run completed. Whether it
// sends a half-transferred file again from the start is spec §15's open
// question; the answer is logged.
func TestRcloneSkipsCompleted(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)

	local := filepath.Join(t.TempDir(), "many")
	for i := 0; i < 100; i++ {
		kit.RandomFile(t, filepath.Join(local, fmt.Sprintf("f%03d.bin", i)), 1<<20)
	}
	dst := kit.NewRemoteDir(t, "sftp").Sub("many")
	task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: "sftp", Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}},
		Spec: map[string]any{"limit_kib": 8192}})
	runID := a.StartRun(t, task, nil)
	a.CancelWhen(t, runID, func(p model.Progress) bool { return p.BytesDone >= 60<<20 })
	if first := a.WaitRun(t, runID); first.Status != model.StatusCanceled {
		t.Fatalf("canceled run ended %s\n%s", first.Status, a.RunLog(runID))
	}
	a.WaitIdle(t, task)
	a.UpdateTask(t, task, func(task map[string]any) { task["sync"].(map[string]any)["limit_kib"] = 0 })
	second := a.RunAndWait(t, task, nil)
	a.WantRun(t, second, model.StatusSuccess)
	if second.Files > 45 {
		t.Errorf("the second run transferred %d files; the completed ones were sent again\n%s", second.Files, a.RunLog(second.ID))
	}
	kit.SameTree(t, local, dst.FS)

	const size = 64 << 20
	bigLocal := filepath.Join(t.TempDir(), "big")
	kit.RandomFile(t, filepath.Join(bigLocal, "big.bin"), size)
	bigDst := kit.NewRemoteDir(t, "sftp").Sub("big")
	big := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: "sftp", Pairs: []model.FolderPair{{Local: bigLocal, Remote: bigDst.Task}},
		Spec: map[string]any{"limit_kib": 8192}})
	runID = a.StartRun(t, big, nil)
	a.CancelWhen(t, runID, func(p model.Progress) bool { return p.BytesDone >= size/2 })
	a.WaitRun(t, runID)
	a.WaitIdle(t, big)
	a.UpdateTask(t, big, func(task map[string]any) { task["sync"].(map[string]any)["limit_kib"] = 0 })
	again := a.RunAndWait(t, big, nil)
	a.WantRun(t, again, model.StatusSuccess)
	t.Logf("to confirm (spec §15): after canceling half way, rclone sent %d of %d bytes of the big file again (whole file: %v)",
		again.Bytes, size, again.Bytes >= size)
}

// Spec §16 item 8: the bandwidth limit holds for every protocol.
func TestBwlimit(t *testing.T) {
	const size = 16 << 20
	a := kit.StartApp(t)
	local := filepath.Join(t.TempDir(), "src")
	kit.RandomFile(t, filepath.Join(local, "blob"), size)
	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
				Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}}, Spec: map[string]any{"limit_kib": 2048}})
			began := time.Now()
			run := a.RunAndWait(t, task, nil)
			took := time.Since(began)
			a.WantRun(t, run, model.StatusSuccess)
			if took < 6*time.Second {
				t.Errorf("16 MiB at 2048 KiB/s took %s, want at least 6s", took)
			}
		})
	}
}

// Spec §16 item 8: rclone moves as many files at once as configured.
func TestTransfersConcurrency(t *testing.T) {
	a := kit.StartApp(t)
	local := filepath.Join(t.TempDir(), "src")
	for i := 0; i < 20; i++ {
		kit.RandomFile(t, filepath.Join(local, fmt.Sprintf("f%02d.bin", i)), 8<<20)
	}
	dst := kit.NewRemoteDir(t, "sftp").Sub("dst")
	task := a.NewSyncTask(t, a.SSHConnection(t), kit.SyncOpts{Protocol: "sftp", Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}},
		Spec: map[string]any{"transfers": 4, "limit_kib": 16384}})
	runID := a.StartRun(t, task, nil)
	most, deadline := 0, time.Now().Add(3*time.Minute)
	for i := 0; ; i++ {
		entries, _ := os.ReadDir(dst.FS)
		n := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".partial") {
				n++
			}
		}
		most = max(most, n)
		if i%5 == 0 {
			var run model.Run
			a.Must(http.StatusOK, "GET", "/api/v1/runs/"+runID, nil, &run)
			if run.Status.Terminal() {
				a.WantRun(t, run, model.StatusSuccess)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the run did not finish")
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.WaitIdle(t, task)
	if most != 4 {
		t.Errorf("at most %d files were in transfer at once, want 4", most)
	}
}

// A remote end without rsync is a configuration problem: retrying will not
// install it.
func TestRemoteRsyncMissing(t *testing.T) {
	a := kit.StartApp(t)
	host := kit.Env(t, "TERN_ITEST_NORSYNC_HOST")
	conn := a.SSHConnectionAt(t, host, host)
	local := filepath.Join(t.TempDir(), "src")
	kit.WriteTree(t, local, map[string]string{"a.txt": "a"})
	task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: "rsync", Pairs: []model.FolderPair{{Local: local, Remote: "/home/backup/dst"}},
		Task: map[string]any{"retry": map[string]any{"retries": 1, "delays_sec": []int{1}}}})
	run := a.RunAndWait(t, task, nil)
	if run.Status != model.StatusFailed || run.ErrorKind != model.KindConfig || run.Attempts != 1 {
		t.Errorf("run = %s, %s, %d attempts: %s\n%s", run.Status, run.ErrorKind, run.Attempts, run.ErrorSummary, a.RunLog(run.ID))
	}
}

// Spec §9.2: a changed host key stops the sync and is not retried. The
// runner cannot rebuild the sshd container, so the connection confirms
// the key of another server instead: to the client, the same thing.
func TestHostKeyChangeStopsSync(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnectionAt(t, kit.Env(t, "TERN_ITEST_SSH_HOST"), kit.Env(t, "TERN_ITEST_NORSYNC_HOST"))
	local := filepath.Join(t.TempDir(), "src")
	kit.WriteTree(t, local, map[string]string{"a.txt": "a"})
	for _, protocol := range []string{"sftp", "rsync"} {
		t.Run(protocol, func(t *testing.T) {
			dst := kit.NewRemoteDir(t, protocol).Sub("dst")
			task := a.NewSyncTask(t, conn, kit.SyncOpts{Protocol: protocol, Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}},
				Task: map[string]any{"retry": map[string]any{"retries": 1, "delays_sec": []int{1}}}})
			run := a.RunAndWait(t, task, nil)
			if run.Status != model.StatusFailed || run.ErrorKind != model.KindHostKey || run.Attempts != 1 {
				t.Errorf("run = %s, %s, %d attempts: %s\n%s", run.Status, run.ErrorKind, run.Attempts, run.ErrorSummary, a.RunLog(run.ID))
			}
			if files := kit.FileSet(t, dst.FS); len(files) != 0 {
				t.Errorf("files arrived: %q", files)
			}
		})
	}
}
