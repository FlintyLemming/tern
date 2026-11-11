// Package events fans state changes out to whoever is watching, which is
// the server-sent-events endpoint. Publishers never wait for subscribers.
package events

import (
	"context"
	"sync"

	"tern/internal/model"
)

const (
	RunUpdated  = "run.updated"  // Data: model.Run
	RunProgress = "run.progress" // Data: Progress
	TaskUpdated = "task.updated" // Data: TaskRef

	subscriberBuffer = 64
)

type Event struct {
	Type string
	Data any
}

// Progress is the payload of run.progress.
type Progress struct {
	RunID    string         `json:"run_id"`
	Progress model.Progress `json:"progress"`
}

// TaskRef is the payload of task.updated.
type TaskRef struct {
	TaskID string `json:"task_id"`
}

type Hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	mu     sync.Mutex
	queue  []Event
	closed bool          // overflowed: the consumer is cut off
	wake   chan struct{} // capacity 1
}

func NewHub() *Hub { return &Hub{subs: map[*subscriber]struct{}{}} }

// Publish hands e to every subscriber and never blocks.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.push(e)
	}
}

// push queues e. A full queue first sheds progress events, which the next
// one supersedes anyway; if there is none to shed the subscriber is cut
// off, so it reconnects and reloads rather than miss a state change.
func (s *subscriber) push(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if len(s.queue) >= subscriberBuffer {
		shed := -1
		for i, q := range s.queue {
			if q.Type == RunProgress {
				shed = i
				break
			}
		}
		switch {
		case shed >= 0:
			s.queue = append(s.queue[:shed], s.queue[shed+1:]...)
		case e.Type == RunProgress:
			return
		default:
			s.closed, s.queue = true, nil
		}
	}
	if !s.closed {
		s.queue = append(s.queue, e)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Subscribe returns a channel of the events published from now on. It is
// closed when ctx ends, or when the subscriber falls too far behind.
func (h *Hub) Subscribe(ctx context.Context) <-chan Event {
	s := &subscriber{wake: make(chan struct{}, 1)}
	out := make(chan Event)
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()

	go func() {
		defer close(out)
		defer func() { // runs first: gone from the hub before the channel closes
			h.mu.Lock()
			delete(h.subs, s)
			h.mu.Unlock()
		}()
		for {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return
			}
			if len(s.queue) > 0 {
				e := s.queue[0]
				s.queue = s.queue[1:]
				s.mu.Unlock()
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
				continue
			}
			s.mu.Unlock()
			select {
			case <-s.wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Subscribers is the number of live subscriptions.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
