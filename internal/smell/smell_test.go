package smell_test

import (
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prefix = "example.com/smelly/"

var (
	smellySnap = sync.OnceValues(func() (*code.Snapshot, error) { return code.Load("testdata/smelly") })
	smellyFind = sync.OnceValues(func() ([]smell.Finding, error) {
		s, err := smellySnap()
		if err != nil {
			return nil, err
		}
		return smell.Find(s), nil
	})
)

func loadSmelly(t *testing.T) (*code.Snapshot, []smell.Finding) {
	t.Helper()
	s, err := smellySnap()
	require.NoError(t, err)
	fs, err := smellyFind()
	require.NoError(t, err)
	return s, fs
}

// of returns one rule's findings keyed by decl ID, else file, else package,
// with the module prefix trimmed.
func of(fs []smell.Finding, rule smell.Rule) map[string]smell.Severity {
	out := map[string]smell.Severity{}
	for _, f := range fs {
		if f.Rule != rule {
			continue
		}
		key := f.Decl
		if key == "" {
			key = f.File
		}
		if key == "" {
			key = f.Package
		}
		out[strings.TrimPrefix(key, prefix)] = f.Severity
	}
	return out
}

func one(t *testing.T, fs []smell.Finding, rule smell.Rule, key string) smell.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Rule == rule && (f.Decl == prefix+key || f.File == key || f.Package == prefix+key) {
			return f
		}
	}
	require.Failf(t, "finding not found", "%s %s", rule, key)
	return smell.Finding{}
}

func TestFixture_sitsOnTheThresholds(t *testing.T) {
	t.Parallel()
	s, _ := loadSmelly(t)

	for _, tc := range []struct {
		id         string
		lines      int
		complexity int
		nesting    int
		params     int
	}{
		{id: "size.Long60", lines: 60},
		{id: "size.Long61", lines: 61},
		{id: "size.Long120", lines: 120},
		{id: "size.Long121", lines: 121},
		{id: "size.T.Long61M", lines: 61},
		{id: "size.Cx10", complexity: 10},
		{id: "size.Cx11", complexity: 11},
		{id: "size.Cx20", complexity: 20},
		{id: "size.Cx21", complexity: 21},
		{id: "size.Nest4", nesting: 4},
		{id: "size.Nest5", nesting: 5},
		{id: "size.Params5", params: 5},
		{id: "size.Params6", params: 6},
	} {
		d := s.Decl(prefix + tc.id)
		require.NotNil(t, d, tc.id)
		if tc.lines != 0 {
			assert.Equal(t, tc.lines, d.Lines, tc.id)
		}
		if tc.complexity != 0 {
			assert.Equal(t, tc.complexity, d.Complexity, tc.id)
		}
		if tc.nesting != 0 {
			assert.Equal(t, tc.nesting, d.Nesting, tc.id)
		}
		if tc.params != 0 {
			assert.Equal(t, tc.params, d.Params, tc.id)
		}
	}
	for file, lines := range map[string]int{"f600.go": 600, "f601.go": 601, "f1200.go": 1200, "f1201.go": 1201} {
		for _, f := range s.Package(prefix + "big").Files {
			if strings.HasSuffix(f.Path, "/"+file) {
				assert.Equal(t, lines, f.Lines, file)
			}
		}
	}
}

func TestFind_flagsLongFunctionsAboveSixtyLines(t *testing.T) {
	t.Parallel()
	s, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{
		"size.Long61":    smell.Warn,
		"size.Long120":   smell.Warn,
		"size.Long121":   smell.High,
		"size.T.Long61M": smell.Warn,
	}, of(fs, smell.LongFunc), "not Long60, not the 1200-line var, not tests or generated code")

	f := one(t, fs, smell.LongFunc, "size.Long121")
	assert.Equal(t, "Long121", f.Subject)
	assert.Equal(t, "size/long.go", f.File)
	assert.Equal(t, s.Decl(prefix+"size.Long121").Start, f.Line)
	assert.Equal(t, prefix+"size", f.Package)
	assert.Equal(t, 121, f.Measure)
	assert.Equal(t, smell.LongFuncHighLimit, f.Limit)
	assert.Equal(t, "121 lines, limit 120", f.Detail)
	assert.Equal(t, "61 lines, limit 60", one(t, fs, smell.LongFunc, "size.Long61").Detail)
}

