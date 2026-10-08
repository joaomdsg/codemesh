package treemap_test

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/joaomdsg/codemesh/internal/treemap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const eps = 1e-6

var canvas = treemap.Rect{X: 10, Y: 20, W: 1000, H: 600}

func fixtures() map[string][]treemap.Item {
	return map[string][]treemap.Item{
		"ten mixed": {
			{ID: "a", Weight: 600}, {ID: "b", Weight: 400}, {ID: "c", Weight: 300},
			{ID: "d", Weight: 250}, {ID: "e", Weight: 120}, {ID: "f", Weight: 90},
			{ID: "g", Weight: 60}, {ID: "h", Weight: 40}, {ID: "i", Weight: 20},
			{ID: "j", Weight: 5},
		},
		"equal weights": {
			{ID: "a", Weight: 1}, {ID: "b", Weight: 1}, {ID: "c", Weight: 1},
			{ID: "d", Weight: 1}, {ID: "e", Weight: 1}, {ID: "f", Weight: 1},
			{ID: "g", Weight: 1},
		},
		"one dominant": {
			{ID: "big", Weight: 10000}, {ID: "x", Weight: 3}, {ID: "y", Weight: 2},
			{ID: "z", Weight: 1},
		},
		"two items": {{ID: "a", Weight: 3}, {ID: "b", Weight: 1}},
		"tiny and huge": {
			{ID: "a", Weight: 1e300}, {ID: "b", Weight: 1e300}, {ID: "c", Weight: 1e290},
		},
	}
}

func rectArea(r treemap.Rect) float64 { return r.W * r.H }

func overlaps(a, b treemap.Rect) bool {
	return math.Min(a.X+a.W, b.X+b.W)-math.Max(a.X, b.X) > eps &&
		math.Min(a.Y+a.H, b.Y+b.H)-math.Max(a.Y, b.Y) > eps
}

func aspect(r treemap.Rect) float64 {
	return math.Max(r.W/r.H, r.H/r.W)
}

func meanAspect(tiles []treemap.Tile) float64 {
	var sum float64
	for _, t := range tiles {
		sum += aspect(t.Rect)
	}
	return sum / float64(len(tiles))
}

// sliceAndDice is the baseline: one strip per item along the long axis.
func sliceAndDice(items []treemap.Item, r treemap.Rect) []treemap.Tile {
	var total float64
	for _, it := range items {
		total += it.Weight
	}
	var tiles []treemap.Tile
	x := r.X
	for _, it := range items {
		w := r.W * it.Weight / total
		tiles = append(tiles, treemap.Tile{ID: it.ID, Rect: treemap.Rect{X: x, Y: r.Y, W: w, H: r.H}})
		x += w
	}
	return tiles
}

func TestLayout_areasAreProportionalToWeight(t *testing.T) {
	t.Parallel()
	for name, items := range fixtures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tiles := treemap.Layout(items, canvas)
			require.Len(t, tiles, len(items))

			var total float64
			for _, it := range items {
				total += it.Weight
			}
			byID := map[string]treemap.Rect{}
			var sumArea float64
			for _, tl := range tiles {
				byID[tl.ID] = tl.Rect
				sumArea += rectArea(tl.Rect)
			}
			assert.InEpsilon(t, rectArea(canvas), sumArea, eps)
			for _, it := range items {
				want := rectArea(canvas) * it.Weight / total
				assert.InEpsilon(t, want, rectArea(byID[it.ID]), eps, it.ID)
			}
		})
	}
}

func TestLayout_tilesStayInsideRect(t *testing.T) {
	t.Parallel()
	for name, items := range fixtures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, tl := range treemap.Layout(items, canvas) {
				assert.GreaterOrEqual(t, tl.Rect.X, canvas.X-eps, tl.ID)
				assert.GreaterOrEqual(t, tl.Rect.Y, canvas.Y-eps, tl.ID)
				assert.LessOrEqual(t, tl.Rect.X+tl.Rect.W, canvas.X+canvas.W+eps, tl.ID)
				assert.LessOrEqual(t, tl.Rect.Y+tl.Rect.H, canvas.Y+canvas.H+eps, tl.ID)
			}
		})
	}
}

func TestLayout_tilesDoNotOverlap(t *testing.T) {
	t.Parallel()
	for name, items := range fixtures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tiles := treemap.Layout(items, canvas)
			for i := range tiles {
				for j := i + 1; j < len(tiles); j++ {
					assert.False(t, overlaps(tiles[i].Rect, tiles[j].Rect),
						"%s overlaps %s", tiles[i].ID, tiles[j].ID)
				}
			}
		})
	}
}

func TestLayout_ignoresInputOrder(t *testing.T) {
	t.Parallel()
	for name, items := range fixtures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := treemap.Layout(items, canvas)
			rng := rand.New(rand.NewSource(1))
			for range 10 {
				shuffled := append([]treemap.Item(nil), items...)
				rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
				assert.Equal(t, want, treemap.Layout(shuffled, canvas))
			}
		})
	}
}

