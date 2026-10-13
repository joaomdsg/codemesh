package smell_test

import (
	"testing"

	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/stretchr/testify/assert"
)

func finding(pkg, name string) smell.Finding {
	return smell.Finding{Rule: smell.LongFunc, Package: pkg, File: pkg + ".go", Decl: pkg + "." + name, Subject: name}
}

func TestDelta_aSecondDeclOfTheSameNameWithTheSameSmellIsIntroduced(t *testing.T) {
	t.Parallel()
	base := []smell.Finding{finding("a", "Run")}
	head := []smell.Finding{finding("a", "Run"), finding("b", "Run")}

	introduced, fixed := smell.Delta(base, head)

	assert.Equal(t, []smell.Finding{finding("b", "Run")}, introduced, "the one that was there before is not new")
	assert.Empty(t, fixed)
}

func TestDelta_fixingOneOfTwoSameNamedSmellsIsFixed(t *testing.T) {
	t.Parallel()
	base := []smell.Finding{finding("a", "Run"), finding("b", "Run")}
	head := []smell.Finding{finding("b", "Run")}

	introduced, fixed := smell.Delta(base, head)

	assert.Empty(t, introduced)
	assert.Equal(t, []smell.Finding{finding("a", "Run")}, fixed)
}

func TestDelta_aMovedDeclCarriesItsSmellAlong(t *testing.T) {
	t.Parallel()
	base := []smell.Finding{finding("a", "Run")}
	head := []smell.Finding{finding("b", "Run")}

	introduced, fixed := smell.Delta(base, head)

	assert.Empty(t, introduced)
	assert.Empty(t, fixed)
}
