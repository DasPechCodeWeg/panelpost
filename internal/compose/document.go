package compose

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"github.com/DasPechCodeWeg/panelpost/internal/brand"
	"html/template"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/render"
)

//go:embed templates/*.html
var templateFS embed.FS

// Inter (SIL Open Font License) is embedded so reports look identical on
// every host, including minimal containers without system fonts. Static
// weights embed as TrueType, which every PDF viewer renders crisply.
//
//go:embed fonts/*.woff2
var fontFS embed.FS

const (
	rangeLatin    = "U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, U+0308, U+0329, U+2000-206F, U+20AC, U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD"
	rangeLatinExt = "U+0100-02BA, U+02BD-02C5, U+02C7-02CC, U+02CE-02D7, U+02DD-02FF, U+0304, U+0308, U+0329, U+1D00-1DBF, U+1E00-1E9F, U+1EF2-1EFF, U+2020, U+20A0-20AB, U+20AD-20C0, U+2113, U+2C60-2C7F, U+A720-A7FF"
)

var fontFaceCSS = func() template.CSS {
	var b strings.Builder
	for _, weight := range []string{"400", "500", "600", "700"} {
		for _, subset := range []struct{ name, rng string }{{"latin", rangeLatin}, {"latin-ext", rangeLatinExt}} {
			data, err := fontFS.ReadFile("fonts/inter-" + subset.name + "-" + weight + "-normal.woff2")
			if err != nil {
				panic(err)
			}
			fmt.Fprintf(&b, "@font-face { font-family: \"Inter\"; font-style: normal; font-weight: %s; font-display: block; src: url(data:font/woff2;base64,%s) format(\"woff2\"); unicode-range: %s; }\n",
				weight, base64.StdEncoding.EncodeToString(data), subset.rng)
		}
	}
	return template.CSS(b.String())
}()

var tmpl = template.Must(template.New("report.html").Funcs(template.FuncMap{
	"join": strings.Join,
}).ParseFS(templateFS, "templates/*.html"))

// Paper sizes in millimetres (portrait).
var papers = map[string][2]float64{
	"A4":     {210, 297},
	"A3":     {297, 420},
	"Letter": {215.9, 279.4},
}

// Margins in millimetres.
const (
	marginTop    = 15.0
	marginBottom = 13.0
	marginSide   = 11.0
	titleBlockMM = 20.0 // compact title shown on page one without a cover
)

// Branding is the visual identity applied to a report.
type Branding struct {
	CompanyName string
	Logo        []byte // PNG, JPEG, SVG or WebP
	Accent      string // #rrggbb
	FooterText  string
	Intro       string
}

// KV is a labelled value shown on the cover.
type KV struct {
	Key   string
	Value string
}

// Document is everything needed to lay out one report.
type Document struct {
	Title       string
	Dashboard   string
	Period      string
	TimeZone    string
	Subtitle    string // e.g. "Prepared for Acme Corp"
	Variables   []KV
	GeneratedAt time.Time
	Branding    Branding
	Credit      bool // show the "Generated with Panelpost" line
	Paper       string
	Landscape   bool
	Cover       bool
	Mode        string // "dashboard" or "panels"
	Columns     int
	Theme       string
	Notes       []string // rendering notes printed at the end, if any
}

// Page geometry derived from the paper settings.
type geometry struct {
	WidthMM, HeightMM  float64
	ContentW, ContentH float64
	PageSize           string
}

func (d Document) geometry() geometry {
	p, ok := papers[d.Paper]
	if !ok {
		p = papers["A4"]
	}
	w, h := p[0], p[1]
	orientation := "portrait"
	if d.Landscape {
		w, h = h, w
		orientation = "landscape"
	}
	name := d.Paper
	if name == "" {
		name = "A4"
	}
	return geometry{
		WidthMM: w, HeightMM: h,
		ContentW: w - 2*marginSide,
		ContentH: h - marginTop - marginBottom,
		PageSize: name + " " + orientation,
	}
}

// SliceHeights returns how many dashboard pixels fit on the first and on
// subsequent pages, for a dashboard rendered width pixels wide.
func (d Document) SliceHeights(width int) (first, rest float64) {
	g := d.geometry()
	pxPerMM := float64(width) / g.ContentW
	rest = math.Floor((g.ContentH - 2) * pxPerMM)
	first = rest
	if !d.Cover {
		first = math.Floor((g.ContentH - titleBlockMM - 2) * pxPerMM)
	}
	return first, rest
}

