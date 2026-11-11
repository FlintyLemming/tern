// Package fake is a scriptable engine for the tests of the runner, the
// scheduler and the API. It moves no data.
package fake

import (
	"context"
	"fmt"
	"sync"

	"tern/internal/engine"
	"tern/internal/model"
)

// Outcome is the scripted result of one RunUnit call.
type Outcome struct {
	Err          error
	Status       model.Status // "" means success
	Bytes, Files int64
}

// Engine is configured through its exported fields before use.
type Engine struct {
	UnitNames     []string          // default: one unit named "unit"
	Preflights    []error           // result of the n-th Preflight; nil once used up
	Results       map[int][]Outcome // unit i's n-th RunUnit; the last one repeats; none = success
	Block         chan struct{}     // when non-nil, RunUnit waits for it to close or ctx to end
	ProgressCalls int               // progress reports made by each RunUnit
	DescribeLines []string

	mu         sync.Mutex
	cond       *sync.Cond
	calls      []string
	preflights int
	unitRuns   map[int]int
	built      []engine.Request
}

// Builder returns a builder that hands out e for every run.
func Builder(e *Engine) engine.Builder {
	return func(t model.Task, req engine.Request) (engine.Engine, error) {
		e.mu.Lock()
		e.built = append(e.built, req)
		e.mu.Unlock()
		return e, nil
	}
}

func (e *Engine) record(call string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call)
	if e.cond != nil {
		e.cond.Broadcast()
	}
}

// Calls lists what was called so far, for example
// ["preflight", "unit:0", "cleanup", "preflight", "unit:0", "close"].
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// WaitCalls blocks until at least n calls were made.
func (e *Engine) WaitCalls(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cond == nil {
		e.cond = sync.NewCond(&e.mu)
	}
	for len(e.calls) < n {
		e.cond.Wait()
	}
}

// Built lists the requests the builder was asked for.
func (e *Engine) Built() []engine.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.Request(nil), e.built...)
}

func (e *Engine) Units() []string {
	if len(e.UnitNames) == 0 {
		return []string{"unit"}
	}
	return e.UnitNames
}

func (e *Engine) Preflight(ctx context.Context, env engine.Env) error {
	e.record("preflight")
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.preflights
	e.preflights++
	if n < len(e.Preflights) {
		return e.Preflights[n]
	}
	return nil
}

func (e *Engine) RunUnit(ctx context.Context, env engine.Env, i int) (model.UnitResult, error) {
	e.record(fmt.Sprintf("unit:%d", i))
	if e.Block != nil {
		select {
		case <-e.Block:
		case <-ctx.Done():
			return model.UnitResult{}, context.Cause(ctx)
		}
	}
	for n := 0; n < e.ProgressCalls && env.Progress != nil; n++ {
		env.Progress(model.Progress{Unit: e.Units()[i], BytesDone: int64(n), BytesTotal: int64(e.ProgressCalls)})
	}
	e.mu.Lock()
	if e.unitRuns == nil {
		e.unitRuns = map[int]int{}
	}
	n := e.unitRuns[i]
	e.unitRuns[i]++
	script := e.Results[i]
	e.mu.Unlock()

	var o Outcome
	if len(script) > 0 {
		o = script[min(n, len(script)-1)]
	}
	res := model.UnitResult{Name: e.Units()[i], Status: o.Status, Bytes: o.Bytes, Files: o.Files}
	if o.Err != nil {
		return res, o.Err
	}
	if res.Status == "" {
		res.Status = model.StatusSuccess
	}
	return res, nil
}

func (e *Engine) Cleanup(ctx context.Context, env engine.Env) error {
	e.record("cleanup")
	return nil
}

func (e *Engine) Describe() []string { return e.DescribeLines }

// Close makes Engine an io.Closer, as the real engines are.
func (e *Engine) Close() error {
	e.record("close")
	return nil
}
