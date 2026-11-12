//go:build acceptance

// Package acceptance holds the spec §16 acceptance tests that run inside
// the integration environment, on a network made bad on purpose. Run them
// with `make acceptance`.
//
// The rules go on the runner's own interface. The node under test runs in
// the test process, so it and every ssh, rsync, rclone and restic it starts
// share that network namespace; the test's own API calls go over loopback
// and are not affected.
package acceptance

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const netDev = "eth0"

var (
	netemMu   sync.Mutex
	netemRule string // the netem rule in force; "" for none
)

func tc(args ...string) error {
	out, err := exec.Command("tc", args...).CombinedOutput()
	if err != nil {
		return &tcError{args: args, out: string(out), err: err}
	}
	return nil
}

type tcError struct {
	args []string
	out  string
	err  error
}

func (e *tcError) Error() string {
	return "tc " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + strings.TrimSpace(e.out)
}

// setRule puts rule on the interface, or removes every rule when it is "".
func setRule(rule string) error {
	if rule == "" {
		err := tc("qdisc", "del", "dev", netDev, "root")
		if err != nil && strings.Contains(err.Error(), "No such file") {
			err = nil // there was none
		}
		return err
	}
	return tc(append([]string{"qdisc", "replace", "dev", netDev, "root", "netem"}, strings.Fields(rule)...)...)
}

// netem applies a netem rule (e.g. "loss 5%") to everything the runner
// sends, until the test ends.
func netem(t *testing.T, spec string) {
	t.Helper()
	netemMu.Lock()
	defer netemMu.Unlock()
	if err := setRule(spec); err != nil {
		t.Fatal(err)
	}
	netemRule = spec
	t.Cleanup(func() {
		netemMu.Lock()
		defer netemMu.Unlock()
		if err := setRule(""); err != nil {
			t.Error(err)
		}
		netemRule = ""
	})
}

// blackout drops every packet the runner sends for d, then puts back the
// rule that was in force before. It blocks for d.
func blackout(t *testing.T, d time.Duration) {
	t.Helper()
	netemMu.Lock()
	prev := netemRule
	if err := setRule("loss 100%"); err != nil {
		netemMu.Unlock()
		t.Fatal(err)
	}
	netemMu.Unlock()
	t.Logf("blackout for %s", d)
	time.Sleep(d)
	netemMu.Lock()
	defer netemMu.Unlock()
	if err := setRule(prev); err != nil {
		t.Fatal(err)
	}
	t.Log("blackout over")
}

func TestNetemRules(t *testing.T) {
	netem(t, "loss 5%")
	out, err := exec.Command("tc", "qdisc", "show", "dev", netDev).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "loss 5%") {
		t.Fatalf("tc qdisc show: %v\n%s", err, out)
	}
	blackout(t, time.Second)
	out, _ = exec.Command("tc", "qdisc", "show", "dev", netDev).CombinedOutput()
	if !strings.Contains(string(out), "loss 5%") {
		t.Fatalf("the rule before the blackout is not back:\n%s", out)
	}
}
