package compose

import (
	"testing"

	"github.com/DasPechCodeWeg/panelpost/internal/render"
)

func box(y, h float64) render.Box { return render.Box{Y: y, H: h, W: 100} }

func TestSlicesNeverCutPanelsWhenAvoidable(t *testing.T) {
	// Rows of panels 300px tall with 16px gaps, on pages 700px tall.
	var boxes []render.Box
	for i := 0; i < 8; i++ {
		y := 16 + float64(i)*316
		boxes = append(boxes, box(y, 300), render.Box{X: 500, Y: y, W: 100, H: 300})
	}
	total := 16 + 8*316.0
	slices := PageSlices(boxes, total, 700)
	if len(slices) < 4 {
		t.Fatalf("slices = %v", slices)
	}
	for i, s := range slices {
		if s.Height() > 700*Stretch {
			t.Errorf("slice %d too tall: %v", i, s)
		}
		for _, b := range boxes {
			if s.Bottom < total && s.Bottom > b.Y && s.Bottom < b.Bottom() {
				t.Errorf("slice %d cuts through panel at %v-%v: %v", i, b.Y, b.Bottom(), s)
			}
		}
	}
	if slices[0].Top != 0 || slices[len(slices)-1].Bottom != total {
		t.Errorf("slices do not cover the dashboard: %v", slices)
	}
	for i := 1; i < len(slices); i++ {
		if slices[i].Top != slices[i-1].Bottom {
			t.Errorf("gap between slices %d and %d", i-1, i)
		}
	}
}

func TestTallPanelIsCutWhenItMust(t *testing.T) {
	boxes := []render.Box{box(0, 2000)}
	slices := PageSlices(boxes, 2000, 700)
	if len(slices) != 3 || slices[0].Bottom != 700 || slices[2].Bottom != 2000 {
		t.Errorf("slices = %v", slices)
	}
}

func TestShortDashboardIsOnePage(t *testing.T) {
	slices := PageSlices([]render.Box{box(10, 200)}, 230, 700)
	if len(slices) != 1 || slices[0].Bottom != 230 {
		t.Errorf("slices = %v", slices)
	}
	if PageSlices(nil, 0, 700) != nil {
		t.Error("empty dashboard should have no slices")
	}
}

func TestEarlyTallPanelDoesNotWastePages(t *testing.T) {
	// A small panel, then a panel taller than a page starting at 100px:
	// cutting cleanly at ~96px would waste almost a whole page.
	boxes := []render.Box{box(0, 90), box(100, 1500)}
	slices := PageSlices(boxes, 1600, 700)
	if slices[0].Bottom < 600 {
		t.Errorf("first page nearly empty: %v", slices)
	}
}

func TestFirstPageCapacity(t *testing.T) {
	slices := PageSlicesFirst(nil, 1500, 500, 700)
	if len(slices) != 3 || slices[0].Height() != 500 || slices[1].Height() != 700 || slices[2].Height() != 300 {
		t.Errorf("slices = %v", slices)
	}
}

func TestStretchAvoidsHalfEmptyPages(t *testing.T) {
	// Panels of 450px: two do not fit on a 900px page (916 > 900), but a
	// slight shrink does, instead of printing one panel per page.
	boxes := []render.Box{box(8, 450), box(466, 450), box(924, 450), box(1382, 450)}
	slices := PageSlices(boxes, 1840, 900)
	if len(slices) != 2 {
		t.Fatalf("slices = %v", slices)
	}
	if slices[0].Height() <= 900 || slices[0].Height() > 900*Stretch {
		t.Errorf("first slice = %v", slices[0])
	}
}
