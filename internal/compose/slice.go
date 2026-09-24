// Package compose lays out captured dashboards as print-ready HTML, which
// the browser then prints to PDF.
package compose

import (
	"sort"

	"github.com/DasPechCodeWeg/panelpost/internal/render"
)

// Stretch is how much taller than a page a band may be; such bands are
// scaled down to fit, which keeps panels whole without half-empty pages.
const Stretch = 1.2

// Slice is a horizontal band of the dashboard that fits on one page.
type Slice struct {
	Top, Bottom float64 // CSS pixels
}

// Height of the slice.
func (s Slice) Height() float64 { return s.Bottom - s.Top }

// PageSlices cuts a dashboard of the given height into bands no taller than
// pageHeight, preferring cut lines that do not run through a panel. Panels
// taller than a page are cut only where unavoidable.
func PageSlices(boxes []render.Box, total, pageHeight float64) []Slice {
	return PageSlicesFirst(boxes, total, pageHeight, pageHeight)
}

// PageSlicesFirst is PageSlices with a different capacity for the first page,
// which may carry a title block.
func PageSlicesFirst(boxes []render.Box, total, firstHeight, pageHeight float64) []Slice {
	if total <= 0 || pageHeight <= 0 || firstHeight <= 0 {
		return nil
	}
	// A cut at y is clean when no box straddles it.
	type span struct{ top, bottom float64 }
	spans := make([]span, 0, len(boxes))
	candidates := map[float64]bool{total: true}
	for _, b := range boxes {
		spans = append(spans, span{b.Y, b.Bottom()})
		// Cut just above each box's top edge (keep a small gap visible).
		candidates[clamp(b.Y-4, 0, total)] = true
		candidates[clamp(b.Bottom()+2, 0, total)] = true
	}
	clean := func(y float64) bool {
		for _, s := range spans {
			if y > s.top+0.5 && y < s.bottom-0.5 {
				return false
			}
		}
		return true
	}
	var cuts []float64
	for y := range candidates {
		if y > 0 && clean(y) {
			cuts = append(cuts, y)
		}
	}
	sort.Float64s(cuts)

	var out []Slice
	top := 0.0
	for top < total-1 {
		capacity := pageHeight
		if len(out) == 0 {
			capacity = firstHeight
		}
		limit := top + capacity
		if limit >= total {
			out = append(out, Slice{top, total})
			break
		}
		best := -1.0
		for _, c := range cuts {
			if c > top+1 && c <= limit {
				best = c
			}
		}
		// A page that would be mostly empty can instead take a clean cut up to
		// Stretch times its capacity; the band is then printed slightly smaller.
		if best < 0 || best-top < capacity*0.85 {
			for _, c := range cuts {
				if c > limit && c <= top+capacity*Stretch {
					best = c
					break
				}
			}
		}
		// Avoid wasting most of a page because a tall panel starts early:
		// only accept a clean cut that uses at least a third of the page.
		if best < 0 || best-top < capacity/3 {
			best = limit
		}
		out = append(out, Slice{top, best})
		top = best
	}
	// Never end with a sliver: fold a thin last band into the previous page
	// when it fits, or drop it when it holds nothing but whitespace.
	if n := len(out); n >= 2 && out[n-1].Height() < pageHeight*0.1 {
		last, prev := out[n-1], out[n-2]
		switch {
		case last.Bottom-prev.Top <= pageHeight*Stretch:
			out = append(out[:n-2], Slice{prev.Top, last.Bottom})
		case !intersects(spans2(boxes), last):
			out = out[:n-1]
		}
	}
	return out
}

func spans2(boxes []render.Box) []Slice {
	out := make([]Slice, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, Slice{b.Y, b.Bottom()})
	}
	return out
}

func intersects(spans []Slice, s Slice) bool {
	for _, b := range spans {
		if b.Bottom > s.Top && b.Top < s.Bottom {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
