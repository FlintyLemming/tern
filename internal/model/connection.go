package model

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ConnSSH    = "ssh"
	ConnWebDAV = "webdav"
)

// Connection is one remote end. Secrets (the WebDAV password) stay sealed in
// the store; this type only says whether one is set.
type Connection struct {
	ID      string
	Version int
	Type    string // ConnSSH | ConnWebDAV
	Name    string

	// SSH
	Host               string // hostname or IP literal; IPv6 without brackets
	Port               int
	SSHKeyID           string
	HostKey            string // the confirmed host key, authorized_keys form
	HostKeyFingerprint string // derived from HostKey, never stored

	// WebDAV
	URL         string
	HasPassword bool

	Username  string // both types
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ApplyDefaults fills in what the user may leave out.
func (c *Connection) ApplyDefaults() {
	if c.Type == ConnSSH && c.Port == 0 {
		c.Port = 22
	}
}

var (
	sshUserRE   = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)
	hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// CheckName validates a display name: 1–64 characters, no control characters,
// no surrounding whitespace. It returns "" or an error code.
func CheckName(s string) string {
	switch {
	case s == "":
		return "required"
	case utf8.RuneCountInString(s) > 64:
		return "too_long"
	case !utf8.ValidString(s) || s != strings.TrimSpace(s) || strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "invalid"
	}
	return ""
}

// CheckHost accepts an RFC 1123 hostname or an IP literal (IPv6 without
// brackets or zone). Everything that could mean something to ssh or ssh_config
// — whitespace, a leading '-', '%' tokens — falls outside both. It returns ""
// or an error code.
func CheckHost(h string) string {
	if h == "" {
		return "required"
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		if addr.Zone() != "" {
			return "invalid"
		}
		return ""
	}
	if len(h) > 253 {
		return "invalid"
	}
	for _, label := range strings.Split(h, ".") {
		if !hostLabelRE.MatchString(label) {
			return "invalid"
		}
	}
	return ""
}

// CheckPort returns "" for 1–65535.
func CheckPort(p int) string {
	if p < 1 || p > 65535 {
		return "invalid"
	}
	return ""
}

func checkURL(raw string) string {
	if raw == "" {
		return "required"
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f || r == utf8.RuneError {
			return "invalid"
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "scheme"
	}
	if u.User != nil {
		return "embedded_credentials"
	}
	if u.Hostname() == "" || u.Fragment != "" || u.Opaque != "" {
		return "invalid"
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || CheckPort(n) != "" {
			return "invalid"
		}
	}
	return ""
}

// Validate returns field name → error code; an empty map means valid.
// Call ApplyDefaults first.
func (c Connection) Validate() map[string]string {
	f := map[string]string{}
	set := func(field, code string) {
		if code != "" {
			f[field] = code
		}
	}
	notAllowed := func(field string, present bool) {
		if present {
			f[field] = "not_allowed"
		}
	}
	set("name", CheckName(c.Name))
	switch c.Type {
	case ConnSSH:
		set("host", CheckHost(c.Host))
		set("port", CheckPort(c.Port))
		switch {
		case c.Username == "":
			f["username"] = "required"
		case !sshUserRE.MatchString(c.Username):
			f["username"] = "invalid"
		}
		if c.SSHKeyID == "" {
			f["ssh_key_id"] = "required"
		}
		notAllowed("url", c.URL != "")
	case ConnWebDAV:
		set("url", checkURL(c.URL))
		// WebDAV user names are often e-mail addresses, so the SSH pattern does
		// not apply; a colon cannot be expressed in Basic authentication.
		if len(c.Username) > 255 || !utf8.ValidString(c.Username) ||
			strings.ContainsRune(c.Username, ':') || strings.IndexFunc(c.Username, unicode.IsControl) >= 0 {
			f["username"] = "invalid"
		}
		notAllowed("host", c.Host != "")
		notAllowed("port", c.Port != 0)
		notAllowed("ssh_key_id", c.SSHKeyID != "")
		notAllowed("host_key", c.HostKey != "")
	default:
		f["type"] = "invalid"
	}
	return f
}
