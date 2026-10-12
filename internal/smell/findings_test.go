package smell_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/stretchr/testify/assert"
)

func TestByDecl_groupsFindingsByDeclAndSkipsThoseWithout(t *testing.T) {
	t.Parallel()
	a1, a2 := smell.Finding{Decl: "m.A", Rule: smell.LongFunc}, smell.Finding{Decl: "m.A", Rule: smell.DeepNesting}
	b := smell.Finding{Decl: "m.B", Rule: smell.ManyParams}
	file := smell.Finding{File: "big.go", Rule: smell.LargeFile}

	got := smell.ByDecl([]smell.Finding{a1, b, file, a2})

	assert.Equal(t, map[string][]smell.Finding{"m.A": {a1, a2}, "m.B": {b}}, got)
}

func TestWorst_isTheHighestSeverityAndInfoWhenThereAreNone(t *testing.T) {
	t.Parallel()
	at := func(sevs ...smell.Severity) []smell.Finding {
		var fs []smell.Finding
		for _, s := range sevs {
			fs = append(fs, smell.Finding{Severity: s})
		}
		return fs
	}
	assert.Equal(t, smell.Info, smell.Worst(nil))
	assert.Equal(t, smell.Warn, smell.Worst(at(smell.Info, smell.Warn, smell.Info)))
	assert.Equal(t, smell.High, smell.Worst(at(smell.Warn, smell.High, smell.Info)))
}
