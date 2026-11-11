package restic

import (
	"fmt"
	"strings"

	"tern/internal/model"
)

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
