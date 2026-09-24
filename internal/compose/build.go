package compose

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/render"
)

// Options tune image encoding.
type Options struct {
	Format  string // png (sharpest) or jpeg (smaller files)
	Quality int    // jpeg quality
	// Panels restricts panels mode to these titles (case-insensitive).
	Panels []string
}

// Result is a finished PDF.
type Result struct {
	PDF   []byte
	Pages int
}

// Build captures the page's panels, lays them out and prints the PDF.
func Build(ctx context.Context, b *render.Browser, p *render.Page, d Document, opts Options) (*Result, error) {
	if opts.Format == "" {
		opts.Format = "jpeg"
	}
	if len(p.Geometry.Panels) == 0 {
		return nil, fmt.Errorf("the dashboard rendered no panels; the report was not produced so that nobody receives an empty PDF")
	}
	var pages []Image
	var sections []Section
	var err error
	if d.Mode == "panels" {
		sections, err = panelSections(p, opts)
		scale := d.PanelScale(p.Width)
		for i := range sections {
			for j := range sections[i].Images {
				img := &sections[i].Images[j]
				img.WidthMM = img.W * scale
				if d.Columns == 1 { // one panel per line, enlarged up to 2x
					img.WidthMM = min(d.ContentWidthMM(), img.W*scale*2)
				}
			}
		}
	} else {
		pages, err = dashboardPages(p, d, opts)
	}
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 && len(sections) == 0 {
		return nil, fmt.Errorf("nothing to include: the dashboard rendered no panels%s", filterHint(opts))
	}
	html, err := d.HTML(pages, sections)
	if err != nil {
		return nil, err
	}
	pdf, err := b.PrintHTML(ctx, html, 90*time.Second)
	if err != nil {
		return nil, err
	}
	return &Result{PDF: pdf, Pages: CountPages(pdf)}, nil
}

func filterHint(opts Options) string {
	if len(opts.Panels) > 0 {
		return " matching the selected panel titles"
	}
	return ""
}

func dashboardPages(p *render.Page, d Document, opts Options) ([]Image, error) {
	geo := p.Geometry
	// Stop just below the last panel: kiosk mode prints a "Powered by"
	// footer further down that does not belong in a report.
	total := geo.Bottom + 4
	if total > p.Height {
		total = p.Height
	}
	boxes := append(append([]render.Box{}, geo.Panels...), geo.Rows...)
	first, rest := d.SliceHeights(p.Width)
	var out []Image
	for i, s := range PageSlicesFirst(boxes, total, first, rest) {
		data, err := p.Screenshot(render.Box{X: 0, Y: s.Top, W: float64(p.Width), H: s.Height()}, opts.Format, opts.Quality)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		out = append(out, Image{Data: data, Format: opts.Format, W: float64(p.Width), H: s.Height()})
	}
	return out, nil
}

func panelSections(p *render.Page, opts Options) ([]Section, error) {
	want := map[string]bool{}
	for _, t := range opts.Panels {
		if t = strings.TrimSpace(strings.ToLower(t)); t != "" {
			want[t] = true
		}
	}
	rows := p.Geometry.Rows
	var sections []Section
	current := -2 // index into rows of the open section; -1 = before any row
	for _, panel := range p.Geometry.Panels {
		if len(want) > 0 && !want[strings.ToLower(strings.TrimSpace(panel.Title))] {
			continue
		}
		row := -1
		for i, r := range rows {
			if r.Y <= panel.Y+1 {
				row = i
			}
		}
		if row != current {
			title := ""
			if row >= 0 {
				title = rows[row].Title
			}
			sections = append(sections, Section{Title: title})
			current = row
		}
		// Include a hair of margin so panel borders are not clipped.
		clip := render.Box{X: panel.X - 1, Y: panel.Y - 1, W: panel.W + 2, H: panel.H + 2}
		if clip.X < 0 {
			clip.X = 0
		}
		if clip.Y < 0 {
			clip.Y = 0
		}
		data, err := p.Screenshot(clip, opts.Format, opts.Quality)
		if err != nil {
			return nil, fmt.Errorf("panel %q: %w", panel.Title, err)
		}
		sec := &sections[len(sections)-1]
		sec.Images = append(sec.Images, Image{Data: data, Format: opts.Format, W: clip.W, H: clip.H, Title: panel.Title})
	}
	return sections, nil
}

var pageObject = regexp.MustCompile(`/Type\s*/Page[^s]`)

// CountPages counts page objects in a PDF produced by Chromium.
func CountPages(pdf []byte) int {
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		return 0
	}
	return len(pageObject.FindAllIndex(pdf, -1))
}
