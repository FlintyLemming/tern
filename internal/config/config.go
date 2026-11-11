// Package config turns environment variables into a Config.
package config

import "errors"

// Config is the process configuration. Every field comes from a TERN_ variable.
type Config struct {
	Listen     string // TERN_LISTEN, default ":8080"
	DataDir    string // TERN_DATA_DIR, default "/data"
	SecretsDir string // TERN_SECRETS_DIR, default "/secrets"
	RuntimeDir string // TERN_RUNTIME_DIR, default "/dev/shm/tern"
	TLSCert    string // TERN_TLS_CERT
	TLSKey     string // TERN_TLS_KEY; both or neither

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
	}
	switch {
	case c.TLSCert != "" && c.TLSKey == "":
		return Config{}, errors.New("config: TERN_TLS_CERT is set but TERN_TLS_KEY is not; set both or neither")
	case c.TLSKey != "" && c.TLSCert == "":
		return Config{}, errors.New("config: TERN_TLS_KEY is set but TERN_TLS_CERT is not; set both or neither")
	}
	return c, nil
}
