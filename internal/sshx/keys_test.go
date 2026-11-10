package sshx

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

const testPass = "pp-123456"

// marshal returns key as an OpenSSH private key PEM, encrypted when pass is set.
func marshal(t *testing.T, key any, pass string) []byte {
	t.Helper()
	var (
		blk *pem.Block
		err error
	)
	if pass == "" {
		blk, err = ssh.MarshalPrivateKey(key, "")
	} else {
		blk, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(pass))
	}
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(blk)
}

func authorizedOf(t *testing.T, pub any) string {
	t.Helper()
	p, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(p)))
}

func TestGenerateParses(t *testing.T) {
	privPEM, p, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	q, err := ParsePrivate(privPEM, nil)
	if err != nil {
		t.Fatalf("ParsePrivate(Generate()): %v", err)
	}
	if !strings.HasPrefix(p.Authorized, "ssh-ed25519 ") || strings.Count(p.Authorized, " ") != 1 {
		t.Errorf("Authorized = %q", p.Authorized)
	}
	if !strings.HasPrefix(p.Fingerprint, "SHA256:") {
		t.Errorf("Fingerprint = %q", p.Fingerprint)
	}
	if q.Authorized != p.Authorized || q.Fingerprint != p.Fingerprint {
		t.Errorf("reparsed key differs: %q vs %q", q.Authorized, p.Authorized)
	}
	if p.Signer == nil || p.Raw == nil || p.Encrypted {
		t.Errorf("Parsed = %+v", p)
	}
	if !bytes.Contains(privPEM, []byte("OPENSSH PRIVATE KEY")) {
		t.Errorf("not OpenSSH format: %s", privPEM[:40])
	}
}

func TestParseKinds(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		key, pub any
	}{
		{"ed25519", ed, ed.Public()},
		{"ecdsa-p256", ec, &ec.PublicKey},
		{"rsa-3072", rs, &rs.PublicKey},
	}
	for _, c := range cases {
		for _, pass := range []string{"", testPass} {
			t.Run(c.name+"/pass="+pass, func(t *testing.T) {
				p, err := ParsePrivate(marshal(t, c.key, pass), []byte(pass))
				if err != nil {
					t.Fatal(err)
				}
				if want := authorizedOf(t, c.pub); p.Authorized != want {
					t.Errorf("Authorized = %q, want %q", p.Authorized, want)
				}
				if p.Encrypted != (pass != "") {
					t.Errorf("Encrypted = %v", p.Encrypted)
				}
				if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(p.Signer.PublicKey()))); got != p.Authorized {
					t.Errorf("Signer key %q != Authorized", got)
				}
			})
		}
	}
}

func TestParseNeedsPassphrase(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := ParsePrivate(marshal(t, ed, testPass), nil); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("err = %v, want ErrPassphraseRequired", err)
	}
}

func TestParseBadPassphrase(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := ParsePrivate(marshal(t, ed, testPass), []byte("wrong-one")); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("err = %v, want ErrBadPassphrase", err)
	}
}

func TestParseGarbage(t *testing.T) {
	for _, in := range []string{"not a key", "", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"} {
		if _, err := ParsePrivate([]byte(in), nil); !errors.Is(err, ErrUnsupportedKey) {
			t.Errorf("ParsePrivate(%q) err = %v, want ErrUnsupportedKey", in, err)
		}
	}
}

func TestParseTolerantOfWhitespace(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	clean := string(marshal(t, ed, ""))
	want := authorizedOf(t, ed.Public())
	variants := map[string]string{
		"crlf":             strings.ReplaceAll(clean, "\n", "\r\n"),
		"blank lines":      "\n\n  \n" + clean + "\n\n",
		"no final newline": strings.TrimRight(clean, "\n"),
		"all three":        "\r\n\r\n" + strings.TrimRight(strings.ReplaceAll(clean, "\n", "\r\n"), "\r\n"),
	}
	for name, in := range variants {
		p, err := ParsePrivate([]byte(in), nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.Authorized != want {
			t.Errorf("%s: wrong key", name)
		}
		if got := string(Normalize([]byte(in))); got != clean {
			t.Errorf("%s: Normalize = %q, want the clean PEM", name, got)
		}
	}
}
