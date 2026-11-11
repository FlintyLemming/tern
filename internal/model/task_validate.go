package model

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"tern/internal/engine/filter"
)

const maxScriptLen = 64 << 10

// CheckSchedule accepts "" (manual only) or a standard 5-field cron
// expression that fires at least once in the next five years. It returns ""
// or an error code.
func CheckSchedule(expr string) string {
	if expr == "" {
		return ""
	}
	// ParseStandard also takes "@every 1h" and "TZ=…" prefixes; tern does not.
	if len(strings.Fields(expr)) != 5 || len(expr) > 256 {
		return "invalid"
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return "invalid"
	}
	if sched.Next(time.Now()).IsZero() { // the parser gives up after five years
		return "never_fires"
	}
	return ""
}

func hasControl(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0
}

// checkLocalPath: absolute, already clean, no control characters.
func checkLocalPath(p string) string {
	switch {
	case p == "":
		return "required"
	case hasControl(p) || !filepath.IsAbs(p) || filepath.Clean(p) != p:
		return "invalid"
	}
	return ""
}

func checkRemotePath(p string) string {
	switch {
	case p == "":
		return "required"
	case hasControl(p):
		return "invalid"
	}
	return ""
}

func oneOf(v string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return ""
		}
	}
	return "invalid"
}

// Validate returns field name → error code; an empty map means valid.
// Call ApplyDefaults first.
func (t Task) Validate() map[string]string {
	f := map[string]string{}
	set := func(field, code string) {
		if code != "" {
			f[field] = code
		}
	}
	nonNeg := func(field string, v int) {
		if v < 0 {
			f[field] = "invalid"
		}
	}

	excludes := func(field string, rules []string) {
		for i, rule := range rules {
			if filter.Validate(rule) != nil {
				f[fmt.Sprintf("%s[%d]", field, i)] = "invalid"
			}
		}
	}

	set("name", CheckName(t.Name))
	set("schedule", CheckSchedule(t.Schedule))
	nonNeg("stale_after_sec", t.StaleAfterSec)
	nonNeg("max_runtime_sec", t.MaxRuntimeSec)

	if t.Retry.Retries < 0 || t.Retry.Retries > 10 {
		f["retry.retries"] = "invalid"
	}
	if len(t.Retry.DelaysSec) > 10 {
		f["retry.delays_sec"] = "invalid"
	}
	for i, d := range t.Retry.DelaysSec {
		if d < 1 || d > 86400 {
			f[fmt.Sprintf("retry.delays_sec[%d]", i)] = "invalid"
		}
	}

	for name, h := range map[string]Hook{"pre": t.Pre, "post": t.Post} {
		set(name+".where", oneOf(h.Where, HookLocal, HookSSH))
		if h.Where == HookSSH && h.ConnectionID == "" {
			f[name+".connection_id"] = "required"
		}
		if h.TimeoutSec < 1 || h.TimeoutSec > 86400 {
			f[name+".timeout_sec"] = "invalid"
		}
		if len(h.Script) > maxScriptLen {
			f[name+".script"] = "too_long"
		} else if strings.ContainsRune(h.Script, 0) || !utf8.ValidString(h.Script) {
			f[name+".script"] = "invalid"
		}
	}

	switch {
	case t.Type == TaskBackup && t.Backup != nil && t.Sync == nil:
		b := t.Backup
		if b.ConnectionID == "" {
			f["backup.connection_id"] = "required"
		}
		set("backup.repo_path", checkRemotePath(b.RepoPath))
		if hasControl(b.Host) {
			f["backup.host"] = "invalid"
		}
		if len(b.Sources) == 0 {
			f["backup.sources"] = "required"
		}
		for i, src := range b.Sources {
			set(fmt.Sprintf("backup.sources[%d]", i), checkLocalPath(src))
		}
		excludes("backup.excludes", b.Excludes)
		nonNeg("backup.limit_upload_kib", b.LimitUploadKiB)
		if b.Connections < 1 || b.Connections > 64 {
			f["backup.connections"] = "invalid"
		}
		nonNeg("backup.keep.last", b.Keep.Last)
		nonNeg("backup.keep.daily", b.Keep.Daily)
		nonNeg("backup.keep.weekly", b.Keep.Weekly)
		nonNeg("backup.keep.monthly", b.Keep.Monthly)
		nonNeg("backup.prune_every_days", b.PruneEveryDays)
		nonNeg("backup.check_every_days", b.CheckEveryDays)
	case t.Type == TaskSync && t.Sync != nil && t.Backup == nil:
		s := t.Sync
		set("sync.protocol", oneOf(s.Protocol, "sftp", "rsync", "webdav"))
		if s.ConnectionID == "" {
			f["sync.connection_id"] = "required"
		}
		set("sync.direction", oneOf(s.Direction, "push", "pull"))
		set("sync.delete_mode", oneOf(s.DeleteMode, "keep", "mirror"))
		set("sync.compare", oneOf(s.Compare, "size_mtime", "checksum"))
		if len(s.Pairs) == 0 {
			f["sync.pairs"] = "required"
		}
		for i, p := range s.Pairs {
			set(fmt.Sprintf("sync.pairs[%d].local", i), checkLocalPath(p.Local))
			set(fmt.Sprintf("sync.pairs[%d].remote", i), checkRemotePath(p.Remote))
		}
		pairOverlaps(s.Pairs, f)
		excludes("sync.excludes", s.Excludes)
		nonNeg("sync.max_deletes", s.MaxDeletes)
		nonNeg("sync.limit_kib", s.LimitKiB)
		if s.Transfers < 0 || s.Transfers > 64 || (s.Protocol == "rsync" && s.Transfers != 0) {
			f["sync.transfers"] = "invalid"
		}
	default:
		f["type"] = "invalid"
	}
	return f
}

// nested reports whether two clean paths are the same or one lies inside
// the other. "/" holds every absolute path, "." every relative one.
func nested(a, b string) bool {
	under := func(child, parent string) bool {
		switch parent {
		case "/":
			return strings.HasPrefix(child, "/")
		case ".":
			return !strings.HasPrefix(child, "/")
		}
		return strings.HasPrefix(child, parent+"/")
	}
	return a == b || under(a, b) || under(b, a)
}

// pairOverlaps marks a folder pair whose local or remote path is the same
// as, or nested with, that of an earlier pair of the task: two pairs would
// then sync the same files, and a mirror of one could delete what the other
// just brought. Paths that are invalid already keep that error.
func pairOverlaps(pairs []FolderPair, f map[string]string) {
	for i := range pairs {
		for j := 0; j < i; j++ {
			for _, side := range []struct {
				name string
				a, b string
			}{
				{"local", filepath.Clean(pairs[j].Local), filepath.Clean(pairs[i].Local)},
				{"remote", path.Clean(pairs[j].Remote), path.Clean(pairs[i].Remote)},
			} {
				field := fmt.Sprintf("sync.pairs[%d].%s", i, side.name)
				earlier := fmt.Sprintf("sync.pairs[%d].%s", j, side.name)
				if f[field] == "" && (f[earlier] == "" || f[earlier] == "overlaps") && nested(side.a, side.b) {
					f[field] = "overlaps"
				}
			}
		}
	}
}