func TestFind_flagsComplexFunctionsAboveTen(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{
		"size.Cx11": smell.Warn,
		"size.Cx20": smell.Warn,
		"size.Cx21": smell.High,
	}, of(fs, smell.ComplexFunc))
	f := one(t, fs, smell.ComplexFunc, "size.Cx11")
	assert.Equal(t, 11, f.Measure)
	assert.Equal(t, smell.ComplexFuncLimit, f.Limit)
	assert.Equal(t, "complexity 11, limit 10", f.Detail)
	assert.Equal(t, smell.ComplexFuncHighLimit, one(t, fs, smell.ComplexFunc, "size.Cx21").Limit)
}

func TestFind_flagsNestingAboveFour(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{"size.Nest5": smell.Warn}, of(fs, smell.DeepNesting))
	f := one(t, fs, smell.DeepNesting, "size.Nest5")
	assert.Equal(t, 5, f.Measure)
	assert.Equal(t, smell.DeepNestingLimit, f.Limit)
	assert.Equal(t, "nesting 5, limit 4", f.Detail)
}

func TestFind_flagsMoreThanFiveParams(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{"size.Params6": smell.Warn}, of(fs, smell.ManyParams))
	f := one(t, fs, smell.ManyParams, "size.Params6")
	assert.Equal(t, 6, f.Measure)
	assert.Equal(t, smell.ManyParamsLimit, f.Limit)
	assert.Equal(t, "6 params, limit 5", f.Detail)
}

func TestFind_flagsFilesAboveSixHundredLines(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{
		"big/f601.go":  smell.Warn,
		"big/f1200.go": smell.Warn,
		"big/f1201.go": smell.High,
	}, of(fs, smell.LargeFile), "not f600.go, not the test or generated file")
	f := one(t, fs, smell.LargeFile, "big/f601.go")
	assert.Equal(t, "big/f601.go", f.Subject)
	assert.Equal(t, prefix+"big", f.Package)
	assert.Empty(t, f.Decl)
	assert.Equal(t, 601, f.Measure)
	assert.Equal(t, smell.LargeFileLimit, f.Limit)
	assert.Equal(t, "601 lines, limit 600", f.Detail)
	assert.Equal(t, smell.LargeFileHighLimit, one(t, fs, smell.LargeFile, "big/f1201.go").Limit)
}

func TestFind_flagsExportsNobodyOutsideUses(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	got := of(fs, smell.UnusedExport)
	for _, id := range []string{
		"core.Orphan",
		"core.OnlyTested", // its only caller is core's own external test
		"core.Hidden",
		"core.T",
		"envy.Few",
		"size.Long60",
	} {
		assert.Equal(t, smell.Info, got[id], id)
		assert.Contains(t, got, id)
	}
	for _, id := range []string{
		"core.Used",        // called from cmd/app and envy
		"hub.A",            // called from several packages
		"core.T.Method",    // methods may satisfy interfaces
		"cmd/app.Exported", // main packages export nothing
		"size.TestSize",
		"core.TestOnlyTested",
	} {
		assert.NotContains(t, got, id)
	}
	assert.Equal(t, "no use outside its package", one(t, fs, smell.UnusedExport, "core.Orphan").Detail)
}

func TestFind_flagsDeclarationsNothingReferences(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{
		"core.unusedFn":    smell.Warn,
		"core.unusedVar":   smell.Info,
		"core.unusedConst": smell.Info,
		"core.unusedType":  smell.Info,
		"cmd/app.Exported": smell.Warn,
		"cmd/app.spare":    smell.Warn,
	}, of(fs, smell.DeadCode), "not main, init, methods, tests, or exports of non-main packages")
	assert.Equal(t, "no references", one(t, fs, smell.DeadCode, "core.unusedFn").Detail)
}

