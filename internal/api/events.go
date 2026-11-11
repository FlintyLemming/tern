package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const ssePingEvery = 20 * time.Second

// handleEvents streams state changes as server-sent events: the event name
// is the event type, the data is JSON. A comment line every 20 seconds
// keeps proxies from closing an idle stream. A client that falls too far
// behind is disconnected and reloads on reconnect.
func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming is not supported", nil)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	ch := a.d.Events.Subscribe(ctx)
	defer func() {
		cancel()
		for range ch { // until the hub has let go of the subscription
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Sent after subscribing: whatever changes from here on is delivered.
	fmt.Fprint(w, ": ok\n\n")
	fl.Flush()

	ping := a.d.Clock.After(ssePingEvery)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(e.Data)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data); err != nil {
				return
			}
		case <-ping:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			ping = a.d.Clock.After(ssePingEvery)
		case <-ctx.Done():
			return
		case <-a.closing:
			return
		}
		fl.Flush()
	}
}
