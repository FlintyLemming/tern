package restic

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/redact"
)

// RepoState says what Attach found at the repository path.
type RepoState string

const (
	RepoNew      RepoState = "new"      // nothing there: a repository was initialized
	RepoExisting RepoState = "existing" // a repository the password opens
)

// Attach makes sure there is a repository at iv.RepoPath that password
// opens: an existing one is only read, an empty path gets a new repository.
// A repository that refuses the password is a KindAuth error and is left
// alone. The password file is written into iv.Dir.
func Attach(ctx context.Context, iv Invocation, password []byte) (RepoState, error) {
	if err := iv.WritePassword(password); err != nil {
		return "", fmt.Errorf("restic: write the password file: %w", err)
	}
	red := redact.New()
	red.Add(password)
	run := func(args ...string) (proc.Result, error) {
		res, err := proc.Run(ctx, iv.Cmd(args...))
		if err != nil {
			return res, &model.KindError{Kind: model.KindConfig, Msg: "cannot start restic", Err: err}
		}
		if res.Signaled {
			return res, &model.KindError{Kind: model.KindUnknown, Msg: "restic " + args[0] + " was stopped", Err: context.Cause(ctx)}
		}
		return res, nil
	}

	res, err := run("cat", "config")
	if err != nil {
		return "", err
	}
	switch res.ExitCode {
	case exitOK:
		return RepoExisting, nil
	case exitNoRepository:
	default:
		return "", exitError(iv.RepoPath, "cat config", res, red)
	}
	if res, err = run("init"); err != nil {
		return "", err
	}
	if res.ExitCode != exitOK {
		return "", exitError(iv.RepoPath, "init", res, red)
	}
	return RepoNew, nil
}

// RecoveryRepository is the repository address a person gives to restic to
// reach the repository without tern: over c, at repoPath.
//
// restic has two spellings. The short one cannot carry a port or an IPv6
// address; in the URL one an absolute path shows up as "//".
func RecoveryRepository(c model.Connection, repoPath string) string {
	ipv6 := strings.Contains(c.Host, ":")
	if c.Port == 22 && !ipv6 {
		return fmt.Sprintf("sftp:%s@%s:%s", c.Username, c.Host, repoPath)
	}
	host := c.Host
	if ipv6 {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("sftp://%s@%s:%d/%s", c.Username, host, c.Port, repoPath)
}

// Recovery is what a person needs to get at the backups without tern.
type Recovery struct {
	Repository string
	Password   string
	Commands   []string
}

// RecoveryInfo is the recovery information of a repository: where it is,
// its password, and the commands that restore from it with nothing but
// restic.
func RecoveryInfo(c model.Connection, repoPath, password string) Recovery {
	repo := RecoveryRepository(c, repoPath)
	return Recovery{Repository: repo, Password: password, Commands: []string{
		"export RESTIC_REPOSITORY=" + singleQuote(repo),
		"export RESTIC_PASSWORD=" + singleQuote(password),
		"restic snapshots",
		"restic restore latest --target /path/to/restore",
	}}
}

// ValidateRepoPassword accepts 1 to 1024 bytes of text without control
// characters that neither starts nor ends with white space: restic trims
// a password read from a file, so such a password would not be the one
// the user sees.
func ValidateRepoPassword(pw string) error {
	switch {
	case pw == "":
		return errors.New("restic: the repository password is empty")
	case len(pw) > 1024:
		return errors.New("restic: the repository password is longer than 1024 bytes")
	case !utf8.ValidString(pw) || strings.IndexFunc(pw, unicode.IsControl) >= 0:
		return errors.New("restic: the repository password has control characters")
	}
	first, _ := utf8.DecodeRuneInString(pw)
	last, _ := utf8.DecodeLastRuneInString(pw)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return errors.New("restic: the repository password starts or ends with white space")
	}
	return nil
}

const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// GenerateRepoPassword returns 32 random characters from [A-Za-z0-9],
// about 190 bits.
func GenerateRepoPassword() string {
	const n = 32
	out := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic("restic: no randomness: " + err.Error())
		}
		for _, b := range buf {
			// 248 is the largest multiple of 62 below 256: no modulo bias.
			if b < 248 && len(out) < n {
				out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			}
		}
	}
	return string(out)
}

// shQuote writes s as one word of a POSIX shell command line.
func shQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return singleQuote(s)
}

func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
