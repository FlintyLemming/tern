package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	got, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Listen: ":8080", DataDir: "/data", SecretsDir: "/secrets", RuntimeDir: "/dev/shm/tern", Platform: PlatformDocker}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	got, err := Load(env(map[string]string{
		"TERN_LISTEN":      "127.0.0.1:9000",
		"TERN_DATA_DIR":    "/d",
		"TERN_SECRETS_DIR": "/s",
		"TERN_RUNTIME_DIR": "/r",
		"TERN_TLS_CERT":    "/c.pem",
		"TERN_TLS_KEY":     "/k.pem",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Listen: "127.0.0.1:9000", DataDir: "/d", SecretsDir: "/s", RuntimeDir: "/r", TLSCert: "/c.pem", TLSKey: "/k.pem", Platform: PlatformDocker}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadTLSNeedsBoth(t *testing.T) {
	_, err := Load(env(map[string]string{"TERN_TLS_CERT": "/c.pem"}))
	if err == nil || !strings.Contains(err.Error(), "TERN_TLS_KEY") {
		t.Fatalf("cert only: got %v, want error naming TERN_TLS_KEY", err)
	}
	_, err = Load(env(map[string]string{"TERN_TLS_KEY": "/k.pem"}))
	if err == nil || !strings.Contains(err.Error(), "TERN_TLS_CERT") {
		t.Fatalf("key only: got %v, want error naming TERN_TLS_CERT", err)
	}
}

func TestLoadAllowUnmounted(t *testing.T) {
	for v, want := range map[string]bool{"": false, "1": true, "0": false, "true": false} {
		got, err := Load(env(map[string]string{"TERN_ALLOW_UNMOUNTED": v}))
		if err != nil {
			t.Fatal(err)
		}
		if got.AllowUnmounted != want {
			t.Errorf("TERN_ALLOW_UNMOUNTED=%q: AllowUnmounted = %v, want %v", v, got.AllowUnmounted, want)
		}
	}
}

func TestLoadBasePath(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "/tern": "/tern", "/tern/": "/tern", "/apps/tern-2": "/apps/tern-2", "/a.b~c_d": "/a.b~c_d"} {
		got, err := Load(env(map[string]string{"TERN_BASE_PATH": in}))
		if err != nil || got.BasePath != want {
			t.Errorf("TERN_BASE_PATH=%q: BasePath = %q, %v; want %q", in, got.BasePath, err, want)
		}
	}
	for _, in := range []string{"tern", "/te rn", "/a//b", "/..", "/./x", "/x\"y", "/x?y", "/%2e"} {
		if _, err := Load(env(map[string]string{"TERN_BASE_PATH": in})); err == nil || !strings.Contains(err.Error(), "TERN_BASE_PATH") {
			t.Errorf("TERN_BASE_PATH=%q: err = %v, want one naming TERN_BASE_PATH", in, err)
		}
	}
}

func TestLoadPlatform(t *testing.T) {
	got, err := Load(env(map[string]string{"TERN_PLATFORM": "qts"}))
	if err != nil || got.Platform != PlatformQTS || got.QTSAuthURL != DefaultQTSAuthURL {
		t.Fatalf("qts: got %+v, %v", got, err)
	}
	got, err = Load(env(map[string]string{"TERN_PLATFORM": "qts", "TERN_QTS_AUTH_URL": "http://127.0.0.1:1/x"}))
	if err != nil || got.QTSAuthURL != "http://127.0.0.1:1/x" {
		t.Fatalf("qts with URL: got %+v, %v", got, err)
	}
	// The URL means nothing outside QTS.
	got, err = Load(env(map[string]string{"TERN_QTS_AUTH_URL": "http://127.0.0.1:1/x"}))
	if err != nil || got.Platform != PlatformDocker || got.QTSAuthURL != "" {
		t.Fatalf("docker with URL: got %+v, %v", got, err)
	}
	if _, err := Load(env(map[string]string{"TERN_PLATFORM": "synology"})); err == nil || !strings.Contains(err.Error(), "TERN_PLATFORM") {
		t.Fatalf("unknown platform: err = %v", err)
	}
}
