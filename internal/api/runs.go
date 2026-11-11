package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"tern/internal/runlog"
	"tern/internal/scheduler"
	"tern/internal/store"
)

const (
	runsDefaultLimit = 50
	runsMaxLimit     = 200
	logChunk         = 256 << 10
)

func (a *API) registerRuns() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("GET /api/v1/runs", a.handleListRuns, g...)
	a.Handle("GET /api/v1/runs/{id}", a.handleGetRun, g...)
	a.Handle("GET /api/v1/runs/{id}/log", a.handleRunLog, g...)
	a.Handle("POST /api/v1/runs/{id}/cancel", a.handleCancelRun, g...)
	a.Handle("GET /api/v1/overview", a.handleOverview, g...)
	a.Handle("GET /api/v1/events", a.handleEvents, g...)
}

func badQuery(w http.ResponseWriter, param string) {
	writeError(w, http.StatusBadRequest, "bad_request", "invalid query parameter "+param, nil)
}

func (a *API) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.RunFilter{TaskID: q.Get("task_id"), Limit: runsDefaultLimit}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			badQuery(w, "limit")
			return
		}
		f.Limit = min(n, runsMaxLimit)
	}
	if raw := q.Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			badQuery(w, "before")
			return
		}
		f.Before = t
	}
	runs, err := a.d.Store.ListRuns(r.Context(), f)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (a *API) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := a.d.Store.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleRunLog returns one chunk of a run's log. eof is true once the run
// is final and the chunk reaches the end of the file; until then the client
// keeps polling from next_offset.
func (a *API) handleRunLog(w http.ResponseWriter, r *http.Request) {
	var offset int64
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			badQuery(w, "offset")
			return
		}
		offset = n
	}
	// The status is read first: a run that is final by now has its whole
	// log on disk by the time the file is read.
	run, err := a.d.Store.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "run")
		return
	}
	data, next, err := runlog.Read(a.d.Cfg.DataDir, run.ID, offset, logChunk)
	if err != nil {
		internalError(w, r, err)
		return
	}
	atEnd := len(data) < logChunk
	if !atEnd {
		// Do not cut a multi-byte character in two: JSON would turn both
		// halves into replacement characters.
		for i := 1; i <= utf8.UTFMax && i <= len(data); i++ {
			if utf8.RuneStart(data[len(data)-i]) {
				if !utf8.FullRune(data[len(data)-i:]) {
					data = data[:len(data)-i]
					next -= int64(i)
				}
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":        string(data),
		"next_offset": next,
		"eof":         atEnd && run.Status.Terminal(),
	})
}

func (a *API) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.d.Scheduler.Cancel(id)
	if errors.Is(err, scheduler.ErrNotActive) {
		if _, gerr := a.d.Store.GetRun(r.Context(), id); gerr != nil {
			storeError(w, r, gerr, "run")
			return
		}
		writeError(w, http.StatusConflict, "conflict", "the run is not queued or running", nil)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
