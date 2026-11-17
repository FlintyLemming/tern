// Package config turns environment variables into a Config.
package config

import (
	"errors"
	"fmt"
	"strings"
)

// Platforms tern knows how to run on.
const (
	PlatformDocker = "docker" // the container image (default)
	PlatformQTS    = "qts"    // a QNAP package, behind QTS's web server
)

// DefaultQTSAuthURL is QTS's session check on the loopback interface.
const DefaultQTSAuthURL = "http://127.0.0.1:58080/cgi-bin/authLogin.cgi"

// Config is the process configuration. Every field comes from a TERN_ variable.
type Config struct {
	Listen     string // TERN_LISTEN, default ":8080"
	DataDir    string // TERN_DATA_DIR, default "/data"
	SecretsDir string // TERN_SECRETS_DIR, default "/secrets"
	RuntimeDir string // TERN_RUNTIME_DIR, default "/dev/shm/tern"
	TLSCert    string // TERN_TLS_CERT
	TLSKey     string // TERN_TLS_KEY; both or neither

	// BasePath (TERN_BASE_PATH) is the URL prefix tern is served under,
	// such as "/tern" behind a reverse proxy that keeps the prefix. Empty
	// means the root. Stored without a trailing slash.
	BasePath string
	// Platform (TERN_PLATFORM) is PlatformDocker or PlatformQTS. On QTS
	// tern trusts QTS administrator sign-ins (checked at QTSAuthURL,
	// TERN_QTS_AUTH_URL) and lets QTS frame it.
	Platform   string
	QTSAuthURL string

	// AllowUnmounted (TERN_ALLOW_UNMOUNTED=1) treats the data and secrets
	// directories as mounted volumes whatever they are. For development
	// and tests only: in a container it lets setup write into the
	// container's own layer, which is lost when the container is recreated.
	AllowUnmounted bool
}

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	c := Config{
		Listen:     or("TERN_LISTEN", ":8080"),
		DataDir:    or("TERN_DATA_DIR", "/data"),
		SecretsDir: or("TERN_SECRETS_DIR", "/secrets"),
		RuntimeDir: or("TERN_RUNTIME_DIR", "/dev/shm/tern"),
		TLSCert:    getenv("TERN_TLS_CERT"),
		TLSKey:     getenv("TERN_TLS_KEY"),

		AllowUnmounted: getenv("TERN_ALLOW_UNMOUNTED") == "1",
		Platform:       or("TERN_PLATFORM", PlatformDocker),
	}
	base, err := cleanBasePath(getenv("TERN_BASE_PATH"))
	if err != nil {
		return Config{}, err
	}
	c.BasePath = base
	switch c.Platform {
	case PlatformDocker:
	case PlatformQTS:
		c.QTSAuthURL = or("TERN_QTS_AUTH_URL", DefaultQTSAuthURL)
	default:
		return Config{}, fmt.Errorf("config: TERN_PLATFORM must be %q or %q, got %q", PlatformDocker, PlatformQTS, c.Platform)
	}
	switch {
	case c.TLSCert != "" && c.TLSKey == "":
		return Config{}, errors.New("config: TERN_TLS_CERT is set but TERN_TLS_KEY is not; set both or neither")
	case c.TLSKey != "" && c.TLSCert == "":
		return Config{}, errors.New("config: TERN_TLS_KEY is set but TERN_TLS_CERT is not; set both or neither")
	}
	return c, nil
}

// cleanBasePath turns "/tern/" or "/tern" into "/tern" and "" or "/" into
// "". Each segment may hold letters, digits and ._~- only, so the prefix can
// be written into HTML and cookie paths as it is.
func cleanBasePath(p string) (string, error) {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "", nil
	}
	bad := func() (string, error) {
		return "", fmt.Errorf("config: TERN_BASE_PATH must look like /tern (letters, digits and ._~- between slashes), got %q", p)
	}
	if !strings.HasPrefix(p, "/") {
		return bad()
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return bad()
		}
		for _, r := range seg {
			if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("._~-", r)) {
				return bad()
			}
		}
	}
	return p, nil
}
