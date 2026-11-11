package events

import (
	"context"
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting on the subscription")
	}
	panic("unreachable")
}

func TestPublishReachesSubscribers(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := h.Subscribe(ctx), h.Subscribe(ctx)
	h.Publish(Event{Type: RunUpdated, Data: 1})
	h.Publish(Event{Type: TaskUpdated, Data: 2})
	for _, ch := range []<-chan Event{a, b} {
		e1, _ := recv(t, ch)
		e2, _ := recv(t, ch)
		if e1.Type != RunUpdated || e1.Data != 1 || e2.Type != TaskUpdated || e2.Data != 2 {
			t.Errorf("got %+v then %+v", e1, e2)
		}
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := h.Subscribe(ctx) // never read while publishing
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			h.Publish(Event{Type: RunProgress, Data: i})
		}
		h.Publish(Event{Type: RunUpdated, Data: "final"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that does not read")
	}
	// Progress was shed to make room; the state change survived.
	var last Event
	n := 0
	for {
		e, _ := recv(t, slow)
		n++
		if last = e; e.Type == RunUpdated {
			break
		}
	}
	if n > subscriberBuffer+1 {
		t.Errorf("subscriber received %d events, buffer is %d", n, subscriberBuffer)
	}
	if last.Data != "final" {
		t.Errorf("last event = %+v", last)
	}
}

func TestOverflowWithoutProgressClosesSubscriber(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := h.Subscribe(ctx)
	for i := 0; i < 3*subscriberBuffer; i++ {
		h.Publish(Event{Type: RunUpdated, Data: i})
	}
	for {
		if _, ok := recv(t, slow); !ok {
			break // closed: the client has to reconnect and reload
		}
	}
	if n := h.Subscribers(); n != 0 {
		t.Errorf("Subscribers = %d after overflow, want 0", n)
	}
}

func TestUnsubscribeOnCtxDone(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	ch := h.Subscribe(ctx)
	if n := h.Subscribers(); n != 1 {
		t.Fatalf("Subscribers = %d, want 1", n)
	}
	cancel()
	if _, ok := recv(t, ch); ok {
		t.Fatal("channel should be closed after the context ends")
	}
	if n := h.Subscribers(); n != 0 {
		t.Errorf("Subscribers = %d, want 0", n)
	}
	h.Publish(Event{Type: RunUpdated}) // must not panic or block
}
