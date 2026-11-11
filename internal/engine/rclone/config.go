// Package rclone turns the folder pairs of an SFTP or WebDAV sync task into
// rclone child processes. It implements synccore.Planner; the mirror guards
// and the dry run live in synccore.
package rclone

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode"

	"tern/internal/access"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/rundir"
	"tern/internal/sshx"
)

const configFile = "rclone.conf"

// iniValue refuses what rclone's config parser would not take literally:
// a line break would start a new key or section, surrounding white space
// is trimmed.
func iniValue(name, v string) error {
	if strings.IndexFunc(v, unicode.IsControl) >= 0 || strings.TrimSpace(v) != v {
		return fmt.Errorf("rclone: the %s of the connection cannot be written to rclone.conf", name)
	}
	return nil
}

// obscure runs `rclone obscure -`: the password reaches rclone on stdin,
// never on its command line.
func obscure(ctx context.Context, bin, pass string) (string, error) {
	var out bytes.Buffer
	res, err := proc.Run(ctx, proc.Cmd{Path: bin, Args: []string{"obscure", "-"}, Stdin: strings.NewReader(pass), Stdout: &out})
	if err != nil {
		return "", &model.KindError{Kind: model.KindConfig, Msg: "cannot start rclone", Err: err}
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("rclone obscure failed (exit %d)", res.ExitCode)
	}
	obscured := strings.TrimSpace(out.String())
	if obscured == "" || strings.ContainsAny(obscured, " \t\r\n") {
		return "", fmt.Errorf("rclone obscure gave no usable output")
	}
	return obscured, nil
}

// WriteConfig writes <dir>/rclone.conf with the one remote "remote" and
// returns its path. SFTP goes through the run's ssh_config, so rclone's
// own SSH client never sees a key; WebDAV gets its password obscured.
func WriteConfig(ctx context.Context, dir *rundir.Dir, a access.Access, bin string) (path string, err error) {
	if bin == "" {
		bin = "rclone"
	}
	var b strings.Builder
	b.WriteString("[remote]\n")
	switch a.Kind {
	case model.ConnSSH:
		// sshx refuses run directories whose path needs quoting in
		// ssh_config; the same rule makes plain double quotes enough here.
		if err := iniValue("ssh_config path", a.SSHConfig); err != nil || strings.Contains(a.SSHConfig, `"`) {
			return "", fmt.Errorf("rclone: ssh_config path %q cannot be written to rclone.conf", a.SSHConfig)
		}
		fmt.Fprintf(&b, "type = sftp\nssh = ssh -F \"%s\" %s\n", a.SSHConfig, sshx.Alias)
	case model.ConnWebDAV:
		if err := iniValue("URL", a.URL); err != nil {
			return "", err
		}
		if err := iniValue("user name", a.User); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "type = webdav\nurl = %s\nvendor = other\nuser = %s\n", a.URL, a.User)
		if a.Pass != "" {
			obscured, err := obscure(ctx, bin, a.Pass)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "pass = %s\n", obscured)
		}
	default:
		return "", fmt.Errorf("rclone: connection type %q is not supported", a.Kind)
	}
	return dir.WriteSecret(configFile, []byte(b.String()))
}
