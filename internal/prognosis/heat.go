package prognosis

import (
	"math"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// Heats buckets a declaration 0 to 5 for each lens, in the order health,
// reach, structure, tests. churn is its file's, busy is BusyChurn, and
// instability its package's.
func Heats(s *code.Snapshot, d *code.Decl, churn, busy int, instability float64) [4]int {
	cx := complexityHeat(d)
	health := cx
	if cx > 0 && churn >= busy {
		health = min(5, cx+1)
	}
	reach := band(float64(len(s.Production(d.Callers))), 1, 3, 10, 30, 100)
	structure := 1 + int(math.Round(instability*4))
	tests := 0
	if cx > 0 && testCallers(s, d) == 0 {
		tests = cx
	}
	if d.Params > smell.ManyParamsLimit {
		tests = max(tests, 3)
	}
	return [4]int{health, reach, structure, tests}
}

// complexityHeat is 0 for anything but a function or method.
func complexityHeat(d *code.Decl) int {
	if d.Kind == code.Func || d.Kind == code.Method {
		return band(float64(d.Complexity), 3, 6, 10, 20)
	}
	return 0
}

// band is the count of thresholds v reaches, so 0 below the first.
func band(v float64, at ...float64) int {
	n := 0
	for _, t := range at {
		if v >= t {
			n++
		}
	}
	return min(n, 5)
}
