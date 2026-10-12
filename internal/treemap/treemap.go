// Package treemap lays out weighted items as a squarified treemap
// (Bruls, Huizing and van Wijk, 2000).
package treemap

import (
	"math"
	"sort"
)

// Rect is an axis-aligned rectangle.
type Rect struct{ X, Y, W, H float64 }

// item is something to place; its tile area is proportional to Weight.
type item struct {
	ID     string
	Weight float64
}

// tile is the placed rectangle of the item with the same ID.
type tile struct {
	ID   string
	Rect Rect
}

// Inset shrinks every side by d. A side shorter than 2d collapses to zero
// length at its midpoint.
func (r Rect) Inset(d float64) Rect {
	x, w := inset(r.X, r.W, d)
	y, h := inset(r.Y, r.H, d)
	return Rect{X: x, Y: y, W: w, H: h}
}

func inset(pos, size, d float64) (float64, float64) {
	if size <= 2*d {
		return pos + size/2, 0
	}
	return pos + d, size - 2*d
}

// Placed is a value and the rectangle Place gave it.
type Placed[T any] struct {
	Val  T
	Rect Rect
}

// Place lays vals out in r as layout does, keyed and sized by size, and
// drops values whose weight is not finite and above zero.
func Place[T any](vals []T, r Rect, size func(T) (id string, weight float64)) []Placed[T] {
	items := make([]item, len(vals))
	byID := make(map[string]T, len(vals))
	for i, v := range vals {
		id, w := size(v)
		items[i] = item{ID: id, Weight: w}
		byID[id] = v
	}
	tiles := layout(items, r)
	out := make([]Placed[T], len(tiles))
	for i, t := range tiles {
		out[i] = Placed[T]{byID[t.ID], t.Rect}
	}
	return out
}

// layout places one tile per item with a finite weight above zero. Tiles
// fill r, in descending weight order with ties by ID, so the result does not
// depend on input order. It returns nil when no item is kept or r has no
// finite positive area.
func layout(items []item, r Rect) []tile {
	if !(r.W > 0 && r.H > 0) || math.IsInf(r.W, 0) || math.IsInf(r.H, 0) {
		return nil
	}
	kept := sortedKept(items)
	if len(kept) == 0 {
		return nil
	}
	areas := areasIn(kept, r)
	tiles := make([]tile, len(kept))
	rest := r
	for i := 0; i < len(kept); {
		side := math.Min(rest.W, rest.H)
		if side <= 0 || areas[i] <= 0 {
			for ; i < len(kept); i++ {
				tiles[i] = tile{ID: kept[i].ID, Rect: Rect{X: rest.X, Y: rest.Y}}
			}
			break
		}
		j, rowSum := rowEnd(areas, i, side)
		rest = placeRow(tiles[i:j], kept[i:j], areas[i:j], rowSum, rest)
		i = j
	}
	return tiles
}

// sortedKept drops items without a finite positive weight and sorts the rest
// by descending weight, ties by ID.
func sortedKept(items []item) []item {
	kept := make([]item, 0, len(items))
	for _, it := range items {
		if it.Weight > 0 && !math.IsInf(it.Weight, 0) {
			kept = append(kept, it)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Weight != kept[j].Weight {
			return kept[i].Weight > kept[j].Weight
		}
		return kept[i].ID < kept[j].ID
	})
	return kept
}

// areasIn scales the weights of items, largest first, to areas that sum to
// the area of r.
func areasIn(items []item, r Rect) []float64 {
	// Dividing by the largest weight first keeps the sum from overflowing.
	var sum float64
	for _, it := range items {
		sum += it.Weight / items[0].Weight
	}
	scale := r.W * r.H / sum
	areas := make([]float64, len(items))
	for i, it := range items {
		areas[i] = it.Weight / items[0].Weight * scale
	}
	return areas
}

// rowEnd returns where the row starting at areas[i] ends, and its total area:
// it takes areas while they do not worsen the row's worst aspect ratio.
func rowEnd(areas []float64, i int, side float64) (end int, rowSum float64) {
	end, rowSum = i+1, areas[i]
	for end < len(areas) && worst(rowSum+areas[end], areas[i], areas[end], side) <= worst(rowSum, areas[i], areas[end-1], side) {
		rowSum += areas[end]
		end++
	}
	return end, rowSum
}

// worst is the largest aspect ratio in a row along a side of the given
// length; largest and smallest are the row's largest and smallest areas.
func worst(rowSum, largest, smallest, side float64) float64 {
	s2, l2 := rowSum*rowSum, side*side
	return math.Max(l2*largest/s2, s2/(l2*smallest))
}

// placeRow fills a strip along the short side of rest and returns what is
// left of rest.
func placeRow(tiles []tile, items []item, areas []float64, rowSum float64, rest Rect) Rect {
	if rest.W >= rest.H {
		strip := rowSum / rest.H
		y := rest.Y
		for i, it := range items {
			h := areas[i] / rowSum * rest.H
			tiles[i] = tile{ID: it.ID, Rect: Rect{X: rest.X, Y: y, W: strip, H: h}}
			y += h
		}
		return Rect{X: rest.X + strip, Y: rest.Y, W: math.Max(rest.W-strip, 0), H: rest.H}
	}
	strip := rowSum / rest.W
	x := rest.X
	for i, it := range items {
		w := areas[i] / rowSum * rest.W
		tiles[i] = tile{ID: it.ID, Rect: Rect{X: x, Y: rest.Y, W: w, H: strip}}
		x += w
	}
	return Rect{X: rest.X, Y: rest.Y + strip, W: rest.W, H: math.Max(rest.H-strip, 0)}
}
