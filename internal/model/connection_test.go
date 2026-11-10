package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestKindRetryable(t *testing.T) {
	for k, want := range map[ErrKind]bool{
		KindNetwork: true, KindUnknown: true,
		KindAuth: false, KindHostKey: false, KindConfig: false, KindData: false, KindGuard: false,
	} {
		if k.Retryable() != want {
			t.Errorf("%s.Retryable() = %v", k, !want)
		}
	}
}

func TestKindOf(t *testing.T) {
	if k := KindOf(nil); k != "" {
		t.Errorf("KindOf(nil) = %q", k)
	}
	if k := KindOf(errors.New("x")); k != KindUnknown {
		t.Errorf("KindOf(plain) = %q", k)
	}
	inner := errors.New("dial refused")
	ke := &KindError{Kind: KindNetwork, Msg: "cannot connect", Err: inner}
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", ke))
	if k := KindOf(wrapped); k != KindNetwork {
		t.Errorf("KindOf(wrapped twice) = %q", k)
	}
	if !errors.Is(ke, inner) {
		t.Error("KindError does not unwrap")
	}
	if ke.Error() != "cannot connect: dial refused" {
		t.Errorf("Error() = %q", ke.Error())
	}
	if (&KindError{Kind: KindAuth, Msg: "denied"}).Error() != "denied" {
		t.Error("Error() without cause")
	}
}

func sshConn(host, user string) Connection {
	c := Connection{Type: "ssh", Name: "nas2", Host: host, Username: user, SSHKeyID: "k1"}
	c.ApplyDefaults()
	return c
}

func TestValidateSSHOK(t *testing.T) {
	for _, host := range []string{"nas2.example", "10.0.0.2", "fd7a:115c::1", "NAS-2", "a", "100.64.0.7", "xn--bcher-kva.example"} {
		c := sshConn(host, "backup")
		if f := c.Validate(); len(f) != 0 {
			t.Errorf("host %q: %v", host, f)
		}
		if c.Port != 22 {
			t.Errorf("port = %d", c.Port)
		}
	}
	for _, user := range []string{"root", "back.up_1", "_svc", "a-b"} {
		if f := sshConn("h", user).Validate(); len(f) != 0 {
			t.Errorf("user %q: %v", user, f)
		}
	}
}

func TestValidateRejectsInjection(t *testing.T) {
	for _, host := range []string{
		"a\nProxyCommand x", "-oProxyCommand=x", "a b", "[::1]", "", "a\x00b", "a\tb", "host.", ".host", "a..b",
		"fe80::1%eth0", "-leading", "trailing-", "under_score", "a/b", "user@host", strings.Repeat("a", 64),
		strings.Repeat("a.", 127) + "a", "%h", "::1 ", "h\r",
	} {
		f := sshConn(host, "backup").Validate()
		if f["host"] == "" || len(f) != 1 {
			t.Errorf("host %q: fields = %v", host, f)
		}
	}
	for _, user := range []string{"root\n", "-x", "a b", "", "a\x00", strings.Repeat("u", 65), "u@h", "%u", "a;b", "é"} {
		f := sshConn("nas2.example", user).Validate()
		if f["username"] == "" || len(f) != 1 {
			t.Errorf("username %q: fields = %v", user, f)
		}
	}
}

func TestValidatePort(t *testing.T) {
	c := Connection{Type: "ssh", Name: "n", Host: "h", Username: "u", SSHKeyID: "k"}
	c.ApplyDefaults()
	if f := c.Validate(); len(f) != 0 || c.Port != 22 {
		t.Errorf("port 0 after defaults: %v, port %d", f, c.Port)
	}
	for _, p := range []int{-1, 65536} {
		c.Port = p
		if f := c.Validate(); f["port"] == "" {
			t.Errorf("port %d accepted", p)
		}
	}
	for _, p := range []int{1, 2222, 65535} {
		c.Port = p
		if f := c.Validate(); len(f) != 0 {
			t.Errorf("port %d: %v", p, f)
		}
	}
}

func TestValidateSSHRequiresKey(t *testing.T) {
	c := sshConn("h", "u")
	c.SSHKeyID = ""
	if f := c.Validate(); f["ssh_key_id"] != "required" {
		t.Errorf("fields = %v", f)
	}
}

func TestValidateWebDAV(t *testing.T) {
	dav := func(u string) Connection {
		return Connection{Type: "webdav", Name: "dav", URL: u, Username: "me@example.com"}
	}
	for _, u := range []string{"https://h/dav", "http://10.0.0.2:8080/remote.php/dav/files/me/", "https://[fd7a::1]/x", "https://h"} {
		if f := dav(u).Validate(); len(f) != 0 {
			t.Errorf("url %q: %v", u, f)
		}
	}
	for u, code := range map[string]string{
		"https://u:p@h/":  "embedded_credentials",
		"https://u@h/":    "embedded_credentials",
		"ftp://h/":        "scheme",
		"h/dav":           "scheme",
		"":                "required",
		"https:///path":   "invalid",
		"https://h/a\nb":  "invalid",
		"https://h/a b":   "invalid",
		"https://h/\x7f":  "invalid",
		"https://h:x/":    "invalid",
		"https://h/#frag": "invalid",
	} {
		if f := dav(u).Validate(); f["url"] != code || len(f) != 1 {
			t.Errorf("url %q: fields = %v, want url=%s", u, f, code)
		}
	}
	c := dav("https://h/")
	c.Username = "a:b"
	if f := c.Validate(); f["username"] == "" {
		t.Error("username with a colon accepted")
	}
	c.Username = "a\nb"
	if f := c.Validate(); f["username"] == "" {
		t.Error("username with a newline accepted")
	}
}

func TestValidateCommon(t *testing.T) {
	c := sshConn("h", "u")
	c.Type = "ftp"
	if f := c.Validate(); f["type"] == "" {
		t.Errorf("type: %v", f)
	}
	c = sshConn("h", "u")
	c.Name = ""
	if f := c.Validate(); f["name"] != "required" {
		t.Errorf("name: %v", f)
	}
	c.Name = "a\nb"
	if f := c.Validate(); f["name"] != "invalid" {
		t.Errorf("name: %v", f)
	}
	// Fields of the other type are refused rather than silently stored.
	c = sshConn("h", "u")
	c.URL = "https://h/"
	if f := c.Validate(); f["url"] == "" {
		t.Errorf("ssh with url: %v", f)
	}
	d := Connection{Type: "webdav", Name: "d", URL: "https://h/", Host: "h", SSHKeyID: "k", HostKey: "x", Port: 22}
	if f := d.Validate(); f["host"] == "" || f["ssh_key_id"] == "" || f["host_key"] == "" || f["port"] == "" {
		t.Errorf("webdav with ssh fields: %v", f)
	}
}
