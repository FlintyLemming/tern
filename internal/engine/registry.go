package engine

import (
	"sync"

	"tern/internal/model"
)

// Builder makes the engine for one run of t.
type Builder func(t model.Task, req Request) (Engine, error)

// Registry maps task types to the engines that run them.
type Registry struct {
	mu       sync.RWMutex
	builders map[model.TaskType]Builder
}

func NewRegistry() *Registry { return &Registry{builders: map[model.TaskType]Builder{}} }

// Register sets the builder for a task type, replacing any earlier one.
func (r *Registry) Register(tt model.TaskType, b Builder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[tt] = b
}

// Build makes the engine for one run. A task type nobody registered is a
// config error.
func (r *Registry) Build(t model.Task, req Request) (Engine, error) {
	r.mu.RLock()
	b := r.builders[t.Type]
	r.mu.RUnlock()
	if b == nil {
		return nil, &model.KindError{Kind: model.KindConfig, Msg: "no engine for task type " + string(t.Type)}
	}
	return b(t, req)
}
