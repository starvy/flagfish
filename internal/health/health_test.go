package health

import "testing"

func TestListenerNilSafe(t *testing.T) {
	var l *Listener
	// Every method must be safe on a nil listener — a component wired without a registry uses one.
	l.Up()
	l.Down()
	if l.Subscribed() {
		t.Fatal("nil listener reported subscribed")
	}
	if l.Subscribes() != 0 {
		t.Fatal("nil listener reported a subscribe count")
	}
	if l.Name() != "" {
		t.Fatal("nil listener reported a name")
	}
}

func TestListenerCountsResubscribes(t *testing.T) {
	reg := NewRegistry()
	l := reg.Listener("c")

	if l.Subscribed() || l.Subscribes() != 0 {
		t.Fatal("a fresh listener should be down with no subscribes")
	}

	l.Up()
	if !l.Subscribed() || l.Subscribes() != 1 {
		t.Fatalf("after first Up: subscribed=%v subscribes=%d", l.Subscribed(), l.Subscribes())
	}
	// Redundant Up must not inflate the count — only an Up that follows a Down is a resubscribe.
	l.Up()
	if l.Subscribes() != 1 {
		t.Fatalf("redundant Up inflated the count to %d", l.Subscribes())
	}

	l.Down()
	if l.Subscribed() {
		t.Fatal("after Down, still subscribed")
	}
	l.Up()
	if l.Subscribes() != 2 {
		t.Fatalf("reconnect should be the second subscribe, got %d", l.Subscribes())
	}
}

func TestRegistryDownAndDedup(t *testing.T) {
	reg := NewRegistry()
	a := reg.Listener("a")
	b := reg.Listener("b")

	// Same name returns the same handle.
	if reg.Listener("a") != a {
		t.Fatal("registry handed out two handles for one name")
	}

	if down := reg.Down(); len(down) != 2 || down[0] != "a" || down[1] != "b" {
		t.Fatalf("Down() = %v, want [a b] before any subscribe", down)
	}

	a.Up()
	b.Up()
	if down := reg.Down(); len(down) != 0 {
		t.Fatalf("Down() = %v, want empty once both are up", down)
	}

	b.Down()
	if down := reg.Down(); len(down) != 1 || down[0] != "b" {
		t.Fatalf("Down() = %v, want [b]", down)
	}
}

func TestNilRegistry(t *testing.T) {
	var reg *Registry
	// A nil registry yields nil listeners that no-op, and reports nothing down.
	l := reg.Listener("x")
	l.Up()
	if l.Subscribed() {
		t.Fatal("nil-registry listener reported subscribed")
	}
	if reg.Down() != nil {
		t.Fatal("nil registry reported something down")
	}
	if reg.All() != nil {
		t.Fatal("nil registry returned listeners")
	}
}
