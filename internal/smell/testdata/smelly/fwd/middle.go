package fwd

type engine struct{}

func (engine) a(n int) int      { return n }
func (engine) b(n int) int      { return n + 1 }
func (engine) e(s string) error { return nil }
func (engine) f(s string) error { return nil }
func (engine) put(s string)     {}

// relay hands three of its four methods to in.
type relay struct{ in engine }

func (r relay) a(n int) int  { return r.in.a(n) }
func (r relay) b(n int) int  { return r.in.b(n) }
func (r relay) put(s string) { r.in.put(s) }
func (r relay) c(n int) int  { return n * 2 }

// generic is relay with a type parameter, so each method sees its own
// instantiation of in.
type generic[T any] struct {
	in engine
	v  T
}

func (g generic[T]) a(n int) int { return g.in.a(n) }
func (g generic[T]) b(n int) int { return g.in.b(n) }
func (g generic[T]) c(n int) int { return n * 2 }

// split forwards to two fields, neither of them for half its methods.
type split struct{ x, y engine }

func (s split) a(n int) int { return s.x.a(n) }
func (s split) b(n int) int { return s.y.b(n) }
func (s split) c(n int) int { return n * 2 }

// renamer swaps names, so each method means something new.
type renamer struct{ in engine }

func (r renamer) a(n int) int { return r.in.b(n) }
func (r renamer) b(n int) int { return r.in.a(n) }
func (r renamer) c(n int) int { return n * 2 }

// dropper swallows the errors its field returns.
type dropper struct{ in engine }

func (d dropper) e(s string)  { d.in.e(s) }
func (d dropper) f(s string)  { d.in.f(s) }
func (d dropper) c(n int) int { return n * 2 }

// pair has too few methods to be a layer of its own.
type pair struct{ in engine }

func (p pair) a(n int) int { return p.in.a(n) }
func (p pair) b(n int) int { return p.in.b(n) }

type shape interface {
	a(n int) int
	b(n int) int
	c(n int) int
}

// decorator stands in for the shape it wraps and changes c.
type decorator struct{ in shape }

func (d decorator) a(n int) int { return d.in.a(n) }
func (d decorator) b(n int) int { return d.in.b(n) }
func (d decorator) c(n int) int { return d.in.c(n) * 2 }

// halfgen's delegating methods are generated, which nobody edits by hand.
type halfgen struct{ in engine }

func (h halfgen) c(n int) int { return n * 2 }

// Deprecated: use engine.
type legacy struct{ in engine }

func (l legacy) a(n int) int { return l.in.a(n) }
func (l legacy) b(n int) int { return l.in.b(n) }
func (l legacy) c(n int) int { return n * 2 }
