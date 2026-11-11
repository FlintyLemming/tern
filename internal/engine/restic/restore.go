package restic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"tern/internal/engine"
	"tern/internal/model"
	"tern/internal/mounts"
)

const unitRestore = "restore"

// RestoreParams says what to restore and where to.
type RestoreParams struct {
	SnapshotID string
	Subpath    string // "" = the whole snapshot
	TargetDir  string
}

// ParamError is a restore parameter tern refuses. Field and Code are for
// the API's field errors; codes for target_dir are not_absolute,
// not_empty, not_mounted, not_writable, inside_source and inside_data.
type ParamError struct {
	Field, Code string
	Msg         string
}

func (e *ParamError) Error() string { return e.Msg }

// Full or abbreviated snapshot IDs only: nothing restic would read as
// "latest", a flag or a path.
var snapshotIDRE = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

func hasControl(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0
}

// ParseRestoreParams reads the parameters of a restore run from the keys
// snapshot_id, subpath and target_dir. The target is only required here;
// ValidateRestoreTarget judges it.
func ParseRestoreParams(m map[string]string) (RestoreParams, error) {
	p := RestoreParams{SnapshotID: m["snapshot_id"], Subpath: m["subpath"], TargetDir: m["target_dir"]}
	if !snapshotIDRE.MatchString(p.SnapshotID) {
		return p, &ParamError{Field: "snapshot_id", Code: "invalid", Msg: "the snapshot ID must be 8 to 64 lower-case hex digits"}
	}
	if s := p.Subpath; s != "" && (hasControl(s) || !filepath.IsAbs(s) || filepath.Clean(s) != s) {
		return p, &ParamError{Field: "subpath", Code: "invalid", Msg: "the path inside the snapshot must be absolute and clean, like /photos/2024"}
	}
	if p.TargetDir == "" {
		return p, &ParamError{Field: "target_dir", Code: "required", Msg: "a restore needs a target directory"}
	}
	return p, nil
}

// within reports whether p is dir or lies inside it.
func within(p, dir string) bool {
	if dir == "" {
		return false
	}
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// resolved follows the symbolic links of the part of p that exists.
func resolved(p string) string {
	base, err := mounts.NearestExisting(p)
	if err != nil {
		return p
	}
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		return p
	}
	return filepath.Join(real, strings.TrimPrefix(p, base))
}

// ValidateRestoreTarget accepts target as the directory of a restore only
// if nothing can be overwritten or lost by it: an absolute, clean path
// that does not exist yet or is an empty directory, on a mounted volume
// that can be written to, and neither a source directory, the data
// directory or the secrets directory nor inside one of them. Version one
// has no way to restore over existing files. Errors are *ParamError.
func ValidateRestoreTarget(target string, sources []string, dataDir, secretsDir string, mounted func(string) (bool, error)) error {
	bad := func(code, format string, args ...any) error {
		return &ParamError{Field: "target_dir", Code: code, Msg: fmt.Sprintf(format, args...)}
	}
	if target == "" || hasControl(target) || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return bad("not_absolute", "the restore target must be an absolute path without \"..\" or a trailing slash")
	}
	// Both as written and with symbolic links followed.
	forms := []string{target, resolved(target)}
	for _, form := range forms {
		for _, src := range sources {
			if within(form, src) || within(form, resolved(src)) {
				return bad("inside_source", "the restore target is inside the source directory %s", src)
			}
		}
		for _, dir := range []string{dataDir, secretsDir} {
			if dir != "" && (within(form, dir) || within(form, resolved(dir))) {
				return bad("inside_data", "the restore target is inside tern's own directory %s", dir)
			}
		}
	}

	switch fi, err := os.Lstat(target); {
	case err == nil && !fi.IsDir():
		return bad("not_empty", "the restore target %s exists and is not a directory", target)
	case err == nil:
		entries, err := os.ReadDir(target)
		if err != nil {
			return bad("not_writable", "the restore target %s cannot be read", target)
		}
		if len(entries) > 0 {
			return bad("not_empty", "the restore target %s is not empty; restore into a new or empty directory", target)
		}
	case !os.IsNotExist(err):
		return bad("not_writable", "the restore target %s cannot be examined", target)
	}

	if ok, err := mounted(target); err != nil || !ok {
		return bad("not_mounted", "the restore target %s is not on a mounted volume; what is written there is lost with the container", target)
	}

	// The only reliable test of "writable" (read-only mounts, ownership,
	// ACLs) is to write.
	parent, err := mounts.NearestExisting(target)
	if err != nil {
		return bad("not_writable", "the restore target %s cannot be examined", target)
	}
	probe, err := os.CreateTemp(parent, ".tern-write-test-*")
	if err != nil {
		return bad("not_writable", "the restore target is not writable: cannot create files in %s", parent)
	}
	probe.Close()
	os.Remove(probe.Name())
	return nil
}

type restoreEngine struct {
	*session
	p         RestoreParams
	validated bool // the target passed validation in this run
}

func (e *restoreEngine) Units() []string { return []string{unitRestore} }

func (e *restoreEngine) Preflight(ctx context.Context, env engine.Env) error {
	// The API checked the target when the restore was requested; things
	// may have changed while the run was queued. A retry skips this: it
	// finds what its own first attempt restored.
	if !e.validated {
		mounted := e.d.Mounted
		if mounted == nil {
			mounted = mounts.IsMounted
		}
		if err := ValidateRestoreTarget(e.p.TargetDir, e.spec().Sources, e.d.DataDir, e.d.SecretsDir, mounted); err != nil {
			return &model.KindError{Kind: model.KindConfig, Msg: err.Error()}
		}
		e.validated = true
	}
	if err := e.check(ctx); err != nil {
		return err
	}
	return e.ensureOpen(ctx, env)
}

func (e *restoreEngine) what() string {
	if e.p.Subpath == "" {
		return e.p.SnapshotID
	}
	return e.p.SnapshotID + ":" + e.p.Subpath
}

func (e *restoreEngine) RunUnit(ctx context.Context, env engine.Env, _ int) (model.UnitResult, error) {
	out := model.UnitResult{Name: unitRestore, Status: model.StatusSuccess}
	parser := NewParser(
		func(p model.Progress) {
			p.Unit = unitRestore
			if env.Progress != nil {
				env.Progress(p)
			}
		},
		func(item, msg string) { fmt.Fprintf(env.Log, "not restored: %s: %s\n", item, msg) },
	)
	res, err := e.exec(ctx, env, parser.Line, "restore", e.what(), "--target", e.p.TargetDir, "--json")
	if err != nil {
		return out, err
	}
	if sum, ok := parser.Summary(); ok {
		out.Bytes, out.Files = sum.BytesRestored, sum.FilesRestored
		fmt.Fprintf(env.Log, "restored %d files, %d bytes to %s\n", sum.FilesRestored, sum.BytesRestored, e.p.TargetDir)
	}
	if res.ExitCode != exitOK {
		return out, e.failure(env, "restore", res)
	}
	return out, nil
}

func (e *restoreEngine) Describe() []string {
	return append(e.exportLines(), "restic restore "+shQuote(e.what())+" --target "+shQuote(e.p.TargetDir))
}
