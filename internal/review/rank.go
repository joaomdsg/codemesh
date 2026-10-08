package review

import (
	"cmp"
	"fmt"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/smell"
)

func (b *builder) rank(u *Unit, old, new *code.Decl) {
	resigned := old != nil && new != nil && old.Signature != new.Signature
	u.Lane = b.laneOf(u, cmp.Or(new, old), resigned)
	u.Risk = min(u.Added+u.Deleted, 40) / 4
	add := func(reason string, points int) {
		if reason != "" {
			u.Reasons = append(u.Reasons, reason)
		}
		u.Risk += points
	}
	if resigned {
		add("signature changed", 6)
	}
	if u.Change == Removed {
		add("", 4)
	}
	if new != nil {
		u.Callers = new.Callers
		prod, tests := b.callers(new)
		if prod > 0 {
			add(plural(prod, "caller"), 3*min(prod, 10))
		}
		if tests > 0 {
			add(plural(tests, "test caller"), 0)
		}
	}
	cur := cmp.Or(new, old)
	if cur.Data {
		add("string data", 0)
	}
	// A test function is exported only so the test runner finds it.
	if u.Exported && !cur.Test {
		add("exported", 4)
	}
	if new == nil || new.Kind != code.Func && new.Kind != code.Method {
		return
	}
	add(complexityReason(old, new))
	if _, tests := b.callers(new); !new.Test && tests == 0 {
		add("no direct test", 5)
	}
}

func (b *builder) laneOf(u *Unit, d *code.Decl, resigned bool) Lane {
	switch {
	case d.Test:
		return Tests
	case u.Exported && b.public(d.Package) && (u.Change != Modified || resigned):
		return Contract
	case d.Data:
		return Other
	}
	return Logic
}

// public reports whether other modules can import the package: not under an
// internal directory and not a command.
func (b *builder) public(pkg string) bool {
	if b.in.Head.Package(pkg) == nil {
		return b.in.Base.Importable(pkg)
	}
	return b.in.Head.Importable(pkg)
}

// complexityReason names a complexity worth a reviewer's notice: one that
// changed, or one past the smell limit. Rising complexity scores double.
func complexityReason(old, new *code.Decl) (string, int) {
	c := new.Complexity
	if old != nil && c != old.Complexity {
		d := c - old.Complexity
		return fmt.Sprintf("complexity %d (%+d)", c, d), c/2 + 2*max(d, 0)
	}
	if c > smell.ComplexFuncLimit {
		return fmt.Sprintf("complexity %d", c), c / 2
	}
	return "", c / 2
}

// callers splits a decl's callers into production and test code. Only
// production callers widen the blast radius.
func (b *builder) callers(d *code.Decl) (prod, tests int) {
	for _, id := range d.Callers {
		if c := b.in.Head.Decl(id); c != nil && c.Test {
			tests++
		} else {
			prod++
		}
	}
	return prod, tests
}
