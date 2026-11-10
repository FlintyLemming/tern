package sshx

import (
	"context"
	"strings"
	"testing"
	"time"

	"tern/internal/model"
	"tern/internal/sshx/sshtest"
)

func genParsed(t *testing.T) Parsed {
	t.Helper()
	_, p, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func targetOf(srv *sshtest.Server) Target {
	return Target{Host: srv.Host(), Port: srv.Port(), User: "backup", HostKey: AuthorizedKey(srv.HostPublicKey())}
}

func wantKind(t *testing.T, err error, want model.ErrKind) *model.KindError {
	t.Helper()
	ke, ok := err.(*model.KindError)
	if !ok {
		t.Fatalf("err = %v (%T), want *model.KindError of kind %s", err, err, want)
	}
	if ke.Kind != want {
		t.Fatalf("kind = %s (%v), want %s", ke.Kind, err, want)
	}
	return ke
}

func TestCheckOK(t *testing.T) {
	p := genParsed(t)
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	if err := Check(context.Background(), targetOf(srv), p.Signer); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIPv6(t *testing.T) {
	needIPv6(t)
	p := genParsed(t)
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()), sshtest.WithListen("[::1]:0"))
	tg := targetOf(srv)
	if tg.Host != "::1" {
		t.Fatalf("host = %q", tg.Host)
	}
	if err := Check(context.Background(), tg, p.Signer); err != nil {
		t.Fatal(err)
	}
}

func TestCheckWrongHostKey(t *testing.T) {
	p := genParsed(t)
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	tg := targetOf(srv)
	tg.HostKey = genParsed(t).Authorized
	wantKind(t, Check(context.Background(), tg, p.Signer), model.KindHostKey)
}

func TestCheckUnauthorized(t *testing.T) {
	srv := sshtest.Start(t)
	wantKind(t, Check(context.Background(), targetOf(srv), genParsed(t).Signer), model.KindAuth)
}

func TestCheckNoSFTP(t *testing.T) {
	p := genParsed(t)
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()), sshtest.WithoutSFTP())
	ke := wantKind(t, Check(context.Background(), targetOf(srv), p.Signer), model.KindConfig)
	if !strings.Contains(ke.Msg, "sftp subsystem") {
		t.Errorf("Msg = %q", ke.Msg)
	}
}

func TestCheckRefused(t *testing.T) {
	p := genParsed(t)
	tg := Target{Host: "127.0.0.1", Port: freePort(t), User: "backup", HostKey: p.Authorized}
	wantKind(t, Check(context.Background(), tg, p.Signer), model.KindNetwork)
}

func TestCheckHang(t *testing.T) {
	p := genParsed(t)
	srv := sshtest.Start(t, sshtest.WithHang())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	wantKind(t, Check(ctx, targetOf(srv), p.Signer), model.KindNetwork)
	if d := time.Since(start); d >= 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestCheckHostKeyBeforeAuth(t *testing.T) {
	srv := sshtest.Start(t) // authorizes nobody
	tg := targetOf(srv)
	tg.HostKey = genParsed(t).Authorized
	wantKind(t, Check(context.Background(), tg, genParsed(t).Signer), model.KindHostKey)
}

func TestCheckMalformedPinnedKey(t *testing.T) {
	p := genParsed(t)
	srv := sshtest.Start(t, sshtest.WithAuthorized(p.Signer.PublicKey()))
	tg := targetOf(srv)
	tg.HostKey = "garbage"
	wantKind(t, Check(context.Background(), tg, p.Signer), model.KindConfig)
}