func TestFind_flagsFunctionsReferencingAnotherPackageMoreThanTheirOwn(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{
		"envy.Greedy":      smell.Info,
		"envy.Outnumbered": smell.Info,
	}, of(fs, smell.EnviousFunc), "not Tied (4 vs 4) nor Few (3 refs)")
	f := one(t, fs, smell.EnviousFunc, "envy.Greedy")
	assert.Equal(t, 4, f.Measure)
	assert.Equal(t, 1, f.Limit)
	assert.Equal(t, "4 refs to hub, 1 to own package", f.Detail)
	assert.Equal(t, prefix+"hub", f.Target)
	assert.Equal(t, "4 refs to hub, 3 to own package", one(t, fs, smell.EnviousFunc, "envy.Outnumbered").Detail)
}

func TestFind_flagsImportsOfLessStablePackages(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	// chaina → chainb is a tie (both 0.50) and every other edge points at a
	// more stable package.
	require.Equal(t, map[string]smell.Severity{"core": smell.Warn}, of(fs, smell.UnstableDep))
	f := one(t, fs, smell.UnstableDep, "core")
	assert.Equal(t, "core", f.Subject)
	assert.Equal(t, prefix+"flaky", f.Target)
	assert.Empty(t, f.File)
	assert.Zero(t, f.Line)
	assert.Empty(t, f.Decl)
	assert.Equal(t, 50, f.Measure)
	assert.Equal(t, 33, f.Limit)
	assert.Equal(t, "imports flaky (I=0.50) from I=0.33", f.Detail)
}

func TestFind_leavesMainPackagesOutOfUnstableDeps(t *testing.T) {
	t.Parallel()
	// user imports cmd/tool, which is more unstable than user. Go refuses
	// that import, but the loader still records the edge.
	s, err := code.Load("testdata/mainimp")
	require.NoError(t, err)
	require.Contains(t, s.Package("example.com/mainimp/user").Imports, "example.com/mainimp/cmd/tool")

	assert.Empty(t, of(smell.Find(s), smell.UnstableDep))
}

func TestFind_flagsPackagesWithoutTests(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	assert.Equal(t, map[string]smell.Severity{
		"big":    smell.Info,
		"gen":    smell.Info,
		"hub":    smell.Info,
		"envy":   smell.Info,
		"flaky":  smell.Info,
		"chaina": smell.Info,
		"chainb": smell.Info,
	}, of(fs, smell.UntestedPackage), "not size or core (tested), nor cmd/app (main)")
	f := one(t, fs, smell.UntestedPackage, "hub")
	assert.Equal(t, "hub", f.Subject)
	assert.Equal(t, "no test files", f.Detail)
}

func TestFind_sortsBySeverityThenLocation(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	require.NotEmpty(t, fs)
	assert.Equal(t, smell.High, fs[0].Severity)
	assert.True(t, sort.SliceIsSorted(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	}))
}

func TestFind_reportsEveryRuleWithAReason(t *testing.T) {
	t.Parallel()
	_, fs := loadSmelly(t)

	seen := map[smell.Rule]bool{}
	for _, f := range fs {
		seen[f.Rule] = true
		assert.NotEmpty(t, f.Rule.Why(), f.Rule)
	}
	assert.Len(t, seen, 10)
}

func TestSeverity_stringNamesLevels(t *testing.T) {
	t.Parallel()

	assert.Less(t, smell.Info, smell.Warn)
	assert.Less(t, smell.Warn, smell.High)
	assert.Equal(t, "info", smell.Info.String())
	assert.Equal(t, "warn", smell.Warn.String())
	assert.Equal(t, "high", smell.High.String())
}
