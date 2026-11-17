package rsync

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"tern/internal/engine/synccore"
	"tern/internal/model"
)

// A --info=progress2 line. rsync before 3.4 prints plain byte counts and
// 1024-based rates ("1,238,099  42%   14.98MB/s"); later versions append
// "B" to the count and print the rate in units of 1000 ("2,097,152B  31%
// 2.05MB/s"). The trailing "(xfr#N, to-chk=A/B)" comes with each file
// that completes.
var progressRE = regexp.MustCompile(`^\s*([0-9][0-9,]*)(B?)\s+(\d{1,3})%\s+([0-9.]+)([kKMGTP]?)B/s\s+(\S+)(?:\s+\(xfr#\d+, (?:to|ir)-chk=(\d+)/(\d+)\))?\s*$`)

// The itemized change string of --out-format=%i is 11 characters, then
// the format's space, then the name.
const nameAt = 12

// Parser reads rsync's standard output with --info=progress2, --stats and
// --out-format="%i %n%L".
type Parser struct {
	onProgress func(model.Progress)
	// quiet leaves itemized entries out of the log: during a dry run
	// synccore logs the (capped) plan instead.
	quiet bool
	// planning reports each file found to transfer as progress: a dry
	// run prints no progress lines of its own.
	planning bool

	plan       synccore.Plan
	dirDeletes int
	planBytes  int64 // "Total transferred file size"
	literal    int64 // "Literal data"
	files      int64 // "Number of regular files transferred"
	filesDone  int64
	filesTotal int64
}

// NewParser returns a parser that reports progress to onProgress, which
// may be nil.
func NewParser(onProgress func(model.Progress)) *Parser { return &Parser{onProgress: onProgress} }

// Line takes one line and returns the text for the run log: "" for
// progress lines.
func (p *Parser) Line(b []byte) (logLine string) {
	line := string(b)
	if m := progressRE.FindStringSubmatch(line); m != nil {
		p.progress(m)
		return ""
	}
	if p.entry(line) && p.quiet {
		return ""
	}
	p.stat(line)
	return line
}

func (p *Parser) progress(m []string) {
	done, err := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
	if err != nil {
		return
	}
	pr := model.Progress{BytesDone: done}
	if pct, _ := strconv.Atoi(m[3]); pct > 0 {
		pr.BytesTotal = int64(math.Round(float64(done) * 100 / float64(pct)))
	}
	base := 1024.0
	if m[2] == "B" {
		base = 1000
	}
	if rate, err := strconv.ParseFloat(m[4], 64); err == nil {
		pr.SpeedBps = rate * math.Pow(base, float64(strings.Index("_KMGTP", strings.ToUpper(m[5]))))
	}
	pr.ETASec = clockSeconds(m[6])
	if m[7] != "" {
		left, _ := strconv.ParseInt(m[7], 10, 64)
		total, _ := strconv.ParseInt(m[8], 10, 64)
		p.filesDone, p.filesTotal = total-left, total
	}
	pr.FilesDone, pr.FilesTotal = p.filesDone, p.filesTotal
	if p.onProgress != nil {
		p.onProgress(pr)
	}
}

// clockSeconds reads "h:mm:ss"; anything else ("??:??:??") is 0.
func clockSeconds(s string) int64 {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	var total int64
	for _, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n < 0 {
			return 0
		}
		total = total*60 + n
	}
	return total
}

// entry counts an itemized line and reports whether it was one.
func (p *Parser) entry(line string) bool {
	if len(line) <= nameAt {
		return false
	}
	name := line[nameAt:]
	switch {
	case strings.HasPrefix(line, "*deleting "):
		if strings.HasSuffix(name, "/") {
			p.dirDeletes++
		} else {
			p.plan.AddDelete(decodeName(name))
		}
		return true
	case strings.HasPrefix(line, "<f") || strings.HasPrefix(line, ">f"):
		p.plan.AddTransfer(decodeName(name), 0)
		if p.planning && p.onProgress != nil {
			p.onProgress(model.Progress{FilesDone: int64(p.plan.TransferCount)})
		}
		return true
	case line[nameAt-1] == ' ' && strings.IndexByte("<>ch.", line[0]) >= 0:
		return true // directories, links, attribute-only changes
	}
	return false
}

var statNames = map[string]func(p *Parser) *int64{
	"Number of regular files transferred: ": func(p *Parser) *int64 { return &p.files },
	"Literal data: ":                        func(p *Parser) *int64 { return &p.literal },
	"Total transferred file size: ":         func(p *Parser) *int64 { return &p.planBytes },
}

func (p *Parser) stat(line string) {
	for prefix, field := range statNames {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		num, _, _ := strings.Cut(rest, " ")
		if n, err := strconv.ParseInt(strings.ReplaceAll(num, ",", ""), 10, 64); err == nil {
			*field(p) = n
		}
		return
	}
}

// Plan is what the itemized lines so far add up to. Its byte count is the
// "Total transferred file size" of --stats.
func (p *Parser) Plan() synccore.Plan {
	plan := p.plan
	plan.Bytes = p.planBytes
	return plan
}

// DirDeletes is the number of directories the itemized lines delete. The
// plan does not count them, rsync's --max-delete does.
func (p *Parser) DirDeletes() int { return p.dirDeletes }

// Totals are the bytes sent ("Literal data") and the regular files
// transferred, from --stats.
func (p *Parser) Totals() (bytes, files int64) { return p.literal, p.files }

// decodeName undoes rsync's output escaping: "\#ooo" stands for the byte
// with that octal value.
func decodeName(s string) string {
	if !strings.Contains(s, `\#`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 < len(s) && s[i+1] == '#' && octal(s[i+2]) && octal(s[i+3]) && octal(s[i+4]) {
			b.WriteByte((s[i+2]-'0')<<6 | (s[i+3]-'0')<<3 | (s[i+4] - '0'))
			i += 4
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func octal(c byte) bool { return c >= '0' && c <= '7' }
