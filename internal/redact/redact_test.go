package redact

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func run(t *testing.T, r *Redactor, writes ...string) string {
	t.Helper()
	var out bytes.Buffer
	w := r.Writer(&out)
	for _, s := range writes {
		if n, err := io.WriteString(w, s); err != nil || n != len(s) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestMasksSecret(t *testing.T) {
	r := New()
	r.Add([]byte("s3cr3t-pass"))
	if got := run(t, r, "pw=s3cr3t-pass x\n"); got != "pw=*** x\n" {
		t.Errorf("got %q", got)
	}
}

func TestMasksAcrossWrites(t *testing.T) {
	r := New()
	r.Add([]byte("s3cr3t-pass"))
	if got := run(t, r, "pw=s3cr3", "t-pass\n"); got != "pw=***\n" {
		t.Errorf("got %q", got)
	}
}

func TestMasksAllOccurrences(t *testing.T) {
	r := New()
	r.Add([]byte("s3cr3t-pass"))
	if got := run(t, r, "a s3cr3t-pass b s3cr3t-pass\nnext s3cr3t-pass\n"); got != "a *** b ***\nnext ***\n" {
		t.Errorf("got %q", got)
	}
}

func TestMasksLongestFirst(t *testing.T) {
	r := New()
	r.Add([]byte("hunter"))
	r.Add([]byte("hunter2-extended"))
	if got := run(t, r, "x hunter2-extended y hunter z\n"); got != "x *** y *** z\n" {
		t.Errorf("got %q", got)
	}
}

func TestSecretAddedLater(t *testing.T) {
	r := New()
	var out bytes.Buffer
	w := r.Writer(&out)
	io.WriteString(w, "before\n")
	r.Add([]byte("late-secret"))
	io.WriteString(w, "now late-secret\n")
	w.Close()
	if out.String() != "before\nnow ***\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestMasksPEMBlock(t *testing.T) {
	in := "before\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
		"QyNTUxOQAAACDjKqp5Fc65tEnRulDqKii7Gm4B+QvaJFotHYdpfRiiZQAAAJDQ0X1h0NF9\n" +
		"-----END OPENSSH PRIVATE KEY-----\n" +
		"after\n"
	if strings.Count(in, "\n") != 6 {
		t.Fatal("fixture")
	}
	got := run(t, New(), in)
	if got != "before\n*** private key ***\nafter\n" {
		t.Errorf("got %q", got)
	}
	for _, kind := range []string{"RSA ", "EC ", "", "ENCRYPTED "} {
		in := "-----BEGIN " + kind + "PRIVATE KEY-----\nAAAA\n-----END " + kind + "PRIVATE KEY-----\nok\n"
		if got := run(t, New(), in); got != "*** private key ***\nok\n" {
			t.Errorf("%sPRIVATE KEY: got %q", kind, got)
		}
	}
	// Public material is not a secret.
	if got := run(t, New(), "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"); !strings.Contains(got, "AAAA") {
		t.Errorf("certificate was masked: %q", got)
	}
}

func TestPEMBlockCutShort(t *testing.T) {
	// The stream ends inside a block: the body still never appears.
	got := run(t, New(), "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAsecretbody\nBBBB")
	if got != "*** private key ***\n" {
		t.Errorf("got %q", got)
	}
}

func TestPEMBlockWithoutEndDoesNotSwallowLogForever(t *testing.T) {
	var in strings.Builder
	in.WriteString("-----BEGIN OPENSSH PRIVATE KEY-----\n")
	for i := 0; i < maxPEMLines+50; i++ {
		fmt.Fprintf(&in, "line %d\n", i)
	}
	got := run(t, New(), in.String())
	if !strings.Contains(got, fmt.Sprintf("line %d\n", maxPEMLines+49)) || strings.Contains(got, "line 0\n") {
		t.Errorf("output has %d lines", strings.Count(got, "\n"))
	}
}

func TestMasksURLUserinfo(t *testing.T) {
	if got := run(t, New(), "GET https://u:p4ss@h/x\n"); got != "GET https://u:***@h/x\n" {
		t.Errorf("got %q", got)
	}
	if got := run(t, New(), "a sftp://me:x@h:22/p b http://o:y@[::1]/ c\n"); got != "a sftp://me:***@h:22/p b http://o:***@[::1]/ c\n" {
		t.Errorf("got %q", got)
	}
	for _, plain := range []string{"https://h/x", "https://h:8080/a@b", "time 12:30 me@host", "https://u@h/x"} {
		if got := run(t, New(), plain+"\n"); got != plain+"\n" {
			t.Errorf("%q became %q", plain, got)
		}
	}
}

func TestIgnoresShortSecrets(t *testing.T) {
	r := New()
	r.Add([]byte("abc"))
	r.Add(nil)
	if got := run(t, r, "abcdef\n"); got != "abcdef\n" {
		t.Errorf("got %q", got)
	}
}

func TestFlushOnClose(t *testing.T) {
	r := New()
	r.Add([]byte("s3cr3t-pass"))
	if got := run(t, r, "tail"); got != "tail" {
		t.Errorf("got %q", got)
	}
	if got := run(t, r, "line\ntail s3cr3t-pass"); got != "line\ntail ***" {
		t.Errorf("got %q", got)
	}
}

func TestVeryLongLineStaysMasked(t *testing.T) {
	const secret = "s3cr3t-pass"
	cases := map[string][]string{
		// Fully buffered, lying across the point where the line is cut.
		"straddles the cut": {strings.Repeat("x", maxLine-5) + secret + "yyy", strings.Repeat("y", 100) + "\n"},
		// Only its beginning has arrived when the line is cut.
		"split across writes": {strings.Repeat("x", maxLine) + secret[:5], secret[5:] + "\n"},
		"no newline at all":   {strings.Repeat("x", maxLine) + secret[:5], secret[5:] + strings.Repeat("z", maxLine)},
	}
	for name, writes := range cases {
		r := New()
		r.Add([]byte(secret))
		r.Add([]byte("zzzz-short"))
		got := run(t, r, writes...)
		if strings.Contains(got, "s3cr3") || strings.Contains(got, "t-pass") {
			t.Errorf("%s: secret leaked across the long-line flush", name)
		}
		if want := len(strings.Join(writes, "")) - len(secret) + 3; len(got) != want {
			t.Errorf("%s: len = %d, want %d", name, len(got), want)
		}
	}
}

// lockedBuffer fails the test if two writes overlap: the Redactor must
// serialize output even to a destination that is not safe for concurrent use.
type lockedBuffer struct {
	t    *testing.T
	busy sync.Mutex
	buf  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	if !b.busy.TryLock() {
		b.t.Error("concurrent Write on the destination")
		b.busy.Lock()
	}
	defer b.busy.Unlock()
	return b.buf.Write(p)
}

func TestConcurrentWriters(t *testing.T) {
	r := New()
	r.Add([]byte("s3cr3t-pass"))
	out := &lockedBuffer{t: t}
	shared := r.Writer(out) // two goroutines on one Writer
	other := r.Writer(out)  // and a second Writer on the same destination
	var wg sync.WaitGroup
	for g, w := range []io.Writer{shared, shared, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				// Two writes per line, to invite interleaving.
				fmt.Fprintf(w, "g%d line %04d s3cr3t-pass end\n", g, i)
			}
		}()
	}
	wg.Wait()
	shared.Close()
	other.Close()
	lines := strings.Split(strings.TrimSuffix(out.buf.String(), "\n"), "\n")
	if len(lines) != 3000 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if len(l) != len("g0 line 0000 *** end") || !strings.HasSuffix(l, " *** end") || l[0] != 'g' {
			t.Fatalf("interleaved or unmasked line %q", l)
		}
	}
}
