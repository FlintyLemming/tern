package clock

import (
	"testing"
	"time"
)

var start = time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)

func fired(ch <-chan time.Time) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestFakeAdvanceFiresInOrder(t *testing.T) {
	f := NewFake(start)
	late, early := f.After(10*time.Second), f.After(5*time.Second)
	f.Advance(4 * time.Second)
	if fired(early) || fired(late) {
		t.Fatal("fired too early")
	}
	f.Advance(time.Second)
	if !fired(early) || fired(late) {
		t.Fatal("at +5s only the early timer should have fired")
	}
	f.Advance(time.Hour)
	select {
	case at := <-late:
		if !at.Equal(start.Add(10 * time.Second)) {
			t.Errorf("late timer delivered %v, want its own deadline", at)
		}
	default:
		t.Fatal("late timer did not fire")
	}
	if !f.Now().Equal(start.Add(time.Hour + 5*time.Second)) {
		t.Errorf("Now = %v", f.Now())
	}
}

func TestFakeAfterNonPositiveFiresAtOnce(t *testing.T) {
	f := NewFake(start)
	if !fired(f.After(0)) {
		t.Error("After(0) should be ready immediately")
	}
}

func TestFakeSetBackwards(t *testing.T) {
	f := NewFake(start)
	ch := f.After(time.Minute)
	f.Set(start.Add(-time.Hour))
	if fired(ch) || !f.Now().Equal(start.Add(-time.Hour)) {
		t.Fatal("going back must not fire timers")
	}
	f.Set(start.Add(time.Minute))
	if !fired(ch) {
		t.Error("timer should fire once its deadline is reached again")
	}
}

func TestFakeBlockUntil(t *testing.T) {
	f := NewFake(start)
	done := make(chan struct{})
	go func() {
		f.BlockUntil(2)
		close(done)
	}()
	f.After(time.Second)
	if fired2(done) {
		t.Fatal("BlockUntil(2) returned with one waiter")
	}
	f.After(time.Second)
	<-done
}

func fired2(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestRealClock(t *testing.T) {
	before := time.Now()
	if now := Real.Now(); now.Before(before) {
		t.Errorf("Real.Now() = %v, before %v", now, before)
	}
	select {
	case <-Real.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("Real.After never fired")
	}
}