// PanelScale returns millimetres per CSS pixel for panels mode, so that
// panels keep the proportions they have on the dashboard.
func (d Document) PanelScale(width int) float64 {
	if width <= 0 {
		return 0
	}
	return d.geometry().ContentW / float64(width) * 0.985
}

// ContentWidthMM is the printable width of a page.
func (d Document) ContentWidthMM() float64 { return d.geometry().ContentW }

// PanelMaxHeightMM is the tallest a single panel image may be printed.
func (d Document) PanelMaxHeightMM() float64 {
	return d.geometry().ContentH - 12
}

// Image is an encoded screenshot.
type Image struct {
	Data    []byte
	Format  string  // png or jpeg
	W, H    float64 // CSS pixels of the captured region
	Title   string
	WidthMM float64 // printed width in panels mode
}

// DataURI encodes the image for inline embedding.
func (i Image) DataURI() template.URL {
	mime := "image/png"
	if i.Format == "jpeg" {
		mime = "image/jpeg"
	}
	return template.URL("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(i.Data))
}

// Section groups panel images under an optional row title.
type Section struct {
	Title  string
	Images []Image
}

type view struct {
	Document
	FontFace   template.CSS
	CreditLine string
	G          geometry
	SheetMaxMM float64
	FirstMaxMM float64
	Accent     string
	AccentSoft string
	LogoURI    template.URL
	HeaderL    string
	HeaderR    string
	FooterL    string
	Pages      []Image
	Sections   []Section
	PanelMaxMM float64
	Generated  string
	Dark       bool
}

// HTML renders the document. Pages are used in dashboard mode, sections in
// panels mode.
func (d Document) HTML(pages []Image, sections []Section) (string, error) {
	accent := d.Branding.Accent
	if accent == "" {
		accent = "#1f6feb"
	}
	v := view{
		Document:   d,
		FontFace:   fontFaceCSS,
		CreditLine: brand.Credit(),
		G:          d.geometry(),
		Accent:     accent,
		AccentSoft: soften(accent),
		Pages:      pages,
		Sections:   sections,
		PanelMaxMM: d.PanelMaxHeightMM(),
		SheetMaxMM: d.geometry().ContentH - 1,
		FirstMaxMM: d.geometry().ContentH - 1,
		Generated:  d.GeneratedAt.Format("2 January 2006 15:04 MST"),
		Dark:       d.Theme == "dark",
	}
	if !d.Cover {
		v.FirstMaxMM = d.geometry().ContentH - titleBlockMM - 1
	}
	if len(d.Branding.Logo) > 0 {
		v.LogoURI = template.URL("data:" + sniff(d.Branding.Logo) + ";base64," + base64.StdEncoding.EncodeToString(d.Branding.Logo))
	}
	left := d.Title
	if d.Branding.CompanyName != "" {
		left = d.Branding.CompanyName + " · " + d.Title
	}
	if d.Subtitle != "" {
		left += " · " + d.Subtitle
	}
	v.HeaderL = left
	v.HeaderR = d.Period
	switch {
	case d.Branding.FooterText != "" && d.Credit:
		v.FooterL = d.Branding.FooterText + " · Generated with Panelpost"
	case d.Branding.FooterText != "":
		v.FooterL = d.Branding.FooterText
	case d.Credit:
		v.FooterL = brand.Credit()
	default:
		v.FooterL = "Generated " + v.Generated
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "report.html", v); err != nil {
		return "", fmt.Errorf("render report template: %w", err)
	}
	return buf.String(), nil
}

func sniff(b []byte) string {
	if bytes.Contains(b[:min(len(b), 512)], []byte("<svg")) {
		return "image/svg+xml"
	}
	return http.DetectContentType(b)
}

// soften mixes a colour with white for backgrounds.
func soften(hex string) string {
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return "#eef4ff"
	}
	mix := func(c int) int { return c + (255-c)*88/100 }
	return fmt.Sprintf("#%02x%02x%02x", mix(r), mix(g), mix(b))
}

// Crop describes an image to cut from a live page.
type Crop struct {
	Box   render.Box
	Title string
}
