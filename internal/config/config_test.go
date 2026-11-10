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
	want := Config{Listen: ":8080", DataDir: "/data", SecretsDir: "/secrets", RuntimeDir: "/dev/shm/tern"}
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
	want := Config{Listen: "127.0.0.1:9000", DataDir: "/d", SecretsDir: "/s", RuntimeDir: "/r", TLSCert: "/c.pem", TLSKey: "/k.pem"}
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
