// Package health publishes the facts a connection check cannot learn.
//
// A pool ping proves the database is reachable. It proves nothing about a Postgres LISTEN
// subscription, which lives on one backend connection and dies with it — silently, while the pool
// reconnects around it and every probe stays green. This package is the one place those
// subscriptions say whether they are actually established, so readiness and the metrics surface can
// both read it.
//
// It imports nothing but the standard library, so the subscribers and the probe that reads them can
// share it without either importing the other.
package health

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// A Listener is one long-lived LISTEN subscription's state. Every method is safe on a nil receiver,
// so a component wired without a registry — a narrow test harness, a CLI — simply reports nothing
// instead of forcing a nil check at each call site.
type Listener struct {
	name         string
	subscribed   atomic.Bool
	resubscribes atomic.Int64
}

// Name is the channel this listener stands for.
func (l *Listener) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Up marks the subscription established. Every Up after the first counts as a resubscribe, which is
// the number an operator wants on a graph: a pump that reconnects all night is a broken network,
// not a healthy one.
func (l *Listener) Up() {
	if l == nil {
		return
	}
	if !l.subscribed.Swap(true) {
		l.resubscribes.Add(1)
	}
}

// Down marks the subscription lost.
func (l *Listener) Down() {
	if l == nil {
		return
	}
	l.subscribed.Store(false)
}

// Subscribed reports whether the subscription is currently established.
func (l *Listener) Subscribed() bool {
	return l != nil && l.subscribed.Load()
}

// Subscribes is how many times this listener has established its subscription, first one included.
func (l *Listener) Subscribes() int64 {
	if l == nil {
		return 0
	}
	return l.resubscribes.Load()
}

// A Registry is the set of listeners this process depends on. Listeners register at construction
// time, before they are started, so a process that has not finished subscribing reads as not ready
// rather than as healthy-with-nothing-registered.
type Registry struct {
	mu     sync.Mutex
	byName map[string]*Listener
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byName: map[string]*Listener{}} }

// Listener registers name and returns its handle, or the existing handle if it is already
// registered. A nil registry yields a nil listener, which no-ops rather than panics.
func (r *Registry) Listener(name string) *Listener {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.byName[name]; ok {
		return l
	}
	l := &Listener{name: name}
	r.byName[name] = l
	return l
}

// All returns every registered listener, ordered by name so a metrics scrape is stable.
func (r *Registry) All() []*Listener {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Listener, 0, len(r.byName))
	for _, l := range r.byName {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b *Listener) int { return strings.Compare(a.name, b.name) })
	return out
}

// Down names every registered listener whose subscription is not currently established. Empty means
// every pump this process needs is live.
func (r *Registry) Down() []string {
	var down []string
	for _, l := range r.All() {
		if !l.Subscribed() {
			down = append(down, l.name)
		}
	}
	return down
}
