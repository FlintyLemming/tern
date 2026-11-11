package scheduler

import (
	"context"
	"errors"
	"testing"
)

func granted(tk *ticket) bool {
	select {
	case <-tk.ch:
		return true
	default:
		return false
	}
}

// The place in the queue is taken when the run is queued, not whenever its
// goroutine gets around to asking: first queued, first served.
func TestSlotsFIFOByReservation(t *testing.T) {
	s := &slots{capacity: 1}
	t1, t2, t3 := s.reserve(), s.reserve(), s.reserve()
	if !granted(t1) || granted(t2) || granted(t3) {
		t.Fatal("only the first reservation should hold the slot")
	}
	// Waiting in the "wrong" order changes nothing.
	ctx := context.Background()
	if err := t1.wait(ctx); err != nil {
		t.Fatal(err)
	}
	s.release()
	if !granted(t2) || granted(t3) {
		t.Fatal("the slot should pass to the second reservation")
	}
	s.release()
	if !granted(t3) {
		t.Fatal("the slot should pass to the third reservation")
	}
}

func TestSlotsCanceledReservationLeavesQueue(t *testing.T) {
	s := &slots{capacity: 1}
	t1, t2, t3 := s.reserve(), s.reserve(), s.reserve()
	cause := errors.New("stop")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	if err := t2.wait(ctx); !errors.Is(err, cause) {
		t.Fatalf("wait = %v, want the cancel cause", err)
	}
	_ = t1
	s.release()
	if !granted(t3) {
		t.Error("a canceled reservation still blocked the queue")
	}
	// Canceled although already granted: the slot goes back.
	if err := t3.wait(ctx); err == nil {
		t.Fatal("wait with a dead context succeeded")
	}
	if t4 := s.reserve(); !granted(t4) {
		t.Error("slot was not returned")
	}
}

func TestSlotsCapacityChange(t *testing.T) {
	s := &slots{capacity: 1}
	t1, t2, t3 := s.reserve(), s.reserve(), s.reserve()
	s.setCapacity(2)
	if !granted(t1) || !granted(t2) || granted(t3) {
		t.Fatal("raising the capacity to 2 should admit exactly one more")
	}
	s.setCapacity(1)
	s.release()
	if granted(t3) {
		t.Fatal("with capacity back at 1 and one slot still in use, nobody is admitted")
	}
	s.release()
	if !granted(t3) {
		t.Fatal("third reservation never admitted")
	}
}
