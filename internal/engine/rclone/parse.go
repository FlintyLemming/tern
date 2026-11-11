package rclone

import (
	"encoding/json"
	"strings"

	"tern/internal/engine/synccore"
	"tern/internal/model"
)

// entry is one line of rclone's --use-json-log output (on stderr).
type entry struct {
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Object  string `json:"object"`
	Skipped string `json:"skipped"` // set by --dry-run: what would have happened
	Size    int64  `json:"size"`
	Stats   *stats `json:"stats"`
}

type stats struct {
	Bytes          int64    `json:"bytes"`
	TotalBytes     int64    `json:"totalBytes"`
	Speed          float64  `json:"speed"`
	ETA            *float64 `json:"eta"` // null while unknown
	Transfers      int64    `json:"transfers"`
	TotalTransfers int64    `json:"totalTransfers"`
}

// Parser reads rclone's JSON log. Dry-run lines become a plan, stats
// lines progress; everything else becomes readable log text.
type Parser struct {
	onProgress   func(model.Progress)
	plan         synccore.Plan
	bytes, files int64
}

// NewParser returns a parser that reports progress to onProgress, which
// may be nil.
func NewParser(onProgress func(model.Progress)) *Parser { return &Parser{onProgress: onProgress} }

// Line takes one line and returns the text for the run log: "" for stats
// and dry-run lines (the plan is logged by synccore, capped), the line
// itself when it is not rclone JSON.
func (p *Parser) Line(b []byte) (logLine string) {
	var e entry
	if err := json.Unmarshal(b, &e); err != nil || (e.Msg == "" && e.Stats == nil) {
		return string(b)
	}
	if s := e.Stats; s != nil {
		p.bytes, p.files = s.Bytes, s.Transfers
		if p.onProgress != nil {
			pr := model.Progress{BytesDone: s.Bytes, BytesTotal: s.TotalBytes, FilesDone: s.Transfers,
				FilesTotal: s.TotalTransfers, SpeedBps: s.Speed}
			if s.ETA != nil {
				pr.ETASec = int64(*s.ETA)
			}
			p.onProgress(pr)
		}
		return ""
	}
	if e.Skipped != "" {
		switch {
		case e.Skipped == "delete":
			p.plan.AddDelete(decodeName(e.Object))
		case e.Skipped == "copy",
			// Only the destination's mtime would change: no data moves.
			strings.HasPrefix(e.Skipped, "update") && e.Skipped != "update modification time":
			p.plan.AddTransfer(decodeName(e.Object), e.Size)
		}
		return ""
	}
	text := strings.ToUpper(e.Level) + " "
	if e.Object != "" {
		text += e.Object + ": "
	}
	return text + e.Msg
}

// Plan is what the dry-run lines so far add up to.
func (p *Parser) Plan() synccore.Plan { return p.plan }

// Totals are the bytes and files transferred, from the latest stats.
func (p *Parser) Totals() (bytes, files int64) { return p.bytes, p.files }

// rclone shows names in its log with control characters replaced by their
// Unicode control pictures (␊ for a newline, ␡ for DEL); a name that
// really contains such a picture, or the quote, gets the quote ‛ before it.
const quoteRune = '‛'

func controlFor(r rune) (rune, bool) {
	switch {
	case r >= 0x2401 && r <= 0x241F:
		return r - 0x2400, true
	case r == 0x2421:
		return 0x7F, true
	}
	return 0, false
}

// decodeName turns a name as rclone logs it back into the file's name.
func decodeName(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { _, ok := controlFor(r); return ok || r == quoteRune }) < 0 {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == quoteRune && i+1 < len(rs) {
			if _, ok := controlFor(rs[i+1]); ok || rs[i+1] == quoteRune {
				b.WriteRune(rs[i+1])
				i++
				continue
			}
		}
		if c, ok := controlFor(r); ok {
			r = c
		}
		b.WriteRune(r)
	}
	return b.String()
}
