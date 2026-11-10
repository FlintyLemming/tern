// Package redact keeps credentials out of logs. Child process output goes
// through a Redactor before it is stored or shown.
package redact

import (
	"bytes"
	"io"
	"regexp"
	"sort"
	"sync"
)

const (
	minSecretLen = 4        // shorter strings would mask ordinary text
	maxLine      = 64 << 10 // a line longer than this is flushed in pieces
	maxPEMLines  = 400      // a key block with no END stops hiding the log after this
	pemNotice    = "*** private key ***\n"
	mask         = "***"
)

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
	// "://user:password@": the user part may be empty, the password ends at '@'.
	urlUserinfo = regexp.MustCompile(`(://[^/\s:@]*):[^/\s@]*@`)
)

// Redactor holds the secrets registered for one run. It is safe for
// concurrent use, and so are the Writers it hands out.
type Redactor struct {
	mu      sync.RWMutex
	secrets [][]byte   // longest first
	out     sync.Mutex // serializes every Writer's output, one line at a time
}

func New() *Redactor { return &Redactor{} }

// Add registers a secret to be masked from now on. Secrets shorter than four
// bytes are ignored.
func (r *Redactor) Add(secret []byte) {
	if len(secret) < minSecretLen {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if bytes.Equal(s, secret) {
			return
		}
	}
	r.secrets = append(r.secrets, bytes.Clone(secret))
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

func (r *Redactor) snapshot() [][]byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.secrets[:len(r.secrets):len(r.secrets)]
}

// Writer returns a line-buffered writer that masks what passes through on
// its way to w. Close flushes a trailing line without newline; it does not
// close w.
func (r *Redactor) Writer(w io.Writer) io.WriteCloser {
	return &writer{r: r, dst: w}
}

type writer struct {
	r   *Redactor
	dst io.Writer

	mu       sync.Mutex
	buf      []byte
	inPEM    bool
	pemLines int
	closed   bool
}

func (w *writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		err := w.emit(w.buf[:i+1])
		w.buf = w.buf[i+1:]
		if err != nil {
			return len(p), err
		}
	}
	if len(w.buf) > maxLine {
		cut := w.safeCut()
		err := w.emit(w.buf[:cut])
		w.buf = append(w.buf[:0], w.buf[cut:]...)
		if err != nil {
			return len(p), err
		}
	}
	if len(w.buf) == 0 {
		w.buf = nil // do not pin a large backing array
	}
	return len(p), nil
}

// safeCut picks how much of an over-long, unterminated line to flush now so
// that no secret is split: neither one fully buffered that straddles the cut
// nor one whose beginning sits at the very end of the buffer.
func (w *writer) safeCut() int {
	secrets := w.r.snapshot()
	keep := 0
	if len(secrets) > 0 {
		keep = len(secrets[0]) - 1
	}
	cut := max(len(w.buf)-keep, 0)
	for moved := true; moved; {
		moved = false
		for _, s := range secrets {
			from := max(cut-len(s)+1, 0)
			if i := bytes.Index(w.buf[from:min(cut+len(s)-1, len(w.buf))], s); i >= 0 && from+i < cut {
				cut, moved = from+i, true
			}
		}
	}
	return cut
}

func (w *writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var err error
	if len(w.buf) > 0 {
		err = w.emit(w.buf)
	}
	w.buf = nil
	return err
}

// emit masks one line (or line fragment) and writes it out.
func (w *writer) emit(line []byte) error {
	if w.inPEM {
		if pemEnd.Match(line) {
			w.inPEM = false
			return nil
		}
		if w.pemLines++; w.pemLines <= maxPEMLines {
			return nil
		}
		w.inPEM = false // no END in sight: stop swallowing the log
	}
	if loc := pemBegin.FindIndex(line); loc != nil {
		if !pemEnd.Match(line[loc[1]:]) {
			w.inPEM, w.pemLines = true, 0
		}
		return w.write([]byte(pemNotice))
	}
	out := line
	for _, s := range w.r.snapshot() {
		out = bytes.ReplaceAll(out, s, []byte(mask))
	}
	out = urlUserinfo.ReplaceAll(out, []byte("$1:"+mask+"@"))
	return w.write(out)
}

func (w *writer) write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	w.r.out.Lock()
	defer w.r.out.Unlock()
	_, err := w.dst.Write(p)
	return err
}
