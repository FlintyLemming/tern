package restic

import (
	"encoding/json"

	"tern/internal/model"
)

// Summary is the closing line of `restic backup --json` or
// `restic restore --json`.
type Summary struct {
	SnapshotID                              string
	FilesNew, FilesChanged, FilesUnmodified int64
	DataAdded, BytesProcessed               int64
	FilesRestored, BytesRestored            int64 // restore only
}

// Parser reads restic's JSON output line by line.
type Parser struct {
	onProgress  func(model.Progress)
	onItemError func(item, msg string)
	summary     Summary
	hasSummary  bool
}

// NewParser returns a parser that reports progress and per-file errors
// through the given callbacks; either may be nil.
func NewParser(onProgress func(model.Progress), onItemError func(item, msg string)) *Parser {
	return &Parser{onProgress: onProgress, onItemError: onItemError}
}

// message has the fields of all message types tern reads; backup and
// restore name their counters differently.
type message struct {
	Type string `json:"message_type"`

	// status
	SecondsElapsed   int64 `json:"seconds_elapsed"`
	SecondsRemaining int64 `json:"seconds_remaining"`
	TotalFiles       int64 `json:"total_files"`
	TotalBytes       int64 `json:"total_bytes"`
	FilesDone        int64 `json:"files_done"`
	BytesDone        int64 `json:"bytes_done"`
	FilesRestored    int64 `json:"files_restored"`
	BytesRestored    int64 `json:"bytes_restored"`

	// summary
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	FilesUnmodified     int64  `json:"files_unmodified"`
	DataAdded           int64  `json:"data_added"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	SnapshotID          string `json:"snapshot_id"`

	// error
	Item  string `json:"item"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Line takes one line of output. Anything that is not a JSON message tern
// knows is ignored.
func (p *Parser) Line(b []byte) {
	var m message
	if json.Unmarshal(b, &m) != nil {
		return
	}
	switch m.Type {
	case "status":
		if p.onProgress == nil {
			return
		}
		pr := model.Progress{
			BytesDone: m.BytesDone + m.BytesRestored, BytesTotal: m.TotalBytes,
			FilesDone: m.FilesDone + m.FilesRestored, FilesTotal: m.TotalFiles,
			ETASec: m.SecondsRemaining,
		}
		if m.SecondsElapsed > 0 {
			pr.SpeedBps = float64(pr.BytesDone) / float64(m.SecondsElapsed)
		}
		p.onProgress(pr)
	case "summary":
		p.summary = Summary{
			SnapshotID: m.SnapshotID,
			FilesNew:   m.FilesNew, FilesChanged: m.FilesChanged, FilesUnmodified: m.FilesUnmodified,
			DataAdded: m.DataAdded, BytesProcessed: m.TotalBytesProcessed,
			FilesRestored: m.FilesRestored, BytesRestored: m.BytesRestored,
		}
		p.hasSummary = true
	case "error":
		if p.onItemError != nil {
			p.onItemError(m.Item, m.Error.Message)
		}
	}
}

// Summary returns the summary line, if one was seen.
func (p *Parser) Summary() (Summary, bool) { return p.summary, p.hasSummary }