func TestLayout_breaksWeightTiesByID(t *testing.T) {
	t.Parallel()
	a := treemap.Layout([]treemap.Item{{ID: "a", Weight: 1}, {ID: "b", Weight: 1}, {ID: "c", Weight: 1}}, canvas)
	b := treemap.Layout([]treemap.Item{{ID: "c", Weight: 1}, {ID: "b", Weight: 1}, {ID: "a", Weight: 1}}, canvas)
	assert.Equal(t, a, b)
	assert.Equal(t, []string{"a", "b", "c"}, []string{a[0].ID, a[1].ID, a[2].ID})
}

func TestLayout_beatsSliceAndDiceAspectRatio(t *testing.T) {
	t.Parallel()
	items := fixtures()["ten mixed"]
	squarified := meanAspect(treemap.Layout(items, canvas))
	baseline := meanAspect(sliceAndDice(items, canvas))
	assert.Less(t, squarified, baseline/2, "squarified %.2f vs slice-and-dice %.2f", squarified, baseline)
	assert.Less(t, squarified, 3.0)
}

func TestLayout_emptyInputGivesNoTiles(t *testing.T) {
	t.Parallel()
	assert.Empty(t, treemap.Layout(nil, canvas))
	assert.Empty(t, treemap.Layout([]treemap.Item{}, canvas))
}

func TestLayout_singleItemFillsRect(t *testing.T) {
	t.Parallel()
	tiles := treemap.Layout([]treemap.Item{{ID: "only", Weight: 7}}, canvas)
	require.Len(t, tiles, 1)
	assert.Equal(t, "only", tiles[0].ID)
	assert.InDelta(t, canvas.X, tiles[0].Rect.X, eps)
	assert.InDelta(t, canvas.Y, tiles[0].Rect.Y, eps)
	assert.InDelta(t, canvas.W, tiles[0].Rect.W, eps)
	assert.InDelta(t, canvas.H, tiles[0].Rect.H, eps)
}

func TestLayout_dropsInvalidWeights(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		weight float64
	}{
		{"zero", 0},
		{"negative", -5},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tiles := treemap.Layout([]treemap.Item{
				{ID: "keep", Weight: 2}, {ID: "drop", Weight: tt.weight}, {ID: "keep2", Weight: 2},
			}, canvas)
			require.Len(t, tiles, 2)
			for _, tl := range tiles {
				assert.NotEqual(t, "drop", tl.ID)
				assert.InEpsilon(t, rectArea(canvas)/2, rectArea(tl.Rect), eps)
			}
		})
	}
}

func TestLayout_onlyInvalidWeightsGivesNoTiles(t *testing.T) {
	t.Parallel()
	assert.Empty(t, treemap.Layout([]treemap.Item{{ID: "a", Weight: 0}, {ID: "b", Weight: math.NaN()}}, canvas))
}

func TestLayout_zeroAreaRectGivesNoTiles(t *testing.T) {
	t.Parallel()
	items := fixtures()["ten mixed"]
	for _, r := range []treemap.Rect{
		{}, {X: 5, Y: 5, W: 0, H: 100}, {X: 5, Y: 5, W: 100, H: 0},
		{W: -1, H: 10}, {W: math.NaN(), H: 10}, {W: math.Inf(1), H: 10},
	} {
		assert.Empty(t, treemap.Layout(items, r), "%+v", r)
	}
}

func TestLayout_survivesTinyWeights(t *testing.T) {
	t.Parallel()
	items := []treemap.Item{
		{ID: "big", Weight: 1e300}, {ID: "big2", Weight: 1e300}, {ID: "big3", Weight: 1e300},
		{ID: "dust", Weight: 5e-324}, {ID: "dust2", Weight: 5e-324},
	}
	tiles := treemap.Layout(items, canvas)
	require.Len(t, tiles, 5)
	for _, tl := range tiles {
		for _, v := range []float64{tl.Rect.X, tl.Rect.Y, tl.Rect.W, tl.Rect.H} {
			assert.False(t, math.IsNaN(v) || math.IsInf(v, 0), "%s: %+v", tl.ID, tl.Rect)
		}
	}
}

func TestRect_insetShrinksEverySide(t *testing.T) {
	t.Parallel()
	got := treemap.Rect{X: 10, Y: 20, W: 100, H: 50}.Inset(3)
	assert.Equal(t, treemap.Rect{X: 13, Y: 23, W: 94, H: 44}, got)
}

func TestRect_insetCollapsesToCentre(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   treemap.Rect
		d    float64
		want treemap.Rect
	}{
		{"both axes", treemap.Rect{X: 10, Y: 20, W: 4, H: 6}, 5, treemap.Rect{X: 12, Y: 23}},
		{"width only", treemap.Rect{X: 10, Y: 20, W: 4, H: 50}, 5, treemap.Rect{X: 12, Y: 25, W: 0, H: 40}},
		{"exactly 2d", treemap.Rect{X: 0, Y: 0, W: 10, H: 10}, 5, treemap.Rect{X: 5, Y: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.in.Inset(tt.d))
		})
	}
}

func Example() {
	tiles := treemap.Layout([]treemap.Item{{ID: "a", Weight: 3}, {ID: "b", Weight: 1}}, treemap.Rect{W: 40, H: 10})
	for _, tl := range tiles {
		fmt.Printf("%s %.0fx%.0f\n", tl.ID, tl.Rect.W, tl.Rect.H)
	}
	// Output:
	// a 30x10
	// b 10x10
}
