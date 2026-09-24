// Package brand holds the product's public identity in one place.
package brand

// Home is the product's public home page. Override at build time with
// -ldflags "-X github.com/DasPechCodeWeg/panelpost/internal/brand.Home=https://example.com".
var Home = "https://github.com/DasPechCodeWeg/panelpost"

// URL returns a link to a section of the home page, such as "#editions".
func URL(anchor string) string { return Home + anchor }

// Credit is the line printed in Community-edition reports.
func Credit() string { return "Generated with Panelpost" }
